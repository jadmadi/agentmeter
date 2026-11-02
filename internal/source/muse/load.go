// SPDX-License-Identifier: AGPL-3.0-only

package muse

import (
	"path/filepath"
	"strings"
	"time"
)

// Load scans the sessions root and returns the rows, ledger and events.
//
// Filter and ledger order mirrors the other loaders:
//   - the model filter runs before the output/timestamp/dedup checks;
//   - the watermark sees every completion timestamp, filtered rows included;
//   - empty rows are ledgered before dedup, and "seen" is marked before the
//     missing-timestamp check;
//   - the date filter runs after the model filter and writes no ledger entry.
func Load(filters Filters) *Source {
	source := &Source{
		Files:    discoverFiles(filters.Root),
		Sessions: make(map[string]struct{}),
		Rows:     make([]*Row, 0, 1024),
		Ledger:   make([]*LedgerEntry, 0, 64),
		Timing:   newTimingCounts(),
		Events: Events{
			Subagents: make(map[string]struct{}),
			Branches:  make(map[string]struct{}),
		},
		DuplicateSessions: make(map[string]int64),
		Outcomes:          make(map[string]string),
		labels:            make(map[string]sessionLabel),
	}
	source.Counts.Files = int64(len(source.Files))
	index := loadIndex(filters.Root)
	seen := make(map[string]struct{}, 4096)
	for _, path := range source.Files {
		loadFile(source, path, seen, index, filters)
	}
	resolveLabels(source, index)
	return source
}

func newTimingCounts() map[string]int64 {
	timing := make(map[string]int64, len(TimingReasons))
	for _, reason := range TimingReasons {
		timing[reason] = 0
	}
	return timing
}

// modelCall is one model_response_created/model_completed pair: the created
// event carries the response_id linkage and the completed event the usage.
type modelCall struct {
	line       int
	id         string
	ts         *time.Time
	model      string
	out        int64
	reas       int64
	inp        int64
	cacheRead  int64
	cacheWrit  int64
	ms         float64
	hasMS      bool
	responseID string
}

// toolCommit is one assistant_tool_calls_committed event.
type toolCommit struct {
	responseID string
	names      []string
}

