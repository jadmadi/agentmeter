// SPDX-License-Identifier: AGPL-3.0-only

// Package dsh loads DeepSeek Harness (dsh) session event logs.
//
// It owns session*.jsonl(.zstd) discovery (selecting the highest schema
// version per session directory), ordered step/start + assistant/message
// pairing, tool/call + tool/result linkage, usage normalization, model/date
// filters, the skip ledger, and the source watermark. Session titles come from
// in-session session/title events and the optional projection cache under
// storages/session_projcache; prompt and response text and tool arguments are
// never read or emitted.
//
// DSH records no cost data: every row carries zero cost and spend views read
// zero, never an estimate.
package dsh

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

// Skip-ledger reasons. The ledger records every model completion that did not
// become a step row, and why.
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
	// Root is the dsh root (~/.dsh or a sessions directory); a leading "~"
	// is expanded. A missing root is an empty source.
	Root string
	// ModelFilters are case-insensitive substrings of the model id. Empty
	// means every model.
	ModelFilters []string
	// Since and Until are inclusive local dates (YYYY-MM-DD). Empty means
	// unbounded. They are compared as ISO strings.
	Since string
	Until string
	// IncludeTitles controls whether session titles are attached to sessions.
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
	Name  string
	Error bool
}

// Row is one model call. Out is generated tokens (visible + reasoning); Reas
// is the thinking-token split; Inp is fresh/uncached input tokens. Cost is
// always zero: DSH records no cost data.
type Row struct {
	ID            string
	Model         string
	Provider      string
	Effort        string
	TS            time.Time
	Out           int64
	Reas          int64
	Inp           int64
	CacheRead     int64
	CacheWrite    int64
	Cost          float64
	Tool          bool
	Tools         []string
	ToolDetails   []ToolDetail
	PrimaryTool   *string
	NTools        int64
	ToolErrors    int64
	Web           bool
	Thinking      bool
	Subagent      bool
	DT            *float64
	TPS           *float64
	SessionID     string
	SessionTitle  *string
	ProjectPath   string
	ProjectLabel  *string
	Branch        *string
	Entrypoint    string
	EntrypointRaw *string
	Timing        string
	Ratio         *float64
}

// Visible returns visible output tokens: generated minus reasoning.
func (row *Row) Visible() int64 {
	return row.Out - row.Reas
}

// LedgerEntry is one skipped model completion or unpaired step/start.
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

// CompactionEvent is one context-compaction event with a known token count.
type CompactionEvent struct {
	TS           *time.Time
	SessionID    *string
	TokensBefore int64
}

// Events is the full-history event ledger.
type Events struct {
	Omissions        int64
	Incomplete       int64
	UnmatchedCommits int64
	Subagents        map[string]struct{}
	Branches         map[string]struct{}
	Compactions      int64
	CompactionTokens int64
	CompactionEvents []CompactionEvent
	ModelChanges     int64
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
	Outcomes          map[string]string
}
