// SPDX-License-Identifier: AGPL-3.0-only

package musetelemetry

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/jadmadi/agentmeter/internal/terminal"
)

// printSummary renders a compact, unit-explicit terminal summary.
func printSummary(w io.Writer, data map[string]any, jsonPath, jsPath, htmlPath, root, filter string) {
	totals := objectField(data, "totals")
	timed := objectField(data, "timed")
	allTimed := objectField(timed, "all")
	textTimed := objectField(timed, "text")
	meta := objectField(data, "meta")
	health := objectField(data, "sessionHealth")
	cache := objectField(data, "cache")
	cost := objectField(data, "cost")

	steps := intField(totals, "steps")
	timedN := intField(allTimed, "n")
	percent := 0.0
	if steps > 0 {
		percent = float64(timedN) / float64(steps) * 100
	}

	terminal.Title(w, "AgentMeter · Muse")
	fmt.Fprintln(w)
	terminal.Line(w, "SOURCE", fmt.Sprintf("%s files · %s sessions · %s models",
		terminal.Count(intField(meta, "filesScanned")),
		terminal.Count(intField(meta, "sessions")),
		terminal.Count(intField(meta, "models"))))
	terminal.Line(w, "ROOT", root)
	if filter != "" {
		terminal.Line(w, "FILTER", fmt.Sprintf("0 of %s files matched %s",
			terminal.Count(intField(meta, "filesScanned")), filter))
	}
	terminal.Line(w, "ROWS", fmt.Sprintf("%s steps · %s timed (%.1f%%) · %s fork copies",
		terminal.Count(steps), terminal.Count(timedN), percent,
		terminal.Count(intField(meta, "duplicatesSkipped"))))
	terminal.Line(w, "TOKENS", fmt.Sprintf("%s out · %s input incl cache",
		terminal.Compact(float64(intField(totals, "outputTokens"))),
		terminal.Compact(float64(inputTotal(totals)))))
	if timedN > 0 {
		terminal.Line(w, "SPEED", fmt.Sprintf("%s / %s / %s tok/s (weighted / median / p90)",
			display(allTimed["weighted"]), display(allTimed["median"]), display(allTimed["p90"])))
	}
	if intField(textTimed, "n") > 0 {
		terminal.Line(w, "TEXT", fmt.Sprintf("%s / %s / %s tok/s (weighted / median / p90) · %s replies",
			display(textTimed["weighted"]), display(textTimed["median"]), display(textTimed["p90"]),
			terminal.Count(intField(textTimed, "n"))))
	}
	terminal.Line(w, "COST", fmt.Sprintf("$%s recorded · ~$%s cache savings · $%s/step",
		commaFloat(floatField(cost, "recorded"), 2),
		commaFloat(floatField(cache, "savings"), 2),
		commaFloat(floatField(cost, "perStep"), 4)))
	terminal.Line(w, "CACHE", fmt.Sprintf("%.1f%% read · %.2f%% write",
		floatField(cache, "hitRate")*100, floatField(cache, "writeShare")*100))
	terminal.Line(w, "HEALTH", fmt.Sprintf("%s subagents · %s omissions · %s incomplete calls",
		terminal.Count(intField(objectField(health, "subagents"), "count")),
		terminal.Count(intField(objectField(health, "omissions"), "count")),
		terminal.Count(intField(objectField(health, "incomplete"), "count"))))

	models := topModels(data, 6)
	if len(models) > 0 {
		terminal.Section(w, fmt.Sprintf("MODELS (top %d of %s, by steps)",
			len(models), terminal.Count(intField(meta, "models"))))
		rows := make([][]string, 0, len(models))
		for _, item := range models {
			fixed := display(item["fixedSeconds"])
			if fixed != "-" {
				fixed += "s"
			}
			rows = append(rows, []string{
				modelName(item),
				terminal.Count(intField(item, "steps")),
				display(item["weighted"]),
				display(item["median"]),
				display(item["p90"]),
				display(item["pureRate"]),
				fixed,
			})
		}
		terminal.Table(w,
			[]string{"MODEL", "STEPS", "TOK/S", "MED", "P90", "PURE", "FIXED"},
			rows,
			[]bool{false, true, true, true, true, true, true},
			[]int{28, 7, 6, 6, 6, 6, 7})
		terminal.Note(w, "tok/s weighted · med/p90 generated tok/s")
		terminal.Note(w, "pure marginal tok/s · fixed model seconds")
	}

	terminal.Section(w, "REPORT")
	fmt.Fprintf(w, "  %s\n", htmlPath)
	fmt.Fprintf(w, "  %s\n", jsonPath)
	fmt.Fprintf(w, "  %s\n", jsPath)
}

func topModels(data map[string]any, limit int) []map[string]any {
	entries, _ := data["byModel"].([]any)
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if item, ok := entry.(map[string]any); ok {
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return intField(out[i], "steps") > intField(out[j], "steps")
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func modelName(item map[string]any) string {
	if name := textField(item, "short"); name != "" {
		return name
	}
	if name := textField(item, "model"); name != "" {
		return name
	}
	return "(unknown)"
}

func display(value any) string {
	if value == nil {
		return "-"
	}
	return number(value)
}

func inputTotal(totals map[string]any) int64 {
	if value, ok := totals["inputTotal"]; ok {
		if parsed, ok := numericValue(value); ok {
			return int64(parsed)
		}
	}
	return intField(totals, "inputTokens")
}

func objectField(data map[string]any, key string) map[string]any {
	value, ok := data[key].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return value
}

func intField(data map[string]any, key string) int64 {
	value, ok := numericValue(data[key])
	if !ok {
		return 0
	}
	return int64(value)
}

func floatField(data map[string]any, key string) float64 {
	value, ok := numericValue(data[key])
	if !ok {
		return 0
	}
	return value
}

func textField(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return value
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	case float32:
		return float64(typed), true
	default:
		return 0, false
	}
}

// number is Python's str() for the numeric fields used in the summary.
func number(value any) string {
	parsed, ok := numericValue(value)
	if !ok {
		return "None"
	}
	return strconv.FormatFloat(parsed, 'g', -1, 64)
}

// commaFloat is Python's f"{value:,.Nf}".
func commaFloat(value float64, digits int) string {
	text := strconv.FormatFloat(value, 'f', digits, 64)
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign = "-"
		text = text[1:]
	}
	intPart := text
	fracPart := ""
	if index := strings.IndexByte(text, '.'); index >= 0 {
		intPart = text[:index]
		fracPart = text[index:]
	}
	grouped := make([]byte, 0, len(intPart)+len(intPart)/3)
	for index, digit := range []byte(intPart) {
		if index > 0 && (len(intPart)-index)%3 == 0 {
			grouped = append(grouped, ',')
		}
		grouped = append(grouped, digit)
	}
	return sign + string(grouped) + fracPart
}
