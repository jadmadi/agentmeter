// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Load scans the session root and returns the rows, ledger and events.
//
// Behavior follows telemetry.py:load_rows() step for step. In particular:
//   - the model filter runs before the output/timestamp/dedup checks;
//   - the watermark sees every assistant timestamp, filtered rows included;
//   - noOutput is checked before dedup, and "seen" is marked before the
//     missing-timestamp check, so duplicates are counted even when the first
//     copy had no timestamp;
//   - the date filter runs last, after dedup, and writes no ledger entry.
func Load(filters Filters) *Source {
	source := &Source{
		Files:             discoverFiles(filters.Root),
		Sessions:          make(map[string]struct{}),
		Rows:              make([]*Row, 0, 1024),
		Ledger:            make([]*LedgerEntry, 0, 64),
		Timing:            newTimingCounts(),
		DuplicateSessions: make(map[string]int64),
		Events: Events{
			Hooks:             make([]Hook, 0),
			ToolWindows:       make([]ToolWindow, 0),
			CompactionEvents:  make([]CompactionEvent, 0),
			ModelChangeEvents: make([]ModelChangeEvent, 0),
			Branches:          make(map[string]struct{}),
		},
	}
	source.Counts.Files = int64(len(source.Files))
	seen := make(map[string]struct{}, 4096)
	for _, path := range source.Files {
		loadFile(source, path, seen, filters)
	}
	return source
}

func newTimingCounts() map[string]int64 {
	timing := make(map[string]int64, len(TimingReasons))
	for _, reason := range TimingReasons {
		timing[reason] = 0
	}
	return timing
}