func loadFile(source *Source, path string, seen map[string]struct{}, index map[string]SessionMeta, filters Filters) {
	fileID := filepath.Base(filepath.Dir(path))
	subagent := isSubagentPath(path)
	sessionID := fileID
	if subagent {
		sessionID = parentSessionID(path)
	}
	meta := index[sessionID]

	lines, err := readLines(path)
	if err != nil {
		return
	}

	var (
		created   []modelCall
		completed []modelCall
		commits   []toolCommit
	)
	for index, line := range lines {
		record := decodeLine(line)
		if record == nil {
			continue
		}
		if _, omitted := record["omitted_record"]; omitted {
			source.Events.Omissions++
			source.Counts.Omissions++
			continue
		}
		// Model and tool events arrive as runtime.session; the session
		// lifecycle (session_end, workspace_branch) has its own payload
		// types. Every other type (user intents, tool effects, frames)
		// is ignored, so message content is never within reach.
		switch asString(record["payload_type"]) {
		case "runtime.session", "session.end", "session.workspace_branch.observed":
		default:
			continue
		}
		payload := asObject(record["payload"])
		if payload == nil {
			continue
		}
		event := asObject(payload["event"])
		if event == nil {
			// Lifecycle events (session_end, workspace_branch) sit flat
			// in the payload with no event wrapper.
			event = payload
		}
		ts := fromMicros(record["recorded_at"])
		switch asString(event["kind"]) {
		case "model_response_created":
			created = append(created, modelCall{line: index, ts: ts, responseID: asString(event["response_id"])})
		case "model_completed":
			usage := asObject(event["usage"])
			if usage == nil {
				usage = make(map[string]any)
			}
			out := toInt(usage["output_tokens"])
			reas := toInt(usage["reasoning_tokens"])
			if reas < 0 {
				reas = 0
			}
			if reas > out {
				reas = out
			}
			ms, hasMS := eventMS(event["duration_ms"])
			completed = append(completed, modelCall{
				line:      index,
				id:        asString(record["id"]),
				ts:        ts,
				model:     stringOr(event["model"], ""),
				out:       out,
				reas:      reas,
				inp:       toInt(usage["input_tokens"]),
				cacheRead: toInt(usage["cache_read_tokens"]),
				cacheWrit: toInt(usage["cache_write_tokens"]),
				ms:        ms,
				hasMS:     hasMS,
			})
		case "assistant_tool_calls_committed":
			var names []string
			if calls, ok := event["tool_calls"].([]any); ok {
				for _, callValue := range calls {
					call := asObject(callValue)
					if call == nil {
						continue
					}
					// Only the tool name is read; args are never touched.
					names = append(names, stringOr(call["name"], ""))
				}
			}
			commits = append(commits, toolCommit{
				responseID: asString(event["response_id"]),
				names:      names,
			})
		case "session_end":
			// Only the main log's exit reason is a session outcome; child
			// exits describe child runtimes. Last record wins.
			if !subagent {
				if record := asObject(event["record"]); record != nil {
					if exit := asString(record["exit_reason"]); exit != "" {
						source.Outcomes[sessionID] = exit
					}
				}
			}
		case "workspace_branch":
			// In-session branch and workspace observation. Only the VCS
			// reference name and workspace root are read; the commit hash
			// is ignored. A null reference means no branch context and
			// never wipes a known branch.
			if record := asObject(event["record"]); record != nil {
				observeLabel(source, sessionID, sessionLabel{
					Branch:    referenceName(record["reference"]),
					Workspace: asString(record["workspace_root"]),
					TS:        ts,
				})
			}
		}
	}

	// Pair completions with creations in file order: each completion consumes
	// the latest unpaired creation before it, so an abandoned creation is
	// left over instead of shifting every later pair. The pair carries the
	// response_id used for tool linkage; creations left over are in-flight
	// or abandoned calls.
	responseOf := make(map[int]string, len(completed))
	used := make([]bool, len(created))
	createdIdx := 0
	for i, call := range completed {
		for createdIdx < len(created) && created[createdIdx].line < call.line {
			createdIdx++
		}
		for j := createdIdx - 1; j >= 0; j-- {
			if !used[j] {
				used[j] = true
				responseOf[i] = created[j].responseID
				break
			}
		}
	}
	for j, call := range created {
		if used[j] {
			continue
		}
		if call.ts != nil {
			day := LocalDate(*call.ts)
			if (filters.Since != "" && day < filters.Since) || (filters.Until != "" && day > filters.Until) {
				source.Counts.FilteredDate++
				continue
			}
		}
		source.Events.Incomplete++
		source.Counts.Incomplete++
		source.appendLedger(ReasonIncomplete, sessionID+"|"+fileID+":created-"+itoa(call.line),
			sessionID, projectOf(meta), "", call.ts, 0, 0, 0, 0)
	}

	// Attach tool commits by response id. Commits without a matching step
	// (redacted or filtered-out calls) are counted, never guessed.
	stepByResponse := make(map[string]int, len(completed))
	for i := range completed {
		if id := responseOf[i]; id != "" {
			if _, exists := stepByResponse[id]; !exists {
				stepByResponse[id] = i
			}
		}
	}
	toolsOf := make(map[int][]string, len(commits))
	for _, commit := range commits {
		i, ok := stepByResponse[commit.responseID]
		if !ok || commit.responseID == "" {
			source.Events.UnmatchedCommits++
			continue
		}
		toolsOf[i] = append(toolsOf[i], commit.names...)
	}

	project := projectOf(meta)
	var branch *string
	if meta.Branch != "" {
		branch = &meta.Branch
	}
	var title *string
	if filters.IncludeTitles && meta.Title != "" {
		title = &meta.Title
	}

	contributed := false
	for i, call := range completed {
		source.Counts.Scanned++
		if call.ts != nil && (source.MaxTS == nil || call.ts.After(*source.MaxTS)) {
			source.MaxTS = call.ts
		}
		if len(filters.ModelFilters) > 0 && !matchesModel(call.model, filters.ModelFilters) {
			source.Counts.FilteredModel++
			continue
		}
		source.Counts.Considered++

		id := call.id
		if id == "" {
			id = sessionID + "|" + fileID + ":line-" + itoa(call.line)
		}
		if call.ts != nil {
			day := LocalDate(*call.ts)
			if (filters.Since != "" && day < filters.Since) || (filters.Until != "" && day > filters.Until) {
				if call.out > 0 {
					seen[id] = struct{}{}
				}
				source.Counts.FilteredDate++
				continue
			}
		}
		if call.out <= 0 {
			source.Counts.Empty++
			source.appendLedger(ReasonEmpty, id, sessionID, project, call.model, call.ts, 0, call.inp, call.cacheRead, call.cacheWrit)
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			source.Counts.Duplicates++
			source.DuplicateSessions[sessionID]++
			source.DuplicateTokens += call.out
			source.appendLedger(ReasonDuplicate, id, sessionID, project, call.model, call.ts, call.out, call.inp, call.cacheRead, call.cacheWrit)
			continue
		}
		seen[id] = struct{}{}
		if call.ts == nil {
			source.Counts.NoTimestamp++
			source.appendLedger(ReasonNoTimestamp, id, sessionID, project, call.model, nil, call.out, call.inp, call.cacheRead, call.cacheWrit)
			continue
		}

		toolNames := toolsOf[i]
		details := make([]ToolDetail, 0, len(toolNames))
		for _, name := range toolNames {
			details = append(details, ToolDetail{Name: name})
		}
		var duration *float64
		if call.hasMS && call.ms > 0 {
			seconds := call.ms / 1000
			duration = &seconds
		}
		var tps *float64
		if duration != nil && *duration != 0 {
			rate := float64(call.out) / *duration
			tps = &rate
		}
		var primaryTool *string
		if len(toolNames) > 0 {
			first := toolNames[0]
			primaryTool = &first
		}
		row := &Row{
			ID:           id,
			Model:        call.model,
			Effort:       "unset",
			TS:           *call.ts,
			Out:          call.out,
			Reas:         call.reas,
			Inp:          call.inp,
			CacheRead:    call.cacheRead,
			CacheWrite:   call.cacheWrit,
			Tool:         len(toolNames) > 0,
			Tools:        toolNames,
			ToolDetails:  details,
			PrimaryTool:  primaryTool,
			NTools:       int64(len(toolNames)),
			Web:          hasWebTool(toolNames),
			Thinking:     call.reas > 0,
			Subagent:     subagent,
			DT:           duration,
			TPS:          tps,
			SessionID:    sessionID,
			SessionTitle: title,
			ProjectPath:  project,
			Branch:       branch,
			Entrypoint:   "muse",
		}
		row.Timing = TimingBucket(row.DT, row.Out)
		source.Timing[row.Timing]++
		source.Rows = append(source.Rows, row)
		source.Counts.Rows++
		if subagent {
			source.Counts.Subagents++
		}
		contributed = true
	}

	if contributed {
		source.Sessions[sessionID] = struct{}{}
		if subagent {
			source.Events.Subagents[fileID] = struct{}{}
		}
	}
}

