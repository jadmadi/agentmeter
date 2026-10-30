// SPDX-License-Identifier: AGPL-3.0-only

package compare

import (
	"math"
	"sort"
	"strings"

	"github.com/jadmadi/agentmeter/internal/stats"
)

// NormalizeFamily reduces a model id and short name to a cross-tool family
// key. Provider prefixes and version punctuation are normalized so
// "agentrouter/deepseek-v4-flash" and "MiniMaxAI/MiniMax-M3" compare by their
// bare model family.
func NormalizeFamily(id, short string) string {
	name := short
	if name == "" {
		name = id
	}
	if index := strings.LastIndexByte(name, '/'); index >= 0 {
		name = name[index+1:]
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "(unknown)"
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case r == ' ' || r == '_' || r == '-' || r == ':' || r == '@':
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		default:
			builder.WriteRune(r)
			lastDash = false
		}
	}
	return strings.Trim(builder.String(), "-")
}

// NormalizeTool maps a raw tool name to a shared category so tool-mix
// comparisons are meaningful across tools with different vocabularies.
func NormalizeTool(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return "other"
	}
	containsAny := func(terms ...string) bool {
		for _, term := range terms {
			if strings.Contains(lower, term) {
				return true
			}
		}
		return false
	}
	switch {
	case lower == "(final answer)" || containsAny("final answer"):
		return "final answer"
	case containsAny("shell", "bash", "terminal", "exec", "command"):
		return "shell"
	case containsAny("todo", "plan"):
		return "todo"
	case containsAny("web", "fetch", "browse"):
		return "web"
	case containsAny("write", "create_file"):
		return "write"
	case containsAny("edit", "patch", "replace", "apply_patch"):
		return "edit"
	case containsAny("read", "view", "open_file"):
		return "read"
	case containsAny("grep", "search", "glob", "find", "list"):
		return "search"
	case containsAny("task", "agent", "delegate", "subagent"):
		return "agent"
	}
	return "other"
}

// PercentDelta returns (a-b)/|b|*100, or nil when either side is missing or b
// is zero. Positive values mean a is larger.
func PercentDelta(a, b *float64) *float64 {
	if a == nil || b == nil || *b == 0 || !finite(*a) || !finite(*b) {
		return nil
	}
	value := (*a - *b) / math.Abs(*b) * 100
	if !finite(value) {
		return nil
	}
	return &value
}

// PointDelta returns (a-b)*100 for rates in [0,1], or nil when either side is
// missing.
func PointDelta(a, b *float64) *float64 {
	if a == nil || b == nil || !finite(*a) || !finite(*b) {
		return nil
	}
	value := (*a - *b) * 100
	return &value
}

// Winner names the better side. higherBetter flips the comparison for costs.
// A difference smaller than epsilon is a tie.
func Winner(a, b *float64, higherBetter bool) string {
	if a == nil || b == nil {
		return "n/a"
	}
	epsilon := 1e-9 * math.Max(1, math.Max(math.Abs(*a), math.Abs(*b)))
	diff := *a - *b
	if math.Abs(diff) <= epsilon {
		return "tie"
	}
	aWins := diff > 0
	if !higherBetter {
		aWins = !aWins
	}
	if aWins {
		return ToolOC
	}
	return ToolCMD
}

// Ratio returns numerator/denominator when the denominator is non-zero.
func Ratio(numerator, denominator float64) (float64, bool) {
	if denominator == 0 || !finite(numerator) || !finite(denominator) {
		return 0, false
	}
	value := numerator / denominator
	if !finite(value) {
		return 0, false
	}
	return value, true
}

// DivPtr returns numerator/denominator, or nil when the denominator is zero.
func DivPtr(numerator, denominator float64) *float64 {
	value, ok := Ratio(numerator, denominator)
	if !ok {
		return nil
	}
	return &value
}

// Rate builds an optional rate from integer counts.
func Rate(numerator, denominator int64) *float64 {
	value, ok := Ratio(float64(numerator), float64(denominator))
	if !ok {
		return nil
	}
	return &value
}

// WeightedRate is total tokens / total seconds.
func WeightedRate(tokens int64, seconds float64) *float64 {
	value, ok := Ratio(float64(tokens), seconds)
	if !ok {
		return nil
	}
	return &value
}

// MedianOf returns the median of values, or nil for an empty sample.
func MedianOf(values []float64) *float64 {
	value, ok := stats.Median(values)
	if !ok {
		return nil
	}
	return &value
}

// PercentileOf returns the p-th percentile of values, or nil when empty.
func PercentileOf(values []float64, p float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	value, ok := stats.Percentile(sorted, p)
	if !ok {
		return nil
	}
	return &value
}

// BucketFor returns the strata bucket label for a generated-token count.
func BucketFor(generated int64) (string, bool) {
	for _, bucket := range StrataBuckets {
		if bucket.Low <= generated && generated < bucket.High {
			return bucket.Label, true
		}
	}
	return "", false
}

// PerKThousand scales a total cost to $/1k generated tokens.
func PerKThousand(cost float64, generated int64) *float64 {
	if generated == 0 {
		return nil
	}
	value := cost / float64(generated) * 1000
	return &value
}

// PerMillion scales a total cost to $/1M generated tokens.
func PerMillion(cost float64, generated int64) *float64 {
	if generated == 0 {
		return nil
	}
	value := cost / float64(generated) * 1e6
	return &value
}

// R1 rounds to one decimal with Python semantics.
func R1(value *float64) *float64 { return roundPtr(value, 1) }

// R2 rounds to two decimals with Python semantics.
func R2(value *float64) *float64 { return roundPtr(value, 2) }

// R4 rounds to four decimals with Python semantics.
func R4(value *float64) *float64 { return roundPtr(value, 4) }

// R6 rounds to six decimals with Python semantics.
func R6(value *float64) *float64 { return roundPtr(value, 6) }

func roundPtr(value *float64, digits int) *float64 {
	if value == nil || !finite(*value) {
		return nil
	}
	rounded := stats.PyRound(*value, digits)
	return &rounded
}

// Round rounds a plain float with Python semantics.
func Round(value float64, digits int) float64 {
	if !finite(value) {
		return 0
	}
	return stats.PyRound(value, digits)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
