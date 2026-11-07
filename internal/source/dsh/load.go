// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Load scans the sessions root and returns the rows, ledger, and events.
func Load(filters Filters) *Source {
	expandedRoot := expandHome(filters.Root)
	source := &Source{
		Files:    discoverFiles(expandedRoot),
		Sessions: make(map[string]struct{}),
		Rows:     make([]*Row, 0, 1024),
		Ledger:   make([]*LedgerEntry, 0, 64),
		Timing:   newTimingCounts(),
		Events: Events{
			Subagents:        make(map[string]struct{}),
			Branches:         make(map[string]struct{}),
			CompactionEvents: make([]CompactionEvent, 0),
		},
		DuplicateSessions: make(map[string]int64),
		Outcomes:          make(map[string]string),
	}
	source.Counts.Files = int64(len(source.Files))
	projMeta := loadProjCache(expandedRoot, filters.IncludeTitles)
	seen := make(map[string]struct{}, 4096)
	for _, path := range source.Files {
		loadFile(source, path, seen, projMeta, filters)
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

type stepKey struct {
	turn int64
	step int64
}

type pendingStep struct {
	line  int
	key   stepKey
	ts    *time.Time
	model string
}

type completedStep struct {
	line       int
	key        stepKey
	id         string
	ts         *time.Time
	startTS    *time.Time
	model      string
	provider   string
	effort     string
	out        int64
	reas       int64
	inp        int64
	cacheRead  int64
	cacheWrite int64
	tools      []ToolDetail
}

func loadFile(source *Source, path string, seen map[string]struct{}, projMeta map[string]projSessionMeta, filters Filters) {
	fileID := defaultSessionIDForPath(path)
	subagent := isSubagentPath(path)
	sessionID := fileID
	if subagent {
		sessionID = parentSessionID(path)
	}

	records := readJSONL(path)
	if len(records) == 0 {
		return
	}

	cwd := inferCWDFromPath(path)
	sessionTitle := ""
	currentProvider := "deepseek"
	currentModel := ""
	currentEffort := "unset"

	var pendingSteps []pendingStep
	pendingByKey := make(map[stepKey]int)
	consumedPending := make(map[int]bool)

	var completed []completedStep
	stepIndexByKey := make(map[stepKey]int)
	callIDStepIndex := make(map[string]struct {
		stepIdx int
		toolIdx int
	})
	queuedToolsByStep := make(map[stepKey][]ToolDetail)
	queuedCallIDs := make(map[string]struct {
		key     stepKey
		toolIdx int
	})

	for index, rec := range records {
		rtype := asString(rec["type"])
		data := asObject(rec["data"])

		switch rtype {
		case "session":
			if id := strings.TrimSpace(asString(rec["id"])); id != "" && !subagent {
				sessionID = id
			}
			if c := strings.TrimSpace(asString(rec["cwd"])); c != "" {
				cwd = c
			}
			if toInt(rec["delegationDepth"]) > 0 {
				subagent = true
			}
		case "session/title":
			if filters.IncludeTitles {
				if t := strings.TrimSpace(asString(data["title"])); t != "" {
					sessionTitle = t
				}
			}
		case "request/header":
			cfg := asObject(asObject(data["header"])["config"])
			if p := strings.TrimSpace(asString(cfg["provider"])); p != "" {
				currentProvider = p
			}
			if m := strings.TrimSpace(asString(cfg["model"])); m != "" {
				currentModel = m
				updateLastPendingModel(pendingSteps, consumedPending, m)
			}
			if eff := strings.TrimSpace(asString(cfg["reasoningEffort"])); eff != "" {
				currentEffort = eff
			}
		case "request/context":
			if p := strings.TrimSpace(asString(data["provider"])); p != "" {
				currentProvider = p
			}
			if m := strings.TrimSpace(asString(data["model"])); m != "" {
				currentModel = m
				updateLastPendingModel(pendingSteps, consumedPending, m)
			}
		case "model/selection":
			if p := strings.TrimSpace(asString(data["provider"])); p != "" {
				currentProvider = p
			}
			if m := strings.TrimSpace(asString(data["model"])); m != "" {
				currentModel = m
			}
			if eff := strings.TrimSpace(asString(data["reasoningEffort"])); eff != "" {
				currentEffort = eff
			}
			source.Events.ModelChanges++
		case "step/start":
			key := stepKey{
				turn: toInt(data["turn"]),
				step: toInt(data["step"]),
			}
			pIdx := len(pendingSteps)
			pendingSteps = append(pendingSteps, pendingStep{
				line:  index,
				key:   key,
				ts:    parseTimestamp(rec["time"]),
				model: currentModel,
			})
			pendingByKey[key] = pIdx
		case "assistant/message":
			key := stepKey{
				turn: toInt(data["turn"]),
				step: toInt(data["step"]),
			}
			var startTS *time.Time
			if pIdx, ok := pendingByKey[key]; ok && !consumedPending[pIdx] {
				consumedPending[pIdx] = true
				startTS = pendingSteps[pIdx].ts
			}
			msg := asObject(data["message"])
			src := asObject(msg["source"])
			model := strings.TrimSpace(asString(src["model"]))
			if model == "" {
				model = currentModel
			}
			provider := strings.TrimSpace(asString(src["provider"]))
			if provider == "" {
				provider = currentProvider
			}
			if provider == "" {
				provider = inferProvider(model)
			}
			usage := asObject(data["usage"])
			if usage == nil {
				usage = make(map[string]any)
			}
			inp := toInt(usage["inputTokens"])
			if inp == 0 {
				inp = toInt(usage["uncachedInputTokens"])
			}
			out := toInt(usage["outputTokens"])
			reas := toInt(usage["reasoningTokens"])
			if reas < 0 {
				reas = 0
			}
			if reas > out {
				reas = out
			}
			cr := toInt(usage["cacheReadTokens"])
			cw := toInt(usage["cacheWriteTokens"])
			eff := strings.TrimSpace(currentEffort)
			if eff == "" {
				eff = "unset"
			}
			var tools []ToolDetail
			if queued, hasQueued := queuedToolsByStep[key]; hasQueued {
				tools = append(tools, queued...)
				delete(queuedToolsByStep, key)
			}
			stepIdx := len(completed)
			completed = append(completed, completedStep{
				line:       index,
				key:        key,
				id:         strings.TrimSpace(asString(msg["id"])),
				ts:         parseTimestamp(rec["time"]),
				startTS:    startTS,
				model:      model,
				provider:   provider,
				effort:     eff,
				out:        out,
				reas:       reas,
				inp:        inp,
				cacheRead:  cr,
				cacheWrite: cw,
				tools:      tools,
			})
			stepIndexByKey[key] = stepIdx
			for cid, qLoc := range queuedCallIDs {
				if qLoc.key == key {
					callIDStepIndex[cid] = struct {
						stepIdx int
						toolIdx int
					}{stepIdx: stepIdx, toolIdx: qLoc.toolIdx}
					delete(queuedCallIDs, cid)
				}
			}
		case "tool/call":
			key := stepKey{
				turn: toInt(data["turn"]),
				step: toInt(data["step"]),
			}
			tname := normalizeToolName(asString(data["name"]))
			callID := strings.TrimSpace(asString(data["callId"]))
			if stepIdx, ok := stepIndexByKey[key]; ok {
				toolIdx := len(completed[stepIdx].tools)
				completed[stepIdx].tools = append(completed[stepIdx].tools, ToolDetail{Name: tname})
				if callID != "" {
					callIDStepIndex[callID] = struct {
						stepIdx int
						toolIdx int
					}{stepIdx: stepIdx, toolIdx: toolIdx}
				}
			} else if len(completed) > 0 && data["turn"] == nil && data["step"] == nil {
				stepIdx := len(completed) - 1
				toolIdx := len(completed[stepIdx].tools)
				completed[stepIdx].tools = append(completed[stepIdx].tools, ToolDetail{Name: tname})
				if callID != "" {
					callIDStepIndex[callID] = struct {
						stepIdx int
						toolIdx int
					}{stepIdx: stepIdx, toolIdx: toolIdx}
				}
			} else {
				toolIdx := len(queuedToolsByStep[key])
				queuedToolsByStep[key] = append(queuedToolsByStep[key], ToolDetail{Name: tname})
				if callID != "" {
					queuedCallIDs[callID] = struct {
						key     stepKey
						toolIdx int
					}{key: key, toolIdx: toolIdx}
				}
			}

		case "tool/result":
			if !isToolResultError(data) {
				continue
			}
			callID := strings.TrimSpace(asString(asObject(data["message"])["toolCallId"]))
			if callID == "" {
				callID = strings.TrimSpace(asString(data["callId"]))
			}
			if callID != "" {
				if loc, ok := callIDStepIndex[callID]; ok {
					completed[loc.stepIdx].tools[loc.toolIdx].Error = true
					continue
				}
				if qLoc, ok := queuedCallIDs[callID]; ok {
					queue := queuedToolsByStep[qLoc.key]
					if qLoc.toolIdx < len(queue) {
						queue[qLoc.toolIdx].Error = true
						queuedToolsByStep[qLoc.key] = queue
					}
					continue
				}
			}
			key := stepKey{
				turn: toInt(data["turn"]),
				step: toInt(data["step"]),
			}
			if stepIdx, ok := stepIndexByKey[key]; ok && len(completed[stepIdx].tools) > 0 {
				last := len(completed[stepIdx].tools) - 1
				completed[stepIdx].tools[last].Error = true
			}

		case "compaction/summary":
			tokensBefore := toInt(asObject(data["usage"])["inputTokens"])
			if tokensBefore <= 0 {
				tokensBefore = toInt(data["shadowedTokenCount"])
			}
			source.Events.Compactions++
			source.Events.CompactionTokens += tokensBefore
			sid := sessionID
			source.Events.CompactionEvents = append(source.Events.CompactionEvents, CompactionEvent{
				TS:           parseTimestamp(rec["time"]),
				SessionID:    &sid,
				TokensBefore: tokensBefore,
			})

		case "turn/end":
			if !subagent {
				if kind := strings.TrimSpace(asString(asObject(data["reason"])["kind"])); kind != "" {
					source.Outcomes[sessionID] = kind
				}
			}
		}
	}

	resolvedProject := resolveCWD(cwd, sessionID, projMeta)
	if filters.IncludeTitles && sessionTitle == "" {
		if meta, ok := projMeta[sessionID]; ok && meta.Title != "" {
			sessionTitle = meta.Title
		}
	}
	var titlePtr *string
	if filters.IncludeTitles && sessionTitle != "" {
		titlePtr = &sessionTitle
	}

	for i, ps := range pendingSteps {
		if consumedPending[i] {
			continue
		}
		if ps.ts != nil {
			day := LocalDate(*ps.ts)
			if (filters.Since != "" && day < filters.Since) || (filters.Until != "" && day > filters.Until) {
				source.Counts.FilteredDate++
				continue
			}
		}
		source.Events.Incomplete++
		source.Counts.Incomplete++
		source.appendLedger(
			ReasonIncomplete,
			fmt.Sprintf("%s|%s:step-start-%d", sessionID, fileID, ps.line),
			sessionID,
			resolvedProject,
			ps.model,
			ps.ts,
			0, 0, 0, 0,
		)
	}

	for _, tools := range queuedToolsByStep {
		source.Events.UnmatchedCommits += int64(len(tools))
	}

	contributed := false
	for _, call := range completed {
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
			id = fmt.Sprintf("%s|%s:line-%d", sessionID, fileID, call.line)
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
			source.appendLedger(ReasonEmpty, id, sessionID, resolvedProject, call.model, call.ts, 0, call.inp, call.cacheRead, call.cacheWrite)
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			source.Counts.Duplicates++
			source.DuplicateSessions[sessionID]++
			source.DuplicateTokens += call.out
			source.appendLedger(ReasonDuplicate, id, sessionID, resolvedProject, call.model, call.ts, call.out, call.inp, call.cacheRead, call.cacheWrite)
			continue
		}
		seen[id] = struct{}{}
		if call.ts == nil {
			source.Counts.NoTimestamp++
			source.appendLedger(ReasonNoTimestamp, id, sessionID, resolvedProject, call.model, nil, call.out, call.inp, call.cacheRead, call.cacheWrite)
			continue
		}

		var duration *float64
		if call.startTS != nil && !call.ts.Before(*call.startTS) {
			secs := DeltaSeconds(call.ts.Sub(*call.startTS))
			if secs > 0 {
				duration = &secs
			}
		}
		var tps *float64
		if duration != nil && *duration != 0 {
			rate := float64(call.out) / *duration
			tps = &rate
		}

		toolNames := make([]string, 0, len(call.tools))
		var toolErrors int64
		for _, td := range call.tools {
			toolNames = append(toolNames, td.Name)
			if td.Error {
				toolErrors++
			}
		}
		var primaryTool *string
		if len(toolNames) > 0 {
			first := toolNames[0]
			primaryTool = &first
		}

		row := &Row{
			ID:           id,
			Model:        call.model,
			Provider:     call.provider,
			Effort:       call.effort,
			TS:           *call.ts,
			Out:          call.out,
			Reas:         call.reas,
			Inp:          call.inp,
			CacheRead:    call.cacheRead,
			CacheWrite:   call.cacheWrite,
			Tool:         len(toolNames) > 0,
			Tools:        toolNames,
			ToolDetails:  call.tools,
			PrimaryTool:  primaryTool,
			NTools:       int64(len(toolNames)),
			ToolErrors:   toolErrors,
			Web:          hasWebTool(toolNames),
			Thinking:     call.reas > 0,
			Subagent:     subagent,
			DT:           duration,
			TPS:          tps,
			SessionID:    sessionID,
			SessionTitle: titlePtr,
			ProjectPath:  resolvedProject,
			Entrypoint:   "dsh",
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

func updateLastPendingModel(pendingSteps []pendingStep, consumed map[int]bool, model string) {
	for i := len(pendingSteps) - 1; i >= 0; i-- {
		if !consumed[i] {
			pendingSteps[i].model = model
			return
		}
	}
}

func isToolResultError(data map[string]any) bool {
	if data == nil {
		return false
	}
	if truthy(data["isError"]) || truthy(data["error"]) {
		return true
	}
	msg := asObject(data["message"])
	return truthy(msg["isError"]) || truthy(msg["error"])
}

func normalizeToolName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func parentSessionID(path string) string {
	segments := strings.Split(filepath.ToSlash(path), "/")
	for i, segment := range segments {
		if (segment == "subagent" || segment == "subagents") && i > 0 {
			return segments[i-1]
		}
	}
	return filepath.Base(filepath.Dir(path))
}

func isSubagentPath(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == "subagent" || segment == "subagents" {
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
