// SPDX-License-Identifier: AGPL-3.0-only

package contract_test

import (
	"strings"
	"testing"

	"github.com/jadmadi/agentmeter/internal/contract"
)

func timingReasonsAny() []any {
	out := make([]any, len(contract.TimingReasons))
	for i, reason := range contract.TimingReasons {
		out[i] = reason
	}
	return out
}

// cmdStepTuple is one valid Command Code compact step (20 slots). Slot layout
// mirrors command-code-telemetry/py/telemetry.py and web/index.html.
func cmdStepTuple() []any {
	return []any{
		int64(1700000000), // 0  timestamp
		int64(0),          // 1  model index
		int64(0),          // 2  tool index
		int64(100),        // 3  output tokens
		2.5,               // 4  duration
		int64(10),         // 5  input tokens
		int64(2),          // 6  cache read
		int64(0),          // 7  cache write
		0.001,             // 8  cost
		int64(0),          // 9  effort index
		int64(0),          // 10 thinking flag
		int64(0),          // 11 project index
		int64(0),          // 12 entrypoint index
		int64(0),          // 13 session index
		int64(1),          // 14 tool calls
		int64(0),          // 15 thinking blocks
		int64(1),          // 16 text blocks
		1.0,               // 17 ratio
		int64(0),          // 18 timing index (plausible)
		[]any{int64(0)},   // 19 tool indexes
	}
}

// ocStepTuple is one valid OpenCode compact step (32 slots).
func ocStepTuple() []any {
	return []any{
		int64(1700000000), // 0  timestamp
		int64(0),          // 1  model index
		int64(0),          // 2  tool index
		int64(100),        // 3  visible output
		2.5,               // 4  duration
		int64(10),         // 5  input tokens
		int64(2),          // 6  cache read
		int64(0),          // 7  cache write
		0.001,             // 8  cost
		int64(0),          // 9  agent index
		int64(0),          // 10 thinking flag
		int64(0),          // 11 project index
		int64(0),          // 12 entrypoint index
		int64(0),          // 13 session index
		int64(1),          // 14 tool calls
		int64(0),          // 15 thinking blocks
		int64(1),          // 16 text blocks
		int64(0),          // 17 reasoning tokens
		2.5,               // 18 total duration
		1.5,               // 19 tool duration
		int64(12),         // 20 input total
		int64(0),          // 21 variant index
		int64(0),          // 22 finish index
		0.1,               // 23 ttft
		2.0,               // 24 decode duration
		50.0,              // 25 decode rate
		0.2,               // 26 overhead
		1.5,               // 27 tool span
		0.1,               // 28 tool gap
		[]any{[]any{int64(0), 1.0, int64(0), 1.0}}, // 29 tool calls
		1.0,      // 30 ratio
		int64(0), // 31 timing index
	}
}

func cmdReport() map[string]any {
	data := report()
	data["steps"] = []any{cmdStepTuple()}
	data["lookups"] = map[string]any{
		"models":        []any{map[string]any{"id": "m", "short": "m", "provider": "p"}},
		"tools":         []any{"t"},
		"efforts":       []any{"unset"},
		"projects":      []any{"p"},
		"projectLabels": []any{"p"},
		"entrypoints":   []any{"interactive"},
		"timingReasons": timingReasonsAny(),
	}
	return data
}

func ocReport() map[string]any {
	data := report()
	data["steps"] = []any{ocStepTuple()}
	data["lookups"] = map[string]any{
		"models":        []any{map[string]any{"id": "m", "short": "m", "provider": "p"}},
		"tools":         []any{"t"},
		"agents":        []any{"build"},
		"variants":      []any{"default"},
		"projects":      []any{"p"},
		"projectLabels": []any{"p"},
		"entrypoints":   []any{"cli"},
		"finishes":      []any{"stop"},
		"timingReasons": timingReasonsAny(),
	}
	return data
}

