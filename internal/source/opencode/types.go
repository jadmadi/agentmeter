// SPDX-License-Identifier: AGPL-3.0-only

package ocsource

import "time"

// TimingReasons is the schema v3 timing-bucket order. "plausible" must stay at
// index 0: the compact step tuple stores this index, and the Python contract
// fixes the order.
var TimingReasons = []string{
	"plausible",
	"noTiming",
	"tooShort",
	"tooShortDuration",
	"tooLong",
	"slowRate",
	"fastRate",
}

// ToolCall is one tool block of an assistant message.
type ToolCall struct {
	Name      string
	Ran       int64
	Completed int64
	Runtime   *float64
	Error     bool
	// Allocated is filled by the report builder's proportional allocation.
	Allocated float64
}

// SessionMeta is one session_v2 row.
type SessionMeta struct {
	Title     *string
	Slug      *string
	Directory *string
	Parent    *string
	Fork      *string
	Version   *string
	Created   *int64
}

// Row is one token-bearing, non-error assistant step.
type Row struct {
	ID        string
	SessionID string

	Model    string
	Provider string
	Short    string
	Variant  string
	Agent    string
	Finish   string

	// Ts is the local-time instant of `time.created` (milliseconds in the
	// store, seconds in the compact step tuple).
	Ts           time.Time
	CreatedMs    int64
	Out          int64
	Reas         int64
	Gen          int64
	Inp          int64
	CacheRead    int64
	CacheWrite   int64
	Cost         float64
	Tool         bool
	Tools        []string
	ToolDetails  []ToolCall
	PrimaryTool  string
	NTools       int
	NThinking    int
	NText        int
	Thinking     bool
	DT           *float64
	TotalDT      *float64
	ToolDT       *float64
	ToolSpanDT   *float64
	ToolGapDT    *float64
	OverheadDT   *float64
	TTFT         *float64
	DecodeDT     *float64
	DecodeRate   *float64
	SessionTitle *string
	Project      string
	ProjectLabel string
	Entry        string
	Version      *string
	Streamed     bool

	// Derived by internal/report/opencode.
	TimingBucket string
	Ratio        *float64
}

// InpTotal is fresh input plus cache read and cache write: the canonical total
// input used everywhere in the schema.
func (r *Row) InpTotal() int64 { return r.Inp + r.CacheRead + r.CacheWrite }

// EpochSeconds is int(ts.timestamp()): the integer seconds stored in the
// compact step tuple and used by session spans.
func (r *Row) EpochSeconds() int64 { return r.CreatedMs / 1000 }

// LedgerEntry is one assistant message that was kept out of the speed rows
// because it errored or generated no tokens.
type LedgerEntry struct {
	Row
	Reason string // "error", "empty" or "both"
	Empty  bool
	Error  bool
}

// CompactionEvent is one `compaction` session_message.
type CompactionEvent struct {
	Ts           *time.Time
	SessionID    string
	TokensBefore any
}

// ModelSwitchEvent is one `model-switched` session_message.
type ModelSwitchEvent struct {
	Ts        *time.Time
	SessionID string
	Model     any
}

// Events holds store-wide event counters plus the assistant-health counters
// that follow the selected window.
type Events struct {
	Types         Counter
	Finishes      Counter
	ToolErrors    Counter
	ToolCalls     Counter
	Versions      Counter
	Compactions   int64
	ModelSwitches int64
	AgentSwitches int64
	Idle          int64
	Shell         int64
	Errors        int64
	NoStream      int64

	CompactionEvents  []CompactionEvent
	ModelSwitchEvents []ModelSwitchEvent

	NegativeOverhead int
}

// Loaded is the complete source view for one build.
type Loaded struct {
	Rows   []Row
	Ledger []LedgerEntry

	Sessions         map[string]bool
	SessionsTotal    int
	SessionsRoot     int
	SessionsSubagent int
	SessionsMeta     map[string]SessionMeta
	SessionMetaOrder []string

	Duplicates        int
	DuplicateSessions map[string]int
	DuplicateCost     float64
	DuplicateTokens   int64

	MaxCreatedMs int64
	Collisions   int
	EmptySteps   int
	ErrorSteps   int
	Scanned      int
	Considered   int
	NoCreated    int

	Events Events
}

// NewLoaded returns a Loaded with every map/slice initialised.
func NewLoaded() *Loaded {
	return &Loaded{
		Sessions:          map[string]bool{},
		SessionsMeta:      map[string]SessionMeta{},
		DuplicateSessions: map[string]int{},
		Events: Events{
			Types:      NewCounter(),
			Finishes:   NewCounter(),
			ToolErrors: NewCounter(),
			ToolCalls:  NewCounter(),
			Versions:   NewCounter(),
		},
	}
}
