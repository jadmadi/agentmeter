// SPDX-License-Identifier: AGPL-3.0-only

package muse

import (
	"math"
	"strconv"
	"strings"
)

// The Python loader reads loosely typed JSON and coerces with int(), float(),
// str() and truthiness. These helpers reproduce that behavior for the value
// shapes the transcript can contain (nil, bool, float64, string, array,
// object). Python would raise on a few exotic mismatches; those inputs cannot
// produce a report, so the helpers are total instead of panic-prone.

// asObject returns a JSON object or nil.
func asObject(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return nil
}

// asString returns a string value, or "" for anything else.
func asString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// stringOr returns asString(value) unless the value is falsy, in which case it
// returns fallback. Mirrors `value or fallback`.
func stringOr(value any, fallback string) string {
	if truthy(value) {
		if text, ok := value.(string); ok {
			return text
		}
		return pyStr(value)
	}
	return fallback
}

// truthy implements Python truthiness for decoded JSON values.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// pyStr reproduces str() for the scalar JSON types.
func pyStr(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case float64:
		return pyFloatRepr(typed)
	default:
		return ""
	}
}

// pyFloatRepr reproduces repr(float): integral values keep a trailing ".0".
func pyFloatRepr(value float64) string {
	if math.IsInf(value, 1) {
		return "inf"
	}
	if math.IsInf(value, -1) {
		return "-inf"
	}
	if math.IsNaN(value) {
		return "nan"
	}
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eEnN") {
		text += ".0"
	}
	// Go prints exponents as "1e+06"; Python prints "1000000.0" for 1e6 but
	// "1e+20" above 1e16. Only the 1e16+ range reaches here in practice.
	return text
}

// toInt reproduces int(value or 0): nil becomes 0, floats truncate toward
// zero, numeric strings parse.
func toInt(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case bool:
		if typed {
			return 1
		}
		return 0
	case float64:
		return int64(math.Trunc(typed))
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0
		}
		if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
			return parsed
		}
		if parsed, err := strconv.ParseFloat(text, 64); err == nil {
			return int64(math.Trunc(parsed))
		}
		return 0
	default:
		return 0
	}
}

// toFloat reproduces float(value or 0).
func toFloat(value any) float64 {
	switch typed := value.(type) {
	case nil:
		return 0
	case bool:
		if typed {
			return 1
		}
		return 0
	case float64:
		return typed
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// floatPtr returns a pointer to value, or nil when value is nil.
func floatPtr(value any) *float64 {
	if value == nil {
		return nil
	}
	parsed := toFloat(value)
	return &parsed
}

// strPtr returns a pointer to the string value, or nil.
func strPtr(value any) *string {
	if value == nil {
		return nil
	}
	text := asString(value)
	return &text
}
