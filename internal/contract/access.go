// SPDX-License-Identifier: AGPL-3.0-only

package contract

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
)

// Value returns a map entry and whether the key exists.
func Value(m map[string]any, key string) (any, bool) {
	if m == nil {
		return nil, false
	}
	value, ok := m[key]
	return value, ok
}

// Get mirrors Python's dict.get(key) with no default: a missing key and a
// present null both read as nil.
func Get(m map[string]any, key string) any {
	value, _ := Value(m, key)
	return value
}

// GetOr mirrors Python's dict.get(key, default).
func GetOr(m map[string]any, key string, fallback any) any {
	if value, ok := Value(m, key); ok {
		return value
	}
	return fallback
}

// ObjectValue returns v as an object when it is one.
func ObjectValue(v any) (map[string]any, bool) {
	object, ok := v.(map[string]any)
	return object, ok
}

// Object returns m[key] as an object, or nil when it is missing or not an
// object.
func Object(m map[string]any, key string) map[string]any {
	object, _ := ObjectValue(Get(m, key))
	return object
}

// SliceValue returns v as a JSON array when it is one.
func SliceValue(v any) ([]any, bool) {
	list, ok := v.([]any)
	return list, ok
}

// Slice returns m[key] as an array, or nil when it is missing or not an array.
func Slice(m map[string]any, key string) []any {
	list, _ := SliceValue(Get(m, key))
	return list
}

// Rows converts an array of objects into report rows. Missing and falsy
// sections yield no rows (Python's "or []"). A non-object entry becomes an
// empty map so downstream per-row checks report real violations instead of
// panicking.
func Rows(v any) []map[string]any {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	rows := make([]map[string]any, len(list))
	for i, item := range list {
		if object, ok := item.(map[string]any); ok {
			rows[i] = object
			continue
		}
		rows[i] = map[string]any{}
	}
	return rows
}

// ObjectSection mirrors Python's `data.get(key) or {}`. ok is false when the
// value is neither an object nor falsy, which the Python validator would only
// discover by raising AttributeError.
func ObjectSection(v any) (map[string]any, bool) {
	if object, ok := v.(map[string]any); ok {
		return object, true
	}
	if falsy(v) {
		return map[string]any{}, true
	}
	return map[string]any{}, false
}

// ListSection mirrors Python's `data.get(key) or []`. ok is false when the
// value is neither an array nor falsy.
func ListSection(v any) ([]map[string]any, bool) {
	if list, ok := v.([]any); ok {
		return Rows(list), true
	}
	if falsy(v) {
		return nil, true
	}
	return nil, false
}

// StringValue returns v as a string when it is one.
func StringValue(v any) (string, bool) {
	text, ok := v.(string)
	return text, ok
}

// BoolValue returns v as a bool when it is one.
func BoolValue(v any) (bool, bool) {
	flag, ok := v.(bool)
	return flag, ok
}

// Number returns v as a float64 when it is a JSON number. Integers wider than
// float64 lose precision; use numValue for exact comparisons.
func Number(v any) (float64, bool) {
	n := numValue(v)
	if !n.valid() {
		return 0, false
	}
	return n.f, true
}

// Integer returns v as an int64 when it is a Python int (an integer literal, a
// bool, or a json.Number without a fraction). A float such as 2.0 is rejected
// on purpose: Python cannot index a list with it, and the Python audits would
// fail on a report that stored an index as a float. Decode reports with
// json.Number when exact kinds matter.
func Integer(v any) (int64, bool) {
	n := numValue(v)
	if !n.valid() || !n.isPyInt || n.r == nil || !n.r.IsInt() {
		return 0, false
	}
	if !n.r.Num().IsInt64() {
		return 0, false
	}
	return n.r.Num().Int64(), true
}

// falsy reports Python truthiness for a decoded JSON value.
func falsy(v any) bool {
	switch value := v.(type) {
	case nil:
		return true
	case bool:
		return !value
	case string:
		return value == ""
	case int:
		return value == 0
	case int8:
		return value == 0
	case int16:
		return value == 0
	case int32:
		return value == 0
	case int64:
		return value == 0
	case uint:
		return value == 0
	case uint8:
		return value == 0
	case uint16:
		return value == 0
	case uint32:
		return value == 0
	case uint64:
		return value == 0
	case float32:
		return value == 0
	case float64:
		return value == 0
	case json.Number:
		f, err := value.Float64()
		return err == nil && f == 0
	case map[string]any:
		return len(value) == 0
	case []any:
		return len(value) == 0
	}
	return false
}

