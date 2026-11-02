// SPDX-License-Identifier: AGPL-3.0-only

package contract

import "fmt"

// indexSlot names a compact-step slot that must index a lookup array.
type indexSlot struct {
	slot   int
	lookup string
	// sessions indexes data["sessions"] rather than data["lookups"].
	sessions bool
}

var cmdIndexSlots = []indexSlot{
	{slot: 1, lookup: "models"},
	{slot: 2, lookup: "tools"},
	{slot: 9, lookup: "efforts"},
	{slot: 11, lookup: "projects"},
	{slot: 12, lookup: "entrypoints"},
	{slot: 13, sessions: true},
}

var museIndexSlots = []indexSlot{
	{slot: 1, lookup: "models"},
	{slot: 2, lookup: "tools"},
	{slot: 9, lookup: "efforts"},
	{slot: 11, lookup: "projects"},
	{slot: 12, lookup: "entrypoints"},
	{slot: 13, sessions: true},
}

var ocIndexSlots = []indexSlot{
	{slot: 1, lookup: "models"},
	{slot: 2, lookup: "tools"},
	{slot: 9, lookup: "agents"},
	{slot: 11, lookup: "projects"},
	{slot: 12, lookup: "entrypoints"},
	{slot: 13, sessions: true},
	{slot: 21, lookup: "variants"},
	{slot: 22, lookup: "finishes"},
}

// ValidateCmdIssues runs the common rules plus Command Code structural checks.
func ValidateCmdIssues(data map[string]any) Issues {
	return ValidateIssues(data,
		Rule{ID: RuleCmdLookupsContainer, Check: lookupsContainerCheck(RuleCmdLookupsContainer)},
		Rule{ID: RuleCmdStepTupleLength, Check: cmdStepChecks},
		Rule{ID: RuleCmdTimingReasons, Check: timingReasonsCheck(RuleCmdTimingReasons)},
	)
}

// ValidateCmd is ValidateCmdIssues rendered as "rule: message" strings.
func ValidateCmd(data map[string]any) []string {
	return ValidateCmdIssues(data).Strings()
}

// ValidateCmdWith adds caller-supplied rules (extension point for builders
// that know about new sections).
func ValidateCmdWith(data map[string]any, extra ...Rule) []string {
	return ValidateCmdWithIssues(data, extra...).Strings()
}

// ValidateCmdWithIssues is ValidateCmdIssues with extra rules appended.
func ValidateCmdWithIssues(data map[string]any, extra ...Rule) Issues {
	rules := append([]Rule{
		{ID: RuleCmdLookupsContainer, Check: lookupsContainerCheck(RuleCmdLookupsContainer)},
		{ID: RuleCmdStepTupleLength, Check: cmdStepChecks},
		{ID: RuleCmdTimingReasons, Check: timingReasonsCheck(RuleCmdTimingReasons)},
	}, extra...)
	return ValidateIssues(data, rules...)
}

// ValidateMuseIssues runs the common rules plus Muse structural checks.
func ValidateMuseIssues(data map[string]any) Issues {
	return ValidateIssues(data,
		Rule{ID: RuleMuseLookupsContainer, Check: lookupsContainerCheck(RuleMuseLookupsContainer)},
		Rule{ID: RuleMuseStepTupleLength, Check: museStepChecks},
		Rule{ID: RuleMuseTimingReasons, Check: timingReasonsCheck(RuleMuseTimingReasons)},
	)
}

// ValidateMuse is ValidateMuseIssues rendered as "rule: message" strings.
func ValidateMuse(data map[string]any) []string {
	return ValidateMuseIssues(data).Strings()
}

// ValidateMuseWith adds caller-supplied rules (extension point for builders
// that know about new sections).
func ValidateMuseWith(data map[string]any, extra ...Rule) []string {
	return ValidateMuseWithIssues(data, extra...).Strings()
}

// ValidateMuseWithIssues is ValidateMuseIssues with extra rules appended.
func ValidateMuseWithIssues(data map[string]any, extra ...Rule) Issues {
	rules := append([]Rule{
		{ID: RuleMuseLookupsContainer, Check: lookupsContainerCheck(RuleMuseLookupsContainer)},
		{ID: RuleMuseStepTupleLength, Check: museStepChecks},
		{ID: RuleMuseTimingReasons, Check: timingReasonsCheck(RuleMuseTimingReasons)},
	}, extra...)
	return ValidateIssues(data, rules...)
}

