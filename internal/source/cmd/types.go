// SPDX-License-Identifier: AGPL-3.0-only

// Package cmd loads Command Code session transcripts.
//
// It owns JSONL discovery, sidecar metadata, ordered message entries, parent
// timing, usage normalization, fork dedup, model/date filters, hook events,
// tool windows, compactions, model changes, branch state, the skip ledger and
// the source watermark. Every behavior in this package mirrors
// command-code-telemetry/py/telemetry.py's load_rows() exactly, including the
// order in which filters and counters are applied.
package cmd

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

// Skip-ledger reasons. The ledger records every assistant message that did not
// become a step row, and why.
const (
	ReasonNoOutput    = "noOutput"
	ReasonDuplicate   = "duplicate"
	ReasonNoTimestamp = "noTimestamp"
)

// Filters mirrors the builder's CLI filters.
type Filters struct {
	// Root is the session root; a leading "~" is expanded. A missing root is
	// an empty source, exactly like the Python glob.
	Root string
	// ModelFilters are case-insensitive substrings of the model id. Empty
	// means every model.
	ModelFilters []string
	// Since and Until are inclusive local dates (YYYY-MM-DD). Empty means
	// unbounded. They are compared as ISO strings, like the Python builder.
	Since string
	Until string
	// IncludeTitles copies sidecar session titles into rows.
	IncludeTitles bool
}

// Count exists because Python initializes a fixed counter dict; the report
// emits it verbatim as meta.counts.
type Count struct {
	Files         int64
	Scanned       int64
	Assistant     int64
	Considered    int64
	Rows          int64
	Duplicates    int64
	NoOutput      int64
	NoTimestamp   int64
	FilteredModel int64
	FilteredDate  int64
}

// ToolDetail is one tool call attributed to a step.
type ToolDetail struct {
	Name  string
	Error bool
}

// Row is one assistant step. Optional values are pointers so a null in the
// Python dict stays a null in the report.
type Row struct {
	ID           string
	Model        string
	Effort       string // normalized: str(effort or "unset")
	TS           time.Time
	Out          int64
	Inp          int64
	CacheRead    int64
	CacheWrite   int64
	Cost         float64
	Tool         bool
	Tools        []string
	ToolDetails  []ToolDetail
	PrimaryTool  *string
	NTools       int64
	NThinking    int64
	NText        int64
	Web          bool
	Thinking     bool
	DT           *float64 // seconds; nil when the parent timing is unusable
	TPS          *float64
	SessionID    string
	SessionTitle *string
	ProjectPath  string
	ProjectLabel *string // assigned by the report builder
	Branch       *string
	Entrypoint   string // normalized: entrypoint or "interactive"
	// EntrypointRaw keeps the sidecar value before the "interactive" fallback;
	// coverage.entrypoints is a truthiness check on the raw value.
	EntrypointRaw *string
	Timing        string // one of TimingReasons
	Ratio         *float64
}

// LedgerEntry is one skipped assistant message.
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

// Hook is one hook event record (records with no "type" but an eventName).
type Hook struct {
	EventName *string
	HookName  *string
	HookType  *string
	ToolName  *string
	Duration  *float64
	ExitCode  *float64
	Blocked   bool
	TS        *time.Time
}

// ToolWindow is a PreToolUse -> PostToolUse window, in seconds.
type ToolWindow struct {
	Tool    *string
	Seconds float64
}

// CompactionEvent is one compaction record.
type CompactionEvent struct {
	TS           *time.Time
	SessionID    *string
	TokensBefore int64
}

// ModelChangeEvent is one model_change record.
type ModelChangeEvent struct {
	TS        *time.Time
	SessionID *string
	Model     string
}

// Events is the full-history event ledger. These are not filtered by the
// model/date filters, matching the Python builder.
type Events struct {
	Compactions       int64
	CompactionTokens  int64
	CompactionEvents  []CompactionEvent
	ModelChanges      int64
	ModelChangeEvents []ModelChangeEvent
	FileSnapshots     int64
	Hooks             []Hook
	ToolWindows       []ToolWindow
	Branches          map[string]struct{}
}

// Source is the loaded transcript corpus plus everything the ledger needs to
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
}