func museReport() map[string]any {
	data := report()
	data["steps"] = []any{cmdStepTuple()}
	data["lookups"] = map[string]any{
		"models":        []any{map[string]any{"id": "m", "short": "m", "provider": "p"}},
		"tools":         []any{"t"},
		"efforts":       []any{"unset"},
		"projects":      []any{"p"},
		"projectLabels": []any{"p"},
		"entrypoints":   []any{"cli"},
		"timingReasons": timingReasonsAny(),
	}
	return data
}

func firstStep(d map[string]any) []any {
	return d["steps"].([]any)[0].([]any)
}

func lookups(d map[string]any) map[string]any {
	return d["lookups"].(map[string]any)
}

func TestValidateCmdAcceptsGoodReport(t *testing.T) {
	if issues := contract.ValidateCmdIssues(cmdReport()); len(issues) != 0 {
		t.Fatalf("good cmd report reported issues:\n%v", issues.Strings())
	}
	if got := contract.ValidateCmd(cmdReport()); len(got) != 0 {
		t.Fatalf("ValidateCmd returned %v", got)
	}
}

func TestValidateMuseAcceptsGoodReport(t *testing.T) {
	if issues := contract.ValidateMuseIssues(museReport()); len(issues) != 0 {
		t.Fatalf("good muse report reported issues:\n%v", issues.Strings())
	}
	if got := contract.ValidateMuse(museReport()); len(got) != 0 {
		t.Fatalf("ValidateMuse returned %v", got)
	}
}

func TestValidateOCAcceptsGoodReport(t *testing.T) {
	if issues := contract.ValidateOCIssues(ocReport()); len(issues) != 0 {
		t.Fatalf("good oc report reported issues:\n%v", issues.Strings())
	}
	if got := contract.ValidateOC(ocReport()); len(got) != 0 {
		t.Fatalf("ValidateOC returned %v", got)
	}
}

func TestValidateOCAcceptsEmptyToolCalls(t *testing.T) {
	data := ocReport()
	firstStep(data)[contract.OCStepToolCallsSlot] = []any{}
	if issues := contract.ValidateOCIssues(data); len(issues) != 0 {
		t.Fatalf("empty tool-call array reported issues:\n%v", issues.Strings())
	}
}

// cmdRuleMutations breaks each Command Code structural rule.
func cmdRuleMutations() []mutation {
	return []mutation{
		{"lookups_container", contract.RuleCmdLookupsContainer, func(d map[string]any) {
			d["lookups"] = "nope"
		}},
		{"steps_container", contract.RuleCmdStepsContainer, func(d map[string]any) {
			d["steps"] = map[string]any{"0": cmdStepTuple()}
		}},
		{"steps_tuple_length", contract.RuleCmdStepTupleLength, func(d map[string]any) {
			d["steps"] = []any{append(cmdStepTuple(), int64(1))}
		}},
		{"steps_tuple_too_short", contract.RuleCmdStepTupleLength, func(d map[string]any) {
			d["steps"] = []any{cmdStepTuple()[:19]}
		}},
		{"steps_timestamp", contract.RuleCmdStepTimestamp, func(d map[string]any) {
			firstStep(d)[0] = "yesterday"
		}},
		{"steps_timing_index", contract.RuleCmdStepTimingIndex, func(d map[string]any) {
			firstStep(d)[contract.CmdStepTimingSlot] = int64(99)
		}},
		{"steps_lookup_index", contract.RuleCmdStepLookupIndex, func(d map[string]any) {
			firstStep(d)[1] = int64(4) // only one model
		}},
		{"steps_tools_index", contract.RuleCmdStepTools, func(d map[string]any) {
			firstStep(d)[contract.CmdStepToolsSlot] = []any{int64(7)}
		}},
		{"steps_tools_not_array", contract.RuleCmdStepTools, func(d map[string]any) {
			firstStep(d)[contract.CmdStepToolsSlot] = "nope"
		}},
		{"lookups_timing_reasons", contract.RuleCmdTimingReasons, func(d map[string]any) {
			lookups(d)["timingReasons"] = []any{"plausible", "noTiming"}
		}},
	}
}