// ValidateOCIssues runs the common rules plus OpenCode structural checks.
func ValidateOCIssues(data map[string]any) Issues {
	return ValidateIssues(data,
		Rule{ID: RuleOCLookupsContainer, Check: lookupsContainerCheck(RuleOCLookupsContainer)},
		Rule{ID: RuleOCStepTupleLength, Check: ocStepChecks},
		Rule{ID: RuleOCTimingReasons, Check: timingReasonsCheck(RuleOCTimingReasons)},
	)
}

// ValidateOC is ValidateOCIssues rendered as "rule: message" strings.
func ValidateOC(data map[string]any) []string {
	return ValidateOCIssues(data).Strings()
}

// ValidateOCWith adds caller-supplied rules.
func ValidateOCWith(data map[string]any, extra ...Rule) []string {
	return ValidateOCWithIssues(data, extra...).Strings()
}

// ValidateOCWithIssues is ValidateOCIssues with extra rules appended.
func ValidateOCWithIssues(data map[string]any, extra ...Rule) Issues {
	rules := append([]Rule{
		{ID: RuleOCLookupsContainer, Check: lookupsContainerCheck(RuleOCLookupsContainer)},
		{ID: RuleOCStepTupleLength, Check: ocStepChecks},
		{ID: RuleOCTimingReasons, Check: timingReasonsCheck(RuleOCTimingReasons)},
	}, extra...)
	return ValidateIssues(data, rules...)
}

// cmdStepChecks validates Command Code compact step tuples (20 slots).
func cmdStepChecks(data map[string]any) []Issue {
	rows, ok := stepRows(data)
	if !ok {
		return []Issue{{Rule: RuleCmdStepsContainer, Message: "steps must be a JSON array"}}
	}
	var issues []Issue
	for i, raw := range rows {
		row, isArray := raw.([]any)
		if !isArray {
			issues = append(issues, Issue{
				Rule:    RuleCmdStepTupleLength,
				Message: fmt.Sprintf("steps[%d] must be a %d-slot array", i, CmdStepSlots),
			})
			continue
		}
		if len(row) != CmdStepSlots {
			issues = append(issues, Issue{
				Rule:    RuleCmdStepTupleLength,
				Message: fmt.Sprintf("steps[%d] has %d slots, want %d", i, len(row), CmdStepSlots),
			})
			continue
		}
		issues = append(issues, timestampIssue(i, row, RuleCmdStepTimestamp)...)
		issues = append(issues, timingIndexIssue(i, row, CmdStepTimingSlot, RuleCmdStepTimingIndex)...)
		issues = append(issues, lookupIndexIssues(data, i, row, cmdIndexSlots, RuleCmdStepLookupIndex)...)

		tools, isArray := row[CmdStepToolsSlot].([]any)
		if !isArray {
			issues = append(issues, Issue{
				Rule:    RuleCmdStepTools,
				Message: fmt.Sprintf("steps[%d][%d] must be an array of tool indexes", i, CmdStepToolsSlot),
			})
			continue
		}
		limit, hasLimit := lookupLength(data, "tools")
		for j, item := range tools {
			index, isInt := Integer(item)
			if !isInt || index < 0 || (hasLimit && index >= int64(limit)) {
				issues = append(issues, Issue{
					Rule:    RuleCmdStepTools,
					Message: fmt.Sprintf("steps[%d][%d][%d] tool index %s is out of range", i, CmdStepToolsSlot, j, PyRepr(item)),
				})
			}
		}
	}
	return issues
}