// resolveLabels patches every row's workspace root and branch after all
// files are scanned: the latest in-session workspace_branch observation
// wins, the session index is the fallback. Branch events carry no labels
// until a session contributes a row, so sessions without rows stay out of
// the branch set.
func resolveLabels(source *Source, index map[string]SessionMeta) {
	bySession := make(map[string][]*Row, len(source.Sessions))
	for _, row := range source.Rows {
		bySession[row.SessionID] = append(bySession[row.SessionID], row)
	}
	for sessionID, rows := range bySession {
		meta := index[sessionID]
		branch := meta.Branch
		workspace := meta.Workspace
		if label, ok := source.labels[sessionID]; ok {
			if label.Branch != "" {
				branch = label.Branch
			}
			if label.Workspace != "" {
				workspace = label.Workspace
			}
		}
		project := "(unknown)"
		if workspace != "" {
			project = workspace
		}
		var branchPtr *string
		if branch != "" {
			value := branch
			branchPtr = &value
			source.Events.Branches[branch] = struct{}{}
		}
		for _, row := range rows {
			row.ProjectPath = project
			row.Branch = branchPtr
		}
	}
}

// referenceName reads the VCS reference name: the reference is an object
// like {"kind": "branch", "name": "main"}, or null when the session has no
// branch context.
func referenceName(value any) string {
	return asString(asObject(value)["name"])
}

// observeLabel merges one workspace_branch observation into the session's
// labels. Each field updates only when the observation carries it, so a
// branchless event keeps the last known branch. An observation older than
// the stored timestamp changes nothing.
func observeLabel(source *Source, sessionID string, label sessionLabel) {
	current, ok := source.labels[sessionID]
	if !ok {
		source.labels[sessionID] = label
		return
	}
	if label.TS != nil && current.TS != nil && label.TS.Before(*current.TS) {
		return
	}
	if label.Branch != "" {
		current.Branch = label.Branch
	}
	if label.Workspace != "" {
		current.Workspace = label.Workspace
	}
	if label.TS != nil {
		current.TS = label.TS
	}
	source.labels[sessionID] = current
}

func (source *Source) appendLedger(reason, id, session, project, model string, ts *time.Time, out, inp, cacheRead, cacheWrite int64) {
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
	})
}

// TimingBucket classifies one step: 0.5s..180s, >= 20 generated tokens and
// 3..400 tokens/s, in that order.
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

// eventMS reads a millisecond duration. Muse emits numbers; strings and
// missing values mean unusable timing, never zero.
func eventMS(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func projectOf(meta SessionMeta) string {
	if meta.Workspace != "" {
		return meta.Workspace
	}
	return "(unknown)"
}

// parentSessionID returns the session that owns a child log: the directory
// before the first "subagent" segment. Nested child logs attribute to the
// top-level session.
func parentSessionID(path string) string {
	segments := strings.Split(filepath.ToSlash(path), "/")
	for i, segment := range segments {
		if segment == "subagent" && i > 0 {
			return segments[i-1]
		}
	}
	return filepath.Base(filepath.Dir(path))
}

// isSubagentPath reports whether a session log lives under a subagent
// directory (a path segment exactly equal to "subagent").
func isSubagentPath(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == "subagent" {
			return true
		}
	}
	return false
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
		lowered := strings.ToLower(name)
		if strings.Contains(lowered, "websearch") || strings.Contains(lowered, "webfetch") ||
			strings.Contains(lowered, "web_search") || strings.Contains(lowered, "web_fetch") {
			return true
		}
	}
	return false
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