func TestValidateCmdStepMutations(t *testing.T) {
	for _, test := range cmdRuleMutations() {
		test := test
		t.Run(test.name, func(t *testing.T) {
			data := cmdReport()
			test.apply(data)
			issues := contract.ValidateCmdIssues(data)
			if !issues.Has(test.rule) {
				t.Fatalf("expected %s, got\n%v", test.rule, issues.Strings())
			}
		})
	}
}

// museRuleMutations breaks each Muse structural rule.
func museRuleMutations() []mutation {
	return []mutation{
		{"lookups_container", contract.RuleMuseLookupsContainer, func(d map[string]any) {
			d["lookups"] = "nope"
		}},
		{"steps_container", contract.RuleMuseStepsContainer, func(d map[string]any) {
			d["steps"] = map[string]any{"0": cmdStepTuple()}
		}},
		{"steps_tuple_length", contract.RuleMuseStepTupleLength, func(d map[string]any) {
			d["steps"] = []any{append(cmdStepTuple(), int64(1))}
		}},
		{"steps_tuple_too_short", contract.RuleMuseStepTupleLength, func(d map[string]any) {
			d["steps"] = []any{cmdStepTuple()[:19]}
		}},
		{"steps_timestamp", contract.RuleMuseStepTimestamp, func(d map[string]any) {
			firstStep(d)[0] = "yesterday"
		}},
		{"steps_timing_index", contract.RuleMuseStepTimingIndex, func(d map[string]any) {
			firstStep(d)[contract.MuseStepTimingSlot] = int64(99)
		}},
		{"steps_lookup_index", contract.RuleMuseStepLookupIndex, func(d map[string]any) {
			firstStep(d)[1] = int64(4) // only one model
		}},
		{"steps_tools_index", contract.RuleMuseStepTools, func(d map[string]any) {
			firstStep(d)[contract.MuseStepToolsSlot] = []any{int64(7)}
		}},
		{"steps_tools_not_array", contract.RuleMuseStepTools, func(d map[string]any) {
			firstStep(d)[contract.MuseStepToolsSlot] = "nope"
		}},
		{"lookups_timing_reasons", contract.RuleMuseTimingReasons, func(d map[string]any) {
			lookups(d)["timingReasons"] = []any{"plausible", "noTiming"}
		}},
	}
}

func TestValidateMuseStepMutations(t *testing.T) {
	for _, test := range museRuleMutations() {
		test := test
		t.Run(test.name, func(t *testing.T) {
			data := museReport()
			test.apply(data)
			issues := contract.ValidateMuseIssues(data)
			if !issues.Has(test.rule) {
				t.Fatalf("expected %s, got\n%v", test.rule, issues.Strings())
			}
		})
	}
}

// ocRuleMutations breaks each OpenCode structural rule.
func ocRuleMutations() []mutation {
	return []mutation{
		{"lookups_container", contract.RuleOCLookupsContainer, func(d map[string]any) {
			d["lookups"] = []any{int64(1)}
		}},
		{"steps_container", contract.RuleOCStepsContainer, func(d map[string]any) {
			d["steps"] = map[string]any{"0": ocStepTuple()}
		}},
		{"steps_tuple_length", contract.RuleOCStepTupleLength, func(d map[string]any) {
			d["steps"] = []any{ocStepTuple()[:31]}
		}},
		{"steps_timestamp", contract.RuleOCStepTimestamp, func(d map[string]any) {
			firstStep(d)[0] = -5.0
		}},
		{"steps_timing_index", contract.RuleOCStepTimingIndex, func(d map[string]any) {
			firstStep(d)[contract.OCStepTimingSlot] = int64(-1)
		}},
		{"steps_lookup_index", contract.RuleOCStepLookupIndex, func(d map[string]any) {
			firstStep(d)[21] = int64(3) // only one variant
		}},
		{"steps_tool_calls_width", contract.RuleOCStepToolCalls, func(d map[string]any) {
			firstStep(d)[contract.OCStepToolCallsSlot] = []any{[]any{int64(0), 1.0, int64(0)}}
		}},
		{"steps_tool_calls_kind", contract.RuleOCStepToolCalls, func(d map[string]any) {
			firstStep(d)[contract.OCStepToolCallsSlot] = "nope"
		}},
		{"steps_tool_calls_index", contract.RuleOCStepToolCalls, func(d map[string]any) {
			firstStep(d)[contract.OCStepToolCallsSlot] = []any{[]any{int64(5), 1.0, int64(0), 1.0}}
		}},
		{"lookups_timing_reasons", contract.RuleOCTimingReasons, func(d map[string]any) {
			lookups(d)["timingReasons"] = []any{"plausible", "noTiming", "tooShort", "tooShortDuration", "tooLong", "slowRate", "bogus"}
		}},
	}
}