// number is a Python-flavoured numeric view of a decoded JSON value.
//
// Python's validator treats int, float and bool (a subclass of int) as
// numbers. set is false for a missing key and for a present null, because
// dict.get cannot tell them apart. ok is false for a present non-numeric
// value, which the Python validator would only hit by raising TypeError.
type number struct {
	raw     any
	set     bool
	ok      bool
	isPyInt bool
	r       *big.Rat
	f       float64
}

func numValue(v any) number {
	switch value := v.(type) {
	case nil:
		return number{}
	case bool:
		n := number{raw: v, set: true, ok: true, isPyInt: true, f: 0}
		if value {
			n.r = big.NewRat(1, 1)
			n.f = 1
		} else {
			n.r = big.NewRat(0, 1)
		}
		return n
	case int:
		return intNumber(v, int64(value))
	case int8:
		return intNumber(v, int64(value))
	case int16:
		return intNumber(v, int64(value))
	case int32:
		return intNumber(v, int64(value))
	case int64:
		return intNumber(v, value)
	case uint:
		return uintNumber(v, uint64(value))
	case uint8:
		return uintNumber(v, uint64(value))
	case uint16:
		return uintNumber(v, uint64(value))
	case uint32:
		return uintNumber(v, uint64(value))
	case uint64:
		return uintNumber(v, value)
	case float32:
		return floatNumber(v, float64(value))
	case float64:
		return floatNumber(v, value)
	case json.Number:
		n := number{raw: v, set: true}
		if i, err := value.Int64(); err == nil {
			n.ok, n.isPyInt, n.r, n.f = true, true, big.NewRat(i, 1), float64(i)
			return n
		}
		if u, err := strconv.ParseUint(string(value), 10, 64); err == nil {
			n.ok, n.isPyInt = true, true
			n.r = new(big.Rat).SetInt(new(big.Int).SetUint64(u))
			n.f = float64(u)
			return n
		}
		if bi, ok := new(big.Int).SetString(string(value), 10); ok {
			n.ok, n.isPyInt = true, true
			n.r = new(big.Rat).SetInt(bi)
			n.f, _ = new(big.Float).SetInt(bi).Float64()
			return n
		}
		if f, err := value.Float64(); err == nil {
			n.ok = true
			n.f = f
			if finiteFloat(f) {
				n.r = new(big.Rat).SetFloat64(f)
			}
		}
		return n
	}
	return number{raw: v, set: true}
}

func intNumber(raw any, value int64) number {
	return number{raw: raw, set: true, ok: true, isPyInt: true, r: big.NewRat(value, 1), f: float64(value)}
}

func uintNumber(raw any, value uint64) number {
	return number{
		raw:     raw,
		set:     true,
		ok:      true,
		isPyInt: true,
		r:       new(big.Rat).SetInt(new(big.Int).SetUint64(value)),
		f:       float64(value),
	}
}

func floatNumber(raw any, value float64) number {
	n := number{raw: raw, set: true, ok: true, f: value}
	if finiteFloat(value) {
		n.r = new(big.Rat).SetFloat64(value)
	}
	return n
}

func finiteFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func zeroNumber() number {
	return number{set: true, ok: true, isPyInt: true, r: big.NewRat(0, 1)}
}

func (n number) valid() bool { return n.set && n.ok }

func (n number) finite() bool {
	if !n.valid() {
		return false
	}
	if n.isPyInt {
		return true
	}
	return finiteFloat(n.f)
}

func (n number) nonNegative() bool {
	return n.finite() && n.r != nil && n.r.Sign() >= 0
}

// truthy mirrors Python's bool(value) for the cases the validator uses.
func (n number) truthy() bool {
	if !n.set {
		return false
	}
	if !n.ok {
		return true
	}
	return n.r == nil || n.r.Sign() != 0
}

func (n number) floatValue() float64 { return n.f }

// numAt reads m[key] with dict.get semantics (missing and null are "unset").
func numAt(m map[string]any, key string) number {
	return numValue(m[key])
}