func loadFile(source *Source, path string, seen map[string]struct{}, filters Filters) {
	var (
		sessionID    string
		cwd          string
		branch       *string
		entrypoint   string
		sessionTitle *string
		contributed  bool
	)
	preHooks := make(map[string]float64)

	if sidecar := readSidecar(path); sidecar != nil {
		entrypoint = stringOr(sidecar["entrypoint"], "")
		if filters.IncludeTitles {
			if title := stringOr(sidecar["title"], ""); title != "" {
				sessionTitle = &title
			}
		}
	}

	lines, err := readLines(path)
	if err != nil {
		// Python catches OSError here and skips the file.
		return
	}

	entries := newOrderedEntries()
	for _, line := range lines {
		record := decodeLine(line)
		if record == nil {
			continue
		}
		kindValue, hasKind := record["type"]
		kindIsNil := !hasKind || kindValue == nil
		kind := asString(kindValue)
		switch {
		case kind == "session":
			if id := stringOr(record["id"], ""); id != "" {
				sessionID = id
			}
			if cwdValue := stringOr(record["cwd"], ""); cwdValue != "" {
				cwd = cwdValue
			}
		case kind == "message" && truthy(record["id"]):
			entries.set(pyStr(record["id"]), record)
		case kind == "compaction":
			tokensBefore := toInt(record["tokensBefore"])
			source.Events.Compactions++
			source.Events.CompactionTokens += tokensBefore
			source.Events.CompactionEvents = append(source.Events.CompactionEvents, CompactionEvent{
				TS:           parseTimestamp(record["timestamp"]),
				SessionID:    sessionRef(record["sessionId"], sessionID),
				TokensBefore: tokensBefore,
			})
		case kind == "model_change":
			source.Events.ModelChanges++
			model := stringOr(record["model"], "")
			if model == "" {
				model = stringOr(record["to"], "")
			}
			source.Events.ModelChangeEvents = append(source.Events.ModelChangeEvents, ModelChangeEvent{
				TS:        parseTimestamp(record["timestamp"]),
				SessionID: sessionRef(record["sessionId"], sessionID),
				Model:     model,
			})
		case kind == "file-history-snapshot":
			source.Events.FileSnapshots++
		case kindIsNil && truthy(record["eventName"]):
			eventName := stringOr(record["eventName"], "")
			hook := Hook{
				EventName: stringPtrValue(record["eventName"]),
				HookName:  stringPtrValue(record["hookName"]),
				HookType:  stringPtrValue(record["hookType"]),
				ToolName:  stringPtrValue(record["toolName"]),
				Duration:  numericPtr(record["duration"]),
				ExitCode:  numericPtr(record["exitCode"]),
				Blocked:   truthy(record["blocked"]),
				TS:        parseTimestamp(record["timestamp"]),
			}
			source.Events.Hooks = append(source.Events.Hooks, hook)

			toolUseID := stringOr(record["toolUseId"], "")
			switch {
			case eventName == "PreToolUse" && toolUseID != "" && hook.TS != nil:
				preHooks[toolUseID] = UnixFloat(*hook.TS)
			case eventName == "PostToolUse" && toolUseID != "" && hook.TS != nil:
				if started, ok := preHooks[toolUseID]; ok {
					window := UnixFloat(*hook.TS) - started
					if window > 0 && window < 3600 {
						source.Events.ToolWindows = append(source.Events.ToolWindows, ToolWindow{
							Tool:    hook.ToolName,
							Seconds: window,
						})
					}
				}
			}
		}
		if truthy(record["gitBranch"]) {
			if name := stringOr(record["gitBranch"], ""); name != "" {
				branch = &name
			}
		}
	}

	fileSession := sessionID
	if fileSession == "" {
		fileSession = fileStem(path)
	}

	for _, messageID := range entries.order {
		record := entries.get(messageID)
		message := asObject(record["message"])
		usage := asObject(record["usage"])
		if asString(message["role"]) != "assistant" {
			continue
		}
		source.Counts.Scanned++
		source.Counts.Assistant++

		ts := parseTimestamp(record["timestamp"])
		if ts != nil && (source.MaxTS == nil || ts.After(*source.MaxTS)) {
			source.MaxTS = ts
		}
		model := stringOr(record["model"], "")
		if len(filters.ModelFilters) > 0 && !matchesModel(model, filters.ModelFilters) {
			source.Counts.FilteredModel++
			continue
		}
		source.Counts.Considered++

		session := fileSession
		project := cwd
		if project == "" {
			project = "(unknown)"
		}
		outTokens := toInt(usage["outputTokens"])
		inp := toInt(usage["inputTokens"])
		cacheRead := toInt(usage["cacheReadTokens"])
		cacheWrite := toInt(usage["cacheWriteTokens"])
		cost := toFloat(usage["costUsd"])

		if outTokens <= 0 {
			source.Counts.NoOutput++
			source.appendLedger(ReasonNoOutput, messageID, session, project, model, ts, 0, inp, cacheRead, cacheWrite, cost)
			continue
		}
		if _, duplicate := seen[messageID]; duplicate {
			source.Counts.Duplicates++
			source.DuplicateSessions[session]++
			source.DuplicateTokens += outTokens
			source.DuplicateCost += cost
			source.appendLedger(ReasonDuplicate, messageID, session, project, model, ts, outTokens, inp, cacheRead, cacheWrite, cost)
			continue
		}
		seen[messageID] = struct{}{}
		if ts == nil {
			source.Counts.NoTimestamp++
			source.appendLedger(ReasonNoTimestamp, messageID, session, project, model, nil, outTokens, inp, cacheRead, cacheWrite, cost)
			continue
		}
		day := LocalDate(*ts)
		if (filters.Since != "" && day < filters.Since) || (filters.Until != "" && day > filters.Until) {
			source.Counts.FilteredDate++
			continue
		}

		var toolNames []string
		var nThinking, nText int64
		if content, ok := message["content"].([]any); ok {
			for _, blockValue := range content {
				block := asObject(blockValue)
				if block == nil {
					continue
				}
				switch asString(block["type"]) {
				case "tool_use":
					toolNames = append(toolNames, stringOr(block["name"], ""))
				case "thinking":
					nThinking++
				case "text":
					nText++
				}
			}
		}
		hasTool := len(toolNames) > 0

		var parentTS *time.Time
		if parentID := stringOr(record["parentId"], ""); parentID != "" {
			if parent := entries.get(parentID); parent != nil {
				parentTS = parseTimestamp(parent["timestamp"])
			}
		}
		var duration *float64
		if parentTS != nil {
			seconds := DeltaSeconds(ts.Sub(*parentTS))
			if seconds > 0 {
				duration = &seconds
			}
		}
		var tps *float64
		if duration != nil && *duration != 0 {
			rate := float64(outTokens) / *duration
			tps = &rate
		}

		toolDetails := make([]ToolDetail, 0, len(toolNames))
		for _, name := range toolNames {
			toolDetails = append(toolDetails, ToolDetail{Name: name})
		}
		var primaryTool *string
		if len(toolNames) > 0 {
			first := toolNames[0]
			primaryTool = &first
		}
		effort := stringOr(record["effort"], "unset")
		entry := entrypoint
		if entry == "" {
			entry = "interactive"
		}
		var entryRaw *string
		if entrypoint != "" {
			raw := entrypoint
			entryRaw = &raw
		}

		row := &Row{
			ID:            messageID,
			Model:         model,
			Effort:        effort,
			TS:            *ts,
			Out:           outTokens,
			Inp:           inp,
			CacheRead:     cacheRead,
			CacheWrite:    cacheWrite,
			Cost:          cost,
			Tool:          hasTool,
			Tools:         toolNames,
			ToolDetails:   toolDetails,
			PrimaryTool:   primaryTool,
			NTools:        int64(len(toolNames)),
			NThinking:     nThinking,
			NText:         nText,
			Web:           hasWebTool(toolNames),
			Thinking:      nThinking > 0,
			DT:            duration,
			TPS:           tps,
			SessionID:     session,
			SessionTitle:  sessionTitle,
			ProjectPath:   project,
			Branch:        branch,
			Entrypoint:    entry,
			EntrypointRaw: entryRaw,
		}
		row.Timing = TimingBucket(row.DT, row.Out)
		source.Timing[row.Timing]++
		source.Rows = append(source.Rows, row)
		source.Counts.Rows++
		contributed = true
	}

	if contributed && branch != nil {
		source.Events.Branches[*branch] = struct{}{}
	}
	if contributed {
		source.Sessions[fileSession] = struct{}{}
	}
}

