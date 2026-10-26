// SPDX-License-Identifier: AGPL-3.0-only

package pyjson

import (
	"encoding/json"
	"math"
	"strconv"
)

// Query walks a decoded JSON document by object key or array index and returns
// the value plus whether the path exists.
//
// Path elements address map keys; a purely numeric element addresses an array
// index. Example:
//
//	v, ok := pyjson.Query(report, "meta", "timing", "plausible")
//	first, ok := pyjson.Query(report, "steps", "0", "1")
//
// Query never panics and never allocates for a miss.
func Query(v any, path ...string) (any, bool) {
	current := v
	for _, segment := range path {
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[segment]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// QueryMap is Query asserting a map[string]any.
func QueryMap(v any, path ...string) (map[string]any, bool) {
	value, ok := Query(v, path...)
	if !ok {
		return nil, false
	}
	m, ok := value.(map[string]any)
	return m, ok
}

// QuerySlice is Query asserting a []any.
func QuerySlice(v any, path ...string) ([]any, bool) {
	value, ok := Query(v, path...)
	if !ok {
		return nil, false
	}
	s, ok := value.([]any)
	return s, ok
}

// QueryString is Query asserting a string.
func QueryString(v any, path ...string) (string, bool) {
	value, ok := Query(v, path...)
	if !ok {
		return "", false
	}
	s, ok := value.(string)
	return s, ok
}

// QueryBool is Query asserting a bool.
func QueryBool(v any, path ...string) (bool, bool) {
	value, ok := Query(v, path...)
	if !ok {
		return false, false
	}
	b, ok := value.(bool)
	return b, ok
}

// QueryFloat is Query asserting a number. json.Number and every Go integer
// kind are widened to float64.
func QueryFloat(v any, path ...string) (float64, bool) {
	value, ok := Query(v, path...)
	if !ok {
		return 0, false
	}
	return Float(value)
}

// QueryInt is Query asserting an integral number.
func QueryInt(v any, path ...string) (int64, bool) {
	value, ok := Query(v, path...)
	if !ok {
		return 0, false
	}
	return Int(value)
}

// Float widens a JSON number to float64.
func Float(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int8:
		return float64(value), true
	case int16:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint8:
		return float64(value), true
	case uint16:
		return float64(value), true
	case uint32:
		return float64(value), true
	case uint64:
		return float64(value), true
	case json.Number:
		f, err := value.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// Int narrows a JSON number to int64. Non-integral, non-finite and
// out-of-range values report false.
func Int(v any) (int64, bool) {
	switch value := v.(type) {
	case int:
		return int64(value), true
	case int8:
		return int64(value), true
	case int16:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case uint:
		if uint64(value) > math.MaxInt64 {
			return 0, false
		}
		return int64(value), true
	case uint8:
		return int64(value), true
	case uint16:
		return int64(value), true
	case uint32:
		return int64(value), true
	case uint64:
		if value > math.MaxInt64 {
			return 0, false
		}
		return int64(value), true
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) {
			return 0, false
		}
		if value < math.MinInt64 || value > math.MaxInt64 {
			return 0, false
		}
		return int64(value), true
	case float32:
		return Int(float64(value))
	case json.Number:
		if i, err := value.Int64(); err == nil {
			return i, true
		}
		if f, err := value.Float64(); err == nil {
			return Int(f)
		}
	}
	return 0, false
}