// getNum mirrors m.get(key, 0) used in arithmetic: a missing key is zero and a
// present null or non-numeric value is invalid, because Python would raise.
func getNum(m map[string]any, key string) (number, bool) {
	raw, present := m[key]
	if !present {
		return zeroNumber(), true
	}
	n := numValue(raw)
	if !n.valid() {
		return n, false
	}
	return n, true
}

// getNumOrZero mirrors (m.get(key) or 0): missing and null are zero.
func getNumOrZero(m map[string]any, key string) (number, bool) {
	n := numValue(m[key])
	if !n.set {
		return zeroNumber(), true
	}
	if !n.ok {
		return n, false
	}
	return n, true
}

// equalNumbers mirrors Python's exact `a == b`.
func equalNumbers(a, b number) bool {
	if !a.valid() || !b.valid() || a.r == nil || b.r == nil {
		return false
	}
	return a.r.Cmp(b.r) == 0
}

// lessOrEqual mirrors Python's exact `a <= b`.
func lessOrEqual(a, b number) bool {
	if !a.valid() || !b.valid() || a.r == nil || b.r == nil {
		return false
	}
	return a.r.Cmp(b.r) <= 0
}

// closeNumbers ports telemetry_common.close:
//
//	abs(a - b) <= max(abs(tol), abs(tol * b))
//
// with None/None matching and non-finite values never closing.
func closeNumbers(a, b number, tol float64) bool {
	if !a.set || !b.set {
		return a.set == b.set
	}
	if !a.finite() || !b.finite() {
		return false
	}
	bound := math.Max(math.Abs(tol), math.Abs(tol*b.floatValue()))
	if a.isPyInt && b.isPyInt {
		if a.r == nil || b.r == nil {
			return false
		}
		diff := new(big.Rat).Sub(a.r, b.r)
		diff.Abs(diff)
		return diff.Cmp(new(big.Rat).SetFloat64(bound)) <= 0
	}
	return math.Abs(a.floatValue()-b.floatValue()) <= bound
}

// sumNumbers adds exact rational values, mirroring Python's exact integer
// arithmetic and float addition order for the first operand only. Reports sum
// integer token/count fields, so the rational result equals the Python result;
// a float sum is represented exactly rather than with CPython's per-step
// rounding.
func sumNumbers(values ...number) (*big.Rat, bool) {
	total := big.NewRat(0, 1)
	for _, value := range values {
		if !value.valid() || value.r == nil {
			return nil, false
		}
		total.Add(total, value.r)
	}
	return total, true
}

// sumFieldDefaultZero mirrors sum(row.get(key, 0) for row in rows): a missing
// key adds zero, a present null or non-numeric value is invalid.
func sumFieldDefaultZero(rows []map[string]any, key string) (*big.Rat, bool) {
	total := big.NewRat(0, 1)
	for _, row := range rows {
		value, ok := getNum(row, key)
		if !ok || value.r == nil {
			return nil, false
		}
		total.Add(total, value.r)
	}
	return total, true
}

// sumFieldNilAsZero mirrors sum(row.get(key) or 0 for row in rows).
func sumFieldNilAsZero(rows []map[string]any, key string) (*big.Rat, bool) {
	total := big.NewRat(0, 1)
	for _, row := range rows {
		value, ok := getNumOrZero(row, key)
		if !ok || value.r == nil {
			return nil, false
		}
		total.Add(total, value.r)
	}
	return total, true
}

// sumFieldChain mirrors sum(row.get(primary, row.get(fallback, 0)) for row in
// rows).
func sumFieldChain(rows []map[string]any, primary, fallback string) (*big.Rat, bool) {
	total := big.NewRat(0, 1)
	for _, row := range rows {
		raw, present := row[primary]
		if !present {
			raw, present = row[fallback]
			if !present {
				continue
			}
		}
		value := numValue(raw)
		if !value.valid() || value.r == nil {
			return nil, false
		}
		total.Add(total, value.r)
	}
	return total, true
}

// keyOf maps a Python dict key onto a comparable Go string, keeping Python's
// numeric key equality (1, 1.0 and True are the same key).
func keyOf(v any) string {
	if v == nil {
		return "none"
	}
	if text, ok := v.(string); ok {
		return "s:" + text
	}
	n := numValue(v)
	if n.valid() && n.r != nil {
		return "n:" + n.r.RatString()
	}
	return "o:" + PyRepr(v)
}