func (source *Source) appendLedger(reason, id, session, project, model string, ts *time.Time, out, inp, cacheRead, cacheWrite int64, cost float64) {
	source.Ledger = append(source.Ledger, &LedgerEntry{
		ID:               id,
		Reason:           reason,
		SessionID:        session,
		ProjectPath:      project,
		Model:            model,
		TS:               ts,
		OutputTokens:     out,
		InputTokens:      inp,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		CostUSD:          cost,
	})
}

// TimingBucket classifies one step, in the exact order of telemetry.py.
func TimingBucket(duration *float64, outTokens int64) string {
	if duration == nil || *duration <= 0 {
		return "noTiming"
	}
	if outTokens < 20 {
		return "tooShort"
	}
	if *duration < 0.5 {
		return "tooShortDuration"
	}
	if *duration > 180 {
		return "tooLong"
	}
	rate := float64(outTokens) / *duration
	if rate < 3 {
		return "slowRate"
	}
	if rate > 400 {
		return "fastRate"
	}
	return "plausible"
}

func matchesModel(model string, filters []string) bool {
	lowered := strings.ToLower(model)
	for _, term := range filters {
		if strings.Contains(lowered, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

func hasWebTool(names []string) bool {
	for _, name := range names {
		if name == "web_search" || name == "web_fetch" {
			return true
		}
	}
	return false
}

// readSidecar loads `<id>.meta.json`. Python strips the last six characters
// (".jsonl") and appends ".meta.json"; a missing or malformed sidecar is
// silently ignored.
func readSidecar(path string) map[string]any {
	if len(path) < len(jsonlSuffix) {
		return nil
	}
	raw, err := os.ReadFile(path[:len(path)-len(jsonlSuffix)] + ".meta.json")
	if err != nil {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return asObject(value)
}

// fileStem is pathlib.Path(path).stem.
func fileStem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// sessionRef reproduces `record["sessionId"] or session_id`: a non-empty
// record value wins, otherwise the file-level session id (nil when the file
// has no session header).
func sessionRef(value any, sessionID string) *string {
	if truthy(value) {
		name := stringOr(value, "")
		return &name
	}
	if sessionID == "" {
		return nil
	}
	name := sessionID
	return &name
}

// stringPtrValue keeps a JSON string as a pointer; anything else stays nil so
// the report emits null like Python's None.
func stringPtrValue(value any) *string {
	if text, ok := value.(string); ok {
		return &text
	}
	return nil
}

// numericPtr keeps a JSON number as a pointer; non-numeric values stay nil.
func numericPtr(value any) *float64 {
	if number, ok := value.(float64); ok {
		return &number
	}
	return nil
}
