// SPDX-License-Identifier: AGPL-3.0-only

// Package contract implements the schema v3 report contract shared by the
// Command Code, Muse and OpenCode builders.
//
// Reports are plain map[string]any documents, exactly like the Python
// builders produce; this package deliberately does not model them as typed
// structs. It provides:
//
//   - the schema constants (SchemaVersion, TimingReasons) and the compact
//     step-tuple layout constants,
//   - tolerant nested accessors that behave like Python dict.get chains,
//   - ValidateCommon, a line-by-line port of telemetry_common.validate_common
//     with stable rule ids, and
//   - ValidateCmd / ValidateMuse / ValidateOC, which run the common rules
//     plus per-tool structural checks (step tuple width, timing index range,
//     lookup index ranges, nested tool-call rows) and accept extra rules.
//
// Validation is fail-closed: anything the Python validator would raise on is
// reported as an issue instead, so a build cannot write a malformed report
// silently.
package contract

const (
	// SchemaVersion is the report schema emitted by both builders.
	SchemaVersion = 3

	// CmdStepSlots is the width of a Command Code step tuple.
	CmdStepSlots = 20
	// MuseStepSlots is the width of a Muse step tuple.
	MuseStepSlots = 20
	// DshStepSlots is the width of a DSH step tuple.
	DshStepSlots = 20
	// OCStepSlots is the width of an OpenCode step tuple.
	OCStepSlots = 32

	// CmdStepTimingSlot is the timing-reason index inside a Command Code step.
	CmdStepTimingSlot = 18
	// CmdStepToolsSlot is the tool-index array inside a Command Code step.
	CmdStepToolsSlot = 19

	// MuseStepTimingSlot is the timing-reason index inside a Muse step.
	MuseStepTimingSlot = 18
	// MuseStepToolsSlot is the tool-index array inside a Muse step.
	MuseStepToolsSlot = 19

	// DshStepTimingSlot is the timing-reason index inside a DSH step.
	DshStepTimingSlot = 18
	// DshStepToolsSlot is the tool-index array inside a DSH step.
	DshStepToolsSlot = 19

	// OCStepToolCallsSlot is the tool-call array inside an OpenCode step.
	OCStepToolCallsSlot = 29
	// OCStepTimingSlot is the timing-reason index inside an OpenCode step.
	OCStepTimingSlot = 31

	// OCToolCallSlots is the width of one OpenCode tool-call row:
	// [toolIndex, runtimeSeconds, errorFlag, allocatedSeconds].
	OCToolCallSlots = 4
)

// TimingReasons is the fixed timing vocabulary shared with the renderers.
// Order is part of the schema: compact step tuples store indexes into this
// slice and report lookups expose it verbatim. "plausible" must stay at index
// 0.
//
// Do not mutate this slice; use TimingReasonsCopy when a caller needs its own
// copy.
var TimingReasons = []string{
	"plausible",
	"noTiming",
	"tooShort",
	"tooShortDuration",
	"tooLong",
	"slowRate",
	"fastRate",
}

// TimingReasonsCopy returns a copy of TimingReasons.
func TimingReasonsCopy() []string {
	out := make([]string, len(TimingReasons))
	copy(out, TimingReasons)
	return out
}

// TimingReasonIndex returns the index of name, or -1.
func TimingReasonIndex(name string) int {
	for i, reason := range TimingReasons {
		if reason == name {
			return i
		}
	}
	return -1
}

// IsTimingReason reports whether name is in TimingReasons.
func IsTimingReason(name string) bool {
	return TimingReasonIndex(name) >= 0
}

// Issue is one violated rule.
type Issue struct {
	// Rule is the stable rule id, e.g. "common.totals.input_sum".
	Rule string
	// Message is the human-readable detail, matching the Python validator
	// where practical.
	Message string
}

// String renders the issue as "rule: message".
func (i Issue) String() string {
	if i.Message == "" {
		return i.Rule
	}
	return i.Rule + ": " + i.Message
}

// Error lets an Issue satisfy the error interface.
func (i Issue) Error() string { return i.String() }

// Issues is a list of violated rules.
type Issues []Issue

// Strings renders every issue as "rule: message".
func (s Issues) Strings() []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	for i, issue := range s {
		out[i] = issue.String()
	}
	return out
}

// Rules returns the rule ids in violation order.
func (s Issues) Rules() []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	for i, issue := range s {
		out[i] = issue.Rule
	}
	return out
}

// Has reports whether rule is among the violated rules.
func (s Issues) Has(rule string) bool {
	for _, issue := range s {
		if issue.Rule == rule {
			return true
		}
	}
	return false
}

// Rule is an additional check composed into a validator. It is the extension
// point for tool-specific or experimental invariants.
type Rule struct {
	// ID is the rule id used when Check returns an issue without one.
	ID string
	// Check inspects data and returns the issues it found.
	Check func(data map[string]any) []Issue
}

// ValidateIssues runs the common rules in Python order, then every extra rule
// in the order given.
func ValidateIssues(data map[string]any, extra ...Rule) Issues {
	checker := &checker{}
	validateCommon(checker, data)
	for _, rule := range extra {
		if rule.Check == nil {
			continue
		}
		for _, issue := range rule.Check(data) {
			if issue.Rule == "" {
				issue.Rule = rule.ID
			}
			checker.append(issue)
		}
	}
	return checker.issues
}

// Validate runs ValidateIssues and renders "rule: message" strings.
func Validate(data map[string]any, extra ...Rule) []string {
	return ValidateIssues(data, extra...).Strings()
}

// ValidateCommonIssues ports telemetry_common.validate_common.
func ValidateCommonIssues(data map[string]any) Issues {
	return ValidateIssues(data)
}

// ValidateCommon ports telemetry_common.validate_common and returns
// "rule: message" strings.
func ValidateCommon(data map[string]any) []string {
	return ValidateCommonIssues(data).Strings()
}

// checker accumulates issues in detection order.
type checker struct {
	issues Issues
}

func (c *checker) append(issue Issue) {
	if issue.Rule == "" {
		issue.Rule = "common.unknown"
	}
	c.issues = append(c.issues, issue)
}

func (c *checker) add(rule, format string, args ...any) {
	c.append(Issue{Rule: rule, Message: sprintf(format, args...)})
}

// check records an issue when cond is false.
func (c *checker) check(rule string, cond bool, format string, args ...any) {
	if cond {
		return
	}
	c.add(rule, format, args...)
}
