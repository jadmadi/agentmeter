// SPDX-License-Identifier: AGPL-3.0-only

package cmdtelemetry

import (
	"fmt"
	"strconv"
	"strings"
)

// printSummary mirrors telemetry.py:print_summary().
func printSummary(data map[string]any, jsonPath, jsPath string) {
	totals := objectField(data, "totals")
	timed := objectField(data, "timed")
	allTimed := objectField(timed, "all")
	textTimed := objectField(timed, "text")
	meta := objectField(data, "meta")
	health := objectField(data, "sessionHealth")
	cache := objectField(data, "cache")
	cost := objectField(data, "cost")

	fmt.Printf("Scanned %d files · %d sessions · %d models · %s steps (%d fork duplicates skipped)\n",
		intField(meta, "filesScanned"),
		intField(meta, "sessions"),
		intField(meta, "models"),
		commaInt(intField(totals, "steps")),
		intField(meta, "duplicatesSkipped"),
	)
	fmt.Printf("Recorded %s output tokens · %s input (incl. cache) · $%s\n",
		commaInt(intField(totals, "outputTokens")),
		commaInt(inputTotal(totals)),
		commaFloat(floatField(cost, "recorded"), 2),
	)
	if intField(allTimed, "n") != 0 {
		fmt.Printf("Timed %s steps: weighted %s tok/s · median %s · p90 %s\n",
			commaInt(intField(allTimed, "n")),
			number(allTimed["weighted"]),
			number(allTimed["median"]),
			number(allTimed["p90"]),
		)
	}
	if intField(textTimed, "n") != 0 {
		fmt.Printf("Text replies (%s): weighted %s tok/s · median %s · p90 %s\n",
			commaInt(intField(textTimed, "n")),
			number(textTimed["weighted"]),
			number(textTimed["median"]),
			number(textTimed["p90"]),
		)
	}
	if top := topByModel(data); top != nil {
		if pureRate, ok := numericValue(top["pureRate"]); ok {
			fmt.Printf("Marginal decode (%s): %s tok/s · fixed %ss\n",
				textField(top, "short"),
				number(pureRate),
				number(top["fixedSeconds"]),
			)
		}
	}
	bits := make([]string, 0, 6)
	if value, ok := numericValue(cache["hitRate"]); ok {
		bits = append(bits, fmt.Sprintf("%.1f%% read hit", value*100))
	}
	if value, ok := numericValue(cache["writeShare"]); ok {
		bits = append(bits, fmt.Sprintf("%.2f%% writes", value*100))
	}
	if value, ok := numericValue(cache["savings"]); ok {
		bits = append(bits, fmt.Sprintf("~$%s read savings", commaFloat(value, 2)))
	}
	bits = append(bits, fmt.Sprintf("recorded $%s", commaFloat(floatField(cost, "recorded"), 2)))
	if value, ok := numericValue(cost["perStep"]); ok {
		bits = append(bits, fmt.Sprintf("$%.4f/step", value))
	}
	fmt.Printf("Cache: %s\n", strings.Join(bits, " · "))
	fmt.Printf("Hooks (full history): %d events · compactions: %d · model changes: %d\n",
		intField(health, "hookEvents"),
		intField(objectField(health, "compactions"), "count"),
		intField(objectField(health, "modelChanges"), "count"),
	)
	fmt.Printf("Wrote %s\n", jsonPath)
	fmt.Printf("Wrote %s\n", jsPath)
}

func topByModel(data map[string]any) map[string]any {
	models, ok := data["byModel"].([]any)
	if !ok || len(models) == 0 {
		return nil
	}
	var best map[string]any
	bestSteps := int64(0)
	for _, entry := range models {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		steps := intField(item, "steps")
		if best == nil || steps > bestSteps {
			best = item
			bestSteps = steps
		}
	}
	return best
}

func inputTotal(totals map[string]any) int64 {
	if value, ok := totals["inputTotal"]; ok {
		if number, ok := numericValue(value); ok {
			return int64(number)
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

// commaInt is Python's f"{value:,}".
func commaInt(value int64) string {
	digits := strconv.FormatInt(value, 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign = "-"
		digits = digits[1:]
	}
	grouped := make([]byte, 0, len(digits)+len(digits)/3)
	for index, digit := range []byte(digits) {
		if index > 0 && (len(digits)-index)%3 == 0 {
			grouped = append(grouped, ',')
		}
		grouped = append(grouped, digit)
	}
	return sign + string(grouped)
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