func TestValidateOCStepMutations(t *testing.T) {
	for _, test := range ocRuleMutations() {
		test := test
		t.Run(test.name, func(t *testing.T) {
			data := ocReport()
			test.apply(data)
			issues := contract.ValidateOCIssues(data)
			if !issues.Has(test.rule) {
				t.Fatalf("expected %s, got\n%v", test.rule, issues.Strings())
			}
		})
	}
}

// Without a lookups section only non-negativity is checkable; the report must
// still validate rather than panic.
func TestValidateCmdWithoutLookups(t *testing.T) {
	data := cmdReport()
	delete(data, "lookups")
	if issues := contract.ValidateCmdIssues(data); len(issues) != 0 {
		t.Fatalf("report without lookups reported issues:\n%v", issues.Strings())
	}
}

// Steps absent is valid for partial fixtures (the Python validator does not
// look at steps at all); a non-array steps section is not.
func TestValidateCmdStepsOptional(t *testing.T) {
	data := cmdReport()
	delete(data, "steps")
	if issues := contract.ValidateCmdIssues(data); len(issues) != 0 {
		t.Fatalf("report without steps reported issues:\n%v", issues.Strings())
	}

	data = cmdReport()
	data["steps"] = []any{"not-a-tuple"}
	issues := contract.ValidateCmdIssues(data)
	if !issues.Has(contract.RuleCmdStepTupleLength) {
		t.Fatalf("non-array step reported %v", issues.Strings())
	}
}

func TestValidateWithExtraRules(t *testing.T) {
	extra := contract.Rule{
		ID: "build.extra",
		Check: func(map[string]any) []contract.Issue {
			return []contract.Issue{{Rule: "build.extra", Message: "custom check failed"}}
		},
	}
	got := contract.ValidateCmdWith(cmdReport(), extra)
	if len(got) != 1 || !strings.Contains(got[0], "build.extra") {
		t.Fatalf("extra rule not reported: %v", got)
	}
	if issues := contract.ValidateCmdWithIssues(cmdReport(), extra); !issues.Has("build.extra") {
		t.Fatalf("ValidateCmdWithIssues lost the extra rule: %v", issues.Strings())
	}

	// A rule that returns an issue without an id falls back to Rule.ID.
	fallback := contract.Rule{
		ID: "build.fallback",
		Check: func(map[string]any) []contract.Issue {
			return []contract.Issue{{Message: "no id"}}
		},
	}
	if issues := contract.ValidateOCWithIssues(ocReport(), fallback); !issues.Has("build.fallback") {
		t.Fatalf("Rule.ID fallback missing: %v", issues.Strings())
	}

	// Extra rules run after the common rules.
	mixed := contract.ValidateCmdWith(cmdReport(), extra)
	if !strings.Contains(mixed[0], "build.extra") {
		t.Fatalf("expected only the extra rule, got %v", mixed)
	}

	clean := contract.Rule{ID: "build.clean", Check: func(map[string]any) []contract.Issue { return nil }}
	if got := contract.ValidateCmdWith(cmdReport(), clean); len(got) != 0 {
		t.Fatalf("clean extra rule reported %v", got)
	}
}
