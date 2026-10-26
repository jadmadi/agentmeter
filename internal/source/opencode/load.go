// SPDX-License-Identifier: AGPL-3.0-only

package ocsource

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The SQL is copied verbatim from telemetry.py:load_rows. The message order is
// load-bearing: non-fork sessions first, then time_created, then id.
const (
	sessionsSQL = "SELECT id, title, slug, directory, parent_id, fork_session_id, version, time_created FROM session_v2"

	messagesSQL = "SELECT m.id, m.session_id, m.time_created, m.data " +
		"FROM session_message m LEFT JOIN session_v2 s ON s.id = m.session_id " +
		"WHERE m.type = 'assistant' " +
		"ORDER BY (s.fork_session_id IS NOT NULL) ASC, m.time_created, m.id"

	eventTypesSQL = "SELECT type, COUNT(*) AS n FROM session_message WHERE type NOT IN ('assistant', 'user') GROUP BY type"

	eventRowsSQL = "SELECT session_id, time_created, type, data FROM session_message " +
		"WHERE type IN ('compaction', 'model-switched') ORDER BY time_created"
)

// LoadOptions controls the read. The zero value reads every model and every
// date, with titles and basename project labels.
type LoadOptions struct {
	DBPath        string
	ModelFilters  []string
	Since         string
	Until         string
	IncludeTitles bool
	FullPaths     bool
}

// fingerprint is the dedup key: same timing and token shape.
type fingerprint struct {
	Created    int64
	Model      string
	FreshInput int64
	Output     int64
	Reasoning  int64
	CacheRead  int64
	CacheWrite int64
}

