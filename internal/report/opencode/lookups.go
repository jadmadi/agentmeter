// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"sort"

	ocsource "github.com/jadmadi/agentmeter/internal/source/opencode"
)

// lookups holds the sorted index vocabularies used by the compact step tuples
// and by the report's lookup table.
type lookups struct {
	b *builder

	toolNames       []string
	agentNames      []string
	variantNames    []string
	projectNames    []string
	projectLabels   []string
	entrypointNames []string
	sessionIDs      []string
	finishNames     []string

	modelIndex      map[string]int
	toolIndex       map[string]int
	agentIndex      map[string]int
	variantIndex    map[string]int
	projectIndex    map[string]int
	entrypointIndex map[string]int
	sessionIndex    map[string]int
	finishIndex     map[string]int
	timingIndex     map[string]int
}

// newLookups builds the index vocabularies in Python's order.
func newLookups(b *builder, rows []*ocsource.Row) *lookups {
	l := &lookups{b: b}

	toolSet := map[string]bool{}
	for _, row := range rows {
		if len(row.ToolDetails) > 0 {
			for _, call := range row.ToolDetails {
				toolSet[call.Name] = true
			}
		} else {
			toolSet["(final answer)"] = true
		}
	}
	l.toolNames = sortedSet(toolSet)
	l.agentNames = sortedSetOf(rows, func(row *ocsource.Row) string { return row.Agent })
	l.variantNames = sortedSetOf(rows, func(row *ocsource.Row) string { return row.Variant })
	l.projectNames = sortedSetOf(rows, func(row *ocsource.Row) string { return row.Project })
	l.projectLabels = make([]string, len(l.projectNames))
	for i, path := range l.projectNames {
		l.projectLabels[i] = b.projectLabels[path]
	}
	l.entrypointNames = sortedSetOf(rows, func(row *ocsource.Row) string { return row.Entry })
	l.sessionIDs = sortedSetOf(rows, func(row *ocsource.Row) string { return row.SessionID })
	l.finishNames = sortedSetOf(rows, func(row *ocsource.Row) string { return row.Finish })

	l.modelIndex = indexOf(b.models)
	l.toolIndex = indexOf(l.toolNames)
	l.agentIndex = indexOf(l.agentNames)
	l.variantIndex = indexOf(l.variantNames)
	l.projectIndex = indexOf(l.projectNames)
	l.entrypointIndex = indexOf(l.entrypointNames)
	l.sessionIndex = indexOf(l.sessionIDs)
	l.finishIndex = indexOf(l.finishNames)
	l.timingIndex = indexOf(ocsource.TimingReasons)
	return l
}

// Map renders the lookups section.
func (l *lookups) Map() map[string]any {
	models := make([]any, len(l.b.models))
	for i, model := range l.b.models {
		models[i] = obj(
			"id", model,
			"short", l.b.modelShort[model],
			"provider", l.b.modelProvider[model],
		)
	}
	return obj(
		"models", models,
		"tools", toAnyStrings(l.toolNames),
		"agents", toAnyStrings(l.agentNames),
		"variants", toAnyStrings(l.variantNames),
		"projects", toAnyStrings(l.projectNames),
		"projectLabels", toAnyStrings(l.projectLabels),
		"entrypoints", toAnyStrings(l.entrypointNames),
		"finishes", toAnyStrings(l.finishNames),
		"timingReasons", toAnyStrings(ocsource.TimingReasons),
	)
}

// sortedSet returns the sorted keys of a string set.
func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// sortedSetOf returns the sorted distinct values of a row field.
func sortedSetOf(rows []*ocsource.Row, get func(*ocsource.Row) string) []string {
	set := map[string]bool{}
	for _, row := range rows {
		set[get(row)] = true
	}
	return sortedSet(set)
}

// indexOf maps each value to its position.
func indexOf(values []string) map[string]int {
	out := make(map[string]int, len(values))
	for i, value := range values {
		out[value] = i
	}
	return out
}