// museStepChecks validates Muse compact step tuples (20 slots, the
// Command Code layout).
func museStepChecks(data map[string]any) []Issue {
	rows, ok := stepRows(data)
	if !ok {
		return []Issue{{Rule: RuleMuseStepsContainer, Message: "steps must be a JSON array"}}
	}
	var issues []Issue
	for i, raw := range rows {
		row, isArray := raw.([]any)
		if !isArray {
			issues = append(issues, Issue{
				Rule:    RuleMuseStepTupleLength,
				Message: fmt.Sprintf("steps[%d] must be a %d-slot array", i, MuseStepSlots),
			})
			continue
		}
		if len(row) != MuseStepSlots {
			issues = append(issues, Issue{
				Rule:    RuleMuseStepTupleLength,
				Message: fmt.Sprintf("steps[%d] has %d slots, want %d", i, len(row), MuseStepSlots),
			})
			continue
		}
		issues = append(issues, timestampIssue(i, row, RuleMuseStepTimestamp)...)
		issues = append(issues, timingIndexIssue(i, row, MuseStepTimingSlot, RuleMuseStepTimingIndex)...)
		issues = append(issues, lookupIndexIssues(data, i, row, museIndexSlots, RuleMuseStepLookupIndex)...)

		tools, isArray := row[MuseStepToolsSlot].([]any)
		if !isArray {
			issues = append(issues, Issue{
				Rule:    RuleMuseStepTools,
				Message: fmt.Sprintf("steps[%d][%d] must be an array of tool indexes", i, MuseStepToolsSlot),
			})
			continue
		}
		limit, hasLimit := lookupLength(data, "tools")
		for j, item := range tools {
			index, isInt := Integer(item)
			if !isInt || index < 0 || (hasLimit && index >= int64(limit)) {
				issues = append(issues, Issue{
					Rule:    RuleMuseStepTools,
					Message: fmt.Sprintf("steps[%d][%d][%d] tool index %s is out of range", i, MuseStepToolsSlot, j, PyRepr(item)),
				})
			}
		}
	}
	return issues
}

// ocStepChecks validates OpenCode compact step tuples (32 slots).
func ocStepChecks(data map[string]any) []Issue {
	rows, ok := stepRows(data)
	if !ok {
		return []Issue{{Rule: RuleOCStepsContainer, Message: "steps must be a JSON array"}}
	}
	var issues []Issue
	for i, raw := range rows {
		row, isArray := raw.([]any)
		if !isArray {
			issues = append(issues, Issue{
				Rule:    RuleOCStepTupleLength,
				Message: fmt.Sprintf("steps[%d] must be a %d-slot array", i, OCStepSlots),
			})
			continue
		}
		if len(row) != OCStepSlots {
			issues = append(issues, Issue{
				Rule:    RuleOCStepTupleLength,
				Message: fmt.Sprintf("steps[%d] has %d slots, want %d", i, len(row), OCStepSlots),
			})
			continue
		}
		issues = append(issues, timestampIssue(i, row, RuleOCStepTimestamp)...)
		issues = append(issues, timingIndexIssue(i, row, OCStepTimingSlot, RuleOCStepTimingIndex)...)
		issues = append(issues, lookupIndexIssues(data, i, row, ocIndexSlots, RuleOCStepLookupIndex)...)

		rawCalls := row[OCStepToolCallsSlot]
		calls, isArray := rawCalls.([]any)
		if !isArray {
			issues = append(issues, Issue{
				Rule:    RuleOCStepToolCalls,
				Message: fmt.Sprintf("steps[%d][%d] must be an array of tool-call rows", i, OCStepToolCallsSlot),
			})
			continue
		}
		issues = append(issues, ocToolCallIssues(data, i, calls)...)
	}
	return issues
}

func ocToolCallIssues(data map[string]any, rowIndex int, calls []any) []Issue {
	var issues []Issue
	limit, hasLimit := lookupLength(data, "tools")
	for j, raw := range calls {
		call, isArray := raw.([]any)
		if !isArray || len(call) != OCToolCallSlots {
			issues = append(issues, Issue{
				Rule:    RuleOCStepToolCalls,
				Message: fmt.Sprintf("steps[%d][%d][%d] must be a %d-slot tool-call row", rowIndex, OCStepToolCallsSlot, j, OCToolCallSlots),
			})
			continue
		}
		index, isInt := Integer(call[0])
		if !isInt || index < 0 || (hasLimit && index >= int64(limit)) {
			issues = append(issues, Issue{
				Rule:    RuleOCStepToolCalls,
				Message: fmt.Sprintf("steps[%d][%d][%d] tool index %s is out of range", rowIndex, OCStepToolCallsSlot, j, PyRepr(call[0])),
			})
		}
		if runtime := numValue(call[1]); !runtime.valid() || !runtime.finite() {
			issues = append(issues, Issue{
				Rule:    RuleOCStepToolCalls,
				Message: fmt.Sprintf("steps[%d][%d][%d] runtime must be a number, got %s", rowIndex, OCStepToolCallsSlot, j, PyRepr(call[1])),
			})
		}
		if errorFlag, isInt := Integer(call[2]); !isInt || (errorFlag != 0 && errorFlag != 1) {
			issues = append(issues, Issue{
				Rule:    RuleOCStepToolCalls,
				Message: fmt.Sprintf("steps[%d][%d][%d] error flag must be 0 or 1, got %s", rowIndex, OCStepToolCallsSlot, j, PyRepr(call[2])),
			})
		}
		if allocated := numValue(call[3]); !allocated.nonNegative() {
			issues = append(issues, Issue{
				Rule:    RuleOCStepToolCalls,
				Message: fmt.Sprintf("steps[%d][%d][%d] allocated seconds must be non-negative, got %s", rowIndex, OCStepToolCallsSlot, j, PyRepr(call[3])),
			})
		}
	}
	return issues
}