// Load opens the store, reads every assistant message in the Python order and
// reconstructs rows, the error/empty ledger and the event counters.
func Load(opts LoadOptions) (*Loaded, error) {
	db, err := OpenRO(opts.DBPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	out := NewLoaded()
	if err := loadSessions(db, out); err != nil {
		return nil, err
	}
	if err := loadMessages(db, out, opts); err != nil {
		return nil, err
	}
	if err := loadEvents(db, out); err != nil {
		return nil, err
	}
	return out, nil
}

func loadSessions(db *sql.DB, out *Loaded) error {
	rows, err := db.Query(sessionsSQL)
	if err != nil {
		return fmt.Errorf("read session_v2: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id                             sql.NullString
			title, slug, directory, parent sql.NullString
			fork, version                  sql.NullString
			created                        sql.NullInt64
		)
		if err := rows.Scan(&id, &title, &slug, &directory, &parent, &fork, &version, &created); err != nil {
			return fmt.Errorf("scan session_v2: %w", err)
		}
		meta := SessionMeta{
			Title:     nullPtr(title),
			Slug:      nullPtr(slug),
			Directory: nullPtr(directory),
			Parent:    nullPtr(parent),
			Fork:      nullPtr(fork),
			Version:   nullPtr(version),
		}
		if created.Valid {
			value := created.Int64
			meta.Created = &value
		}
		out.SessionsMeta[id.String] = meta
		out.SessionMetaOrder = append(out.SessionMetaOrder, id.String)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read session_v2: %w", err)
	}

	out.SessionsTotal = len(out.SessionsMeta)
	for _, id := range out.SessionMetaOrder {
		meta := out.SessionsMeta[id]
		if meta.Parent != nil && *meta.Parent != "" {
			out.SessionsSubagent++
		} else {
			out.SessionsRoot++
		}
		version := "unknown"
		if meta.Version != nil && *meta.Version != "" {
			version = *meta.Version
		}
		out.Events.Versions.Inc(version)
	}
	return nil
}

func loadMessages(db *sql.DB, out *Loaded, opts LoadOptions) error {
	filters := make([]string, 0, len(opts.ModelFilters))
	for _, term := range opts.ModelFilters {
		trimmed := strings.TrimSpace(term)
		if trimmed == "" {
			continue
		}
		filters = append(filters, strings.ToLower(trimmed))
	}

	seen := map[fingerprint]map[string]bool{}

	rows, err := db.Query(messagesSQL)
	if err != nil {
		return fmt.Errorf("read session_message: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id          sql.NullString
			sessionID   sql.NullString
			timeCreated sql.NullInt64
			data        sql.NullString
		)
		if err := rows.Scan(&id, &sessionID, &timeCreated, &data); err != nil {
			return fmt.Errorf("scan session_message: %w", err)
		}
		_ = timeCreated
		out.Scanned++
		if !data.Valid {
			continue
		}
		doc, err := decodePayload(data.String)
		if err != nil {
			// Python skips rows whose payload is not valid JSON.
			continue
		}
		p, err := parsePayload(doc, id.String)
		if err != nil {
			return err
		}

		if p.Created > out.MaxCreatedMs {
			out.MaxCreatedMs = p.Created
		}

		modelKey := p.ModelID
		if p.Provider != "" {
			modelKey = p.Provider + "/" + p.ModelID
		}
		if modelKey == "" {
			modelKey = "(unknown)"
		}
		if len(filters) > 0 {
			lowerKey := strings.ToLower(modelKey)
			lowerID := strings.ToLower(p.ModelID)
			matched := false
			for _, term := range filters {
				if strings.Contains(lowerKey, term) || strings.Contains(lowerID, term) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		if p.Created == 0 {
			out.NoCreated++
			continue
		}
		ts := time.UnixMilli(p.Created).Local()
		if opts.Since != "" || opts.Until != "" {
			day := ts.Format("2006-01-02")
			if opts.Since != "" && day < opts.Since {
				continue
			}
			if opts.Until != "" && day > opts.Until {
				continue
			}
		}
		out.Considered++

		// Generation ends when the stream ends. If OpenCode did not record
		// `streamed`, only trust `completed` for tool-free steps.
		var llmEnd int64
		if p.Streamed != 0 && p.Streamed > p.Created {
			llmEnd = p.Streamed
		} else if p.Completed != 0 && p.Completed > p.Created && len(p.ToolNames) == 0 {
			llmEnd = p.Completed
		}
		var llmSeconds *float64
		if llmEnd != 0 {
			value := float64(llmEnd-p.Created) / 1000.0
			llmSeconds = &value
		}
		var totalSeconds *float64
		if p.Completed > p.Created {
			value := float64(p.Completed-p.Created) / 1000.0
			totalSeconds = &value
		}
		toolSecondsValue, toolSpanValue := MergeIntervals(p.ToolWindows)
		var toolSeconds, toolSpan, toolGap *float64
		if len(p.ToolWindows) > 0 {
			seconds := toolSecondsValue
			span := toolSpanValue
			gap := span - seconds
			if gap < 0 {
				gap = 0
			}
			toolSeconds = &seconds
			toolSpan = &span
			toolGap = &gap
		}
		var overhead *float64
		if totalSeconds != nil && llmSeconds != nil {
			raw := *totalSeconds - *llmSeconds - toolSecondsValue
			if raw < 0 {
				out.Events.NegativeOverhead++
				zero := 0.0
				overhead = &zero
			} else {
				overhead = &raw
			}
		}

		// TTFT: first reasoning/tool block after the request starts. Text-only
		// replies have no timestamps in the store, so TTFT stays unknown.
		var ttft, decodeSeconds, decodeRate *float64
		if p.FirstBlockCreated != nil && p.Created != 0 && llmSeconds != nil {
			candidate := float64(*p.FirstBlockCreated-p.Created) / 1000.0
			if candidate >= 0.05 && candidate < *llmSeconds {
				value := candidate
				ttft = &value
				decode := *llmSeconds - candidate
				decodeSeconds = &decode
				if decode >= 0.1 {
					rate := float64(p.Generated) / decode
					decodeRate = &rate
				}
			}
		}

		key := fingerprint{
			Created:    p.Created,
			Model:      modelKey,
			FreshInput: p.FreshInput,
			Output:     p.Output,
			Reasoning:  p.Reasoning,
			CacheRead:  p.CacheRead,
			CacheWrite: p.CacheWrite,
		}
		signature, err := Signature(NormalizePayload(p.raw))
		if err != nil {
			return fmt.Errorf("message %s: dedup signature: %w", id.String, err)
		}
		signatures, ok := seen[key]
		if !ok {
			signatures = map[string]bool{}
			seen[key] = signatures
		}
		if signatures[signature] {
			out.Duplicates++
			out.DuplicateSessions[sessionID.String]++
			out.DuplicateCost += p.Cost
			out.DuplicateTokens += p.Generated
			continue
		}
		if len(signatures) > 0 {
			// Same timing/token fingerprint but different payload: not a copy.
			out.Collisions++
		}
		signatures[signature] = true

		meta := out.SessionsMeta[sessionID.String]
		directory := "(unknown)"
		if meta.Directory != nil && *meta.Directory != "" {
			directory = *meta.Directory
		}
		projectPath := directory
		projectLabel := directory
		if !opts.FullPaths {
			if base := pyBasename(directory); base != "" {
				projectLabel = base
			}
		}
		parent := meta.Parent != nil && *meta.Parent != ""
		short := p.ModelID
		if short == "" {
			short = p.Provider
		} else if index := strings.LastIndexByte(p.ModelID, '/'); index >= 0 {
			short = p.ModelID[index+1:]
		}
		provider := p.Provider
		if provider == "" {
			provider = "unknown"
		}
		variant := p.Variant
		if variant == "" {
			variant = "default"
		}
		entry := "root"
		if parent {
			entry = "subagent"
		}
		row := Row{
			ID:           id.String,
			SessionID:    sessionID.String,
			Model:        modelKey,
			Provider:     provider,
			Short:        short,
			Variant:      variant,
			Agent:        p.Agent,
			Finish:       p.Finish,
			Ts:           ts,
			CreatedMs:    p.Created,
			Out:          p.Output,
			Reas:         p.Reasoning,
			Gen:          p.Generated,
			Inp:          p.FreshInput,
			CacheRead:    p.CacheRead,
			CacheWrite:   p.CacheWrite,
			Cost:         p.Cost,
			Tool:         len(p.ToolNames) > 0,
			Tools:        p.ToolNames,
			ToolDetails:  p.ToolDetails,
			PrimaryTool:  "(final answer)",
			NTools:       len(p.ToolNames),
			NThinking:    p.NReasoning,
			NText:        p.NText,
			Thinking:     p.NReasoning > 0,
			DT:           llmSeconds,
			TotalDT:      totalSeconds,
			ToolDT:       toolSeconds,
			ToolSpanDT:   toolSpan,
			ToolGapDT:    toolGap,
			OverheadDT:   overhead,
			TTFT:         ttft,
			DecodeDT:     decodeSeconds,
			DecodeRate:   decodeRate,
			SessionTitle: sessionTitle(meta, opts.IncludeTitles),
			Project:      projectPath,
			ProjectLabel: projectLabel,
			Entry:        entry,
			Version:      meta.Version,
			Streamed:     p.Streamed != 0,
		}
		if len(p.ToolNames) > 0 {
			row.PrimaryTool = p.ToolNames[0]
		}

		// Assistant health follows the selected rows and the error ledger.
		out.Events.Finishes.Inc(p.Finish)
		if p.Finish == "error" {
			out.Events.Errors++
		}
		if p.Streamed == 0 {
			out.Events.NoStream++
		}
		for _, call := range p.ToolDetails {
			out.Events.ToolCalls.Inc(call.Name)
			if call.Error {
				out.Events.ToolErrors.Inc(call.Name)
			}
		}

		if p.Generated <= 0 || p.Finish == "error" {
			reason := "empty"
			switch {
			case p.Generated <= 0 && p.Finish == "error":
				reason = "both"
			case p.Finish == "error":
				reason = "error"
			}
			out.Ledger = append(out.Ledger, LedgerEntry{
				Row:    row,
				Reason: reason,
				Empty:  p.Generated <= 0,
				Error:  p.Finish == "error",
			})
			if p.Generated <= 0 {
				out.EmptySteps++
			}
			if p.Finish == "error" {
				out.ErrorSteps++
			}
			continue
		}
		out.Rows = append(out.Rows, row)
		out.Sessions[sessionID.String] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read session_message: %w", err)
	}
	return nil
}

// loadEvents reads the non-assistant event types and the compaction /
// model-switched records. These counters are store-wide, not filtered.
func loadEvents(db *sql.DB, out *Loaded) error {
	typeRows, err := db.Query(eventTypesSQL)
	if err != nil {
		return fmt.Errorf("read event types: %w", err)
	}
	for typeRows.Next() {
		var kind sql.NullString
		var count int64
		if err := typeRows.Scan(&kind, &count); err != nil {
			typeRows.Close()
			return fmt.Errorf("scan event types: %w", err)
		}
		out.Events.Types.Add(kind.String, count)
	}
	if err := typeRows.Err(); err != nil {
		typeRows.Close()
		return fmt.Errorf("read event types: %w", err)
	}
	typeRows.Close()

	events, err := db.Query(eventRowsSQL)
	if err != nil {
		return fmt.Errorf("read events: %w", err)
	}
	defer events.Close()
	for events.Next() {
		var (
			sessionID   sql.NullString
			timeCreated sql.NullInt64
			kind        sql.NullString
			data        sql.NullString
		)
		if err := events.Scan(&sessionID, &timeCreated, &kind, &data); err != nil {
			return fmt.Errorf("scan events: %w", err)
		}
		var eventTs *time.Time
		if timeCreated.Valid && timeCreated.Int64 != 0 {
			value := time.UnixMilli(timeCreated.Int64).Local()
			eventTs = &value
		}
		payload := map[string]any{}
		if data.Valid && data.String != "" {
			if parsed, err := decodePayload(data.String); err == nil {
				payload = parsed
			}
		}
		switch kind.String {
		case "compaction":
			out.Events.CompactionEvents = append(out.Events.CompactionEvents, CompactionEvent{
				Ts:           eventTs,
				SessionID:    sessionID.String,
				TokensBefore: compactionTokensBefore(payload),
			})
		case "model-switched":
			out.Events.ModelSwitchEvents = append(out.Events.ModelSwitchEvents, ModelSwitchEvent{
				Ts:        eventTs,
				SessionID: sessionID.String,
				Model:     firstTruthy(payload, "model", "to", "id"),
			})
		}
	}
	if err := events.Err(); err != nil {
		return fmt.Errorf("read events: %w", err)
	}

	out.Events.Compactions = out.Events.Types.Get("compaction")
	out.Events.ModelSwitches = out.Events.Types.Get("model-switched")
	out.Events.AgentSwitches = out.Events.Types.Get("agent-switched")
	out.Events.Idle = out.Events.Types.Get("idle")
	out.Events.Shell = out.Events.Types.Get("shell")
	return nil
}

// compactionTokensBefore extracts tokensBefore when scalar, or sums
// tokens.{input,cache.read,cache.write} when OpenCode v2 stores a token object.
func compactionTokensBefore(payload map[string]any) any {
	if val := firstTruthy(payload, "tokensBefore"); val != nil {
		return val
	}
	raw, ok := payload["tokens"]
	if !ok || isFalsy(raw) {
		return nil
	}
	if tokMap, isMap := raw.(map[string]any); isMap {
		inp, _ := getInt(tokMap, "input")
		cache, _ := getObject(tokMap, "cache")
		cr, _ := getInt(cache, "read")
		cw, _ := getInt(cache, "write")
		if total := inp + cr + cw; total > 0 {
			return total
		}
		return nil
	}
	return normalizeEventValue(raw)
}

// firstTruthy mirrors `payload.get(a) or payload.get(b) or ...`.
func firstTruthy(payload map[string]any, keys ...string) any {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok || isFalsy(value) {
			continue
		}
		return normalizeEventValue(value)
	}
	return nil
}

// normalizeEventValue keeps raw JSON scalars as their Python equivalents.
func normalizeEventValue(value any) any {
	switch v := value.(type) {
	case json.Number:
		if integer, err := v.Int64(); err == nil {
			return integer
		}
		if f, err := v.Float64(); err == nil {
			return f
		}
		return string(v)
	case []any, map[string]any:
		return value
	default:
		return value
	}
}

func nullPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

// sessionTitle mirrors `meta.get("title") or meta.get("slug")` gated by
// --no-titles: a non-empty title wins, otherwise the slug (which may be null
// or an empty string, exactly like Python's `or`).
func sessionTitle(meta SessionMeta, include bool) *string {
	if !include {
		return nil
	}
	if meta.Title != nil && *meta.Title != "" {
		return meta.Title
	}
	return meta.Slug
}

// pyBasename is os.path.basename(p.rstrip("/")).
func pyBasename(p string) string {
	trimmed := strings.TrimRight(p, "/")
	if index := strings.LastIndexByte(trimmed, '/'); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

// pyDirname is os.path.dirname.
func pyDirname(p string) string {
	index := strings.LastIndexByte(p, '/')
	if index < 0 {
		return ""
	}
	head := strings.TrimRight(p[:index], "/")
	if head == "" {
		return "/"
	}
	return head
}
