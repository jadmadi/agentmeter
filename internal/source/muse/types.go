// SPDX-License-Identifier: AGPL-3.0-only

// Package muse loads Muse session event logs.
//
// It owns session.jsonl discovery, ordered model-call records, tool-call
// linkage by response id, usage normalization, model/date filters, the
// skip ledger and the source watermark. Session titles come from the
// optional session index (session-index.db next to the sessions root);
// the index's prompt columns are never selected. Workspace roots and
// branches prefer in-session workspace_branch events and fall back to the
// index. Session outcomes are the main log's session_end exit reason.
//
// Source layout: <root>/<date>/<session-id>/session.jsonl with one JSON
// record per line, plus <session>/subagent/<id>/session.jsonl child logs in
// the same format. Child rows are attributed to the parent session and
// flagged Subagent; a child log never creates its own session. Only record
// metadata is read: timestamps, model ids, token usage, durations, tool
// names and session labels. Message text, tool arguments and result bodies
// are never read or emitted.
//
// Muse records no cost data: every row carries zero cost and spend views
// read zero, never an estimate.
package muse

import "time"

// TimingReasons is the schema v3 timing classification, in contract order.
// "plausible" is index 0 and the only bucket treated as a timed step.
var TimingReasons = []string{
	"plausible",
	"noTiming",
	"tooShort",
	"tooShortDuration",
	"tooLong",
	"slowRate",
	"fastRate",
}

// Skip-ledger reasons. The ledger records every model completion that did
// not become a step row, and why.
const (
	ReasonError       = "error"
	ReasonEmpty       = "empty"
	ReasonBoth        = "both"
	ReasonDuplicate   = "duplicate"
	ReasonNoTimestamp = "noTimestamp"
	ReasonIncomplete  = "incomplete"
)

// Filters mirrors the builder's CLI filters.
type Filters struct {
	// Root is the sessions root; a leading "~" is expanded. A missing root
	// is an empty source.
	Root string
	// ModelFilters are case-insensitive substrings of the model id. Empty
	// means every model.
	ModelFilters []string
	// Since and Until are inclusive local dates (YYYY-MM-DD). Empty means
	// unbounded. They are compared as ISO strings.
	Since string
	Until string
	// IncludeTitles controls whether index titles are attached to sessions.
	IncludeTitles bool
}

// Count mirrors the loader counters; the report emits them verbatim as
// meta.counts.
type Count struct {
	Files         int64
	Scanned       int64
	Considered    int64
	Rows          int64
	Duplicates    int64
	Errors        int64
	Empty         int64
	Incomplete    int64
	NoTimestamp   int64
	FilteredModel int64
	FilteredDate  int64
	Subagents     int64
	Omissions     int64
}

// ToolDetail is one tool call attributed to a step.
type ToolDetail struct {
	Name string
}

// Row is one model call. Out is generated tokens (visible + reasoning);
// Reas is the thinking-token split. Cost is always zero: Muse records no
// cost data.
type Row struct {
	ID           string
	Model        string
	Effort       string // always "unset": Muse has no effort signal
	TS           time.Time
	Out          int64
	Reas         int64
	Inp          int64
	CacheRead    int64
	CacheWrite   int64
	Cost         float64
	Tool         bool
	Tools        []string
	ToolDetails  []ToolDetail
	PrimaryTool  *string
	NTools       int64
	Web          bool
	Thinking     bool
	Subagent     bool
	DT           *float64 // seconds from duration_ms; nil when unusable
	TPS          *float64
	SessionID    string
	SessionTitle *string
	ProjectPath  string
	ProjectLabel *string // assigned by the report builder
	Branch       *string
	Entrypoint   string // always "muse"
	// EntrypointRaw stays nil: there is no per-record entrypoint signal, so
	// coverage.entrypoints is always false.
	EntrypointRaw *string
	Timing        string // one of TimingReasons
	Ratio         *float64
}

// Visible returns visible output tokens: generated minus reasoning.
func (row *Row) Visible() int64 {
	return row.Out - row.Reas
}

// LedgerEntry is one skipped model completion.
type LedgerEntry struct {
	ID               string
	Reason           string
	SessionID        string
	ProjectPath      string
	Model            string
	TS               *time.Time
	OutputTokens     int64
	InputTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
}

// SessionMeta is the session index row: labels only, never prompts.
type SessionMeta struct {
	Title     string
	Workspace string
	Branch    string
}

// CompactionEvent is reserved: no Muse context-compaction signal is mapped
// yet, so the list stays empty. It preserves the report shape for future
// summarizer/compaction events.
type CompactionEvent struct {
	TS           *time.Time
	SessionID    *string
	TokensBefore int64
}

// Events is the full-history event ledger. These are not filtered by the
// model/date filters.
type Events struct {
	Omissions        int64
	Incomplete       int64
	UnmatchedCommits int64
	Subagents        map[string]struct{}
	Branches         map[string]struct{}
	// Reserved zeros: Muse has no hook, tool-window, compaction, snapshot
	// or model-change signals mapped yet.
	Compactions      int64
	CompactionTokens int64
	CompactionEvents []CompactionEvent
	FileSnapshots    int64
}

// Source is the loaded session corpus plus everything the ledger needs to
// describe what was skipped.
type Source struct {
	Files             []string
	Sessions          map[string]struct{}
	Rows              []*Row
	Ledger            []*LedgerEntry
	Counts            Count
	Timing            map[string]int64
	Events            Events
	DuplicateSessions map[string]int64
	DuplicateTokens   int64
	DuplicateCost     float64
	MaxTS             *time.Time
	// Outcomes maps a parent session id to its main log's session_end
	// exit reason. Sessions without a session_end record carry no entry
	// and report "unknown".
	Outcomes map[string]string
	// labels holds the latest in-session workspace_branch observation
	// per parent session, across main and child logs.
	labels map[string]sessionLabel
}

// sessionLabel is one workspace_branch observation: the VCS reference and
// workspace root with the event timestamp that orders them.
type sessionLabel struct {
	Branch    string
	Workspace string
	TS        *time.Time
}