func timestampIssue(rowIndex int, row []any, rule string) []Issue {
	value := numValue(row[0])
	if value.valid() && value.isPyInt && value.nonNegative() {
		return nil
	}
	return []Issue{{
		Rule:    rule,
		Message: fmt.Sprintf("steps[%d][0] must be a non-negative integer timestamp, got %s", rowIndex, PyRepr(row[0])),
	}}
}

func timingIndexIssue(rowIndex int, row []any, slot int, rule string) []Issue {
	index, isInt := Integer(row[slot])
	if isInt && index >= 0 && index < int64(len(TimingReasons)) {
		return nil
	}
	return []Issue{{
		Rule:    rule,
		Message: fmt.Sprintf("steps[%d][%d] timing index %s is outside the timing vocabulary", rowIndex, slot, PyRepr(row[slot])),
	}}
}

func lookupIndexIssues(data map[string]any, rowIndex int, row []any, slots []indexSlot, rule string) []Issue {
	var issues []Issue
	for _, spec := range slots {
		limit, hasLimit := 0, false
		label := spec.lookup
		if spec.sessions {
			rows, _ := ListSection(data["sessions"])
			limit, hasLimit = len(rows), true
			label = "sessions"
		} else {
			limit, hasLimit = lookupLength(data, spec.lookup)
		}
		index, isInt := Integer(row[spec.slot])
		if isInt && index >= 0 && (!hasLimit || index < int64(limit)) {
			continue
		}
		detail := ""
		if hasLimit {
			detail = fmt.Sprintf(" (%d entries)", limit)
		}
		issues = append(issues, Issue{
			Rule:    rule,
			Message: fmt.Sprintf("steps[%d][%d] index %s is out of range for lookups.%s%s", rowIndex, spec.slot, PyRepr(row[spec.slot]), label, detail),
		})
	}
	return issues
}

// lookupLength returns the length of lookups[name] when it is an array.
func lookupLength(data map[string]any, name string) (int, bool) {
	lookups, _ := ObjectSection(data["lookups"])
	list, ok := SliceValue(lookups[name])
	if !ok {
		return 0, false
	}
	return len(list), true
}

// lookupsContainerCheck fails closed when a non-object lookups section would
// make the Python validator raise.
func lookupsContainerCheck(rule string) func(data map[string]any) []Issue {
	return func(data map[string]any) []Issue {
		if _, ok := ObjectSection(data["lookups"]); !ok {
			return []Issue{{Rule: rule, Message: "lookups must be a JSON object"}}
		}
		return nil
	}
}

// timingReasonsCheck verifies lookups.timingReasons against the schema
// vocabulary. It is skipped when the lookup is absent.
func timingReasonsCheck(rule string) func(data map[string]any) []Issue {
	return func(data map[string]any) []Issue {
		lookups, _ := ObjectSection(data["lookups"])
		list, ok := SliceValue(lookups["timingReasons"])
		if !ok {
			return nil
		}
		if len(list) != len(TimingReasons) {
			return []Issue{{
				Rule:    rule,
				Message: fmt.Sprintf("lookups.timingReasons has %d entries, want %d", len(list), len(TimingReasons)),
			}}
		}
		for i, want := range TimingReasons {
			got, _ := StringValue(list[i])
			if got != want {
				return []Issue{{
					Rule:    rule,
					Message: fmt.Sprintf("lookups.timingReasons[%d] = %s, want %q", i, PyRepr(list[i]), want),
				}}
			}
		}
		return nil
	}
}

// stepRows returns the steps array. ok is false when steps is present but not
// an array; missing and falsy steps are treated as empty.
func stepRows(data map[string]any) ([]any, bool) {
	raw := data["steps"]
	if list, ok := raw.([]any); ok {
		return list, true
	}
	if falsy(raw) {
		return nil, true
	}
	return nil, false
}
