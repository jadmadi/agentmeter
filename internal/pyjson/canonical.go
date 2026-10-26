// SPDX-License-Identifier: AGPL-3.0-only

package pyjson

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxDepth bounds recursion so a cyclic map cannot hang the process. CPython
// raises RecursionError well below this, so no realistic payload is affected.
const maxDepth = 1000

// ErrUnsupported reports a value that has no Python JSON equivalent
// (channels, functions, structs, ...).
var ErrUnsupported = errors.New("pyjson: unsupported value type")

// Canonical encodes v as CPython's
// json.dumps(v, sort_keys=True, separators=(",", ":"), ensure_ascii=False).
//
// Supported values are nil, bool, string, all Go integer kinds, uint64,
// float32/float64, json.Number, map[string]any, []any, maps with string keys
// and slices/arrays of any element type. A nil map or slice encodes as null,
// matching encoding/json.
//
// Parity notes:
//   - Floats use CPython's repr rules (shortest round-trip digits, scientific
//     notation only when the decimal point falls outside (-4, 16]); integral
//     floats keep CPython's trailing ".0".
//   - Non-finite floats encode as NaN, Infinity and -Infinity, matching
//     json.dumps with allow_nan=True.
//   - Strings are escaped like CPython with ensure_ascii=False: only quote,
//     backslash and C0 controls are escaped; "<", ">", "&", U+007F, U+0080,
//     U+2028 and U+2029 stay raw.
//   - Invalid UTF-8 bytes cannot exist in a Python str; each invalid byte is
//     replaced by U+FFFD (the same replacement encoding/json performs).
//   - json.Number values are re-parsed and reformatted, so "1e5" canonicalises
//     to "100000.0" exactly like a Python float round-trip.
func Canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := appendCanonical(&buf, v, 0); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Signature returns the lowercase hex SHA-256 of Canonical(v).
func Signature(v any) (string, error) {
	b, err := Canonical(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func appendCanonical(buf *bytes.Buffer, v any, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("pyjson: nesting deeper than %d levels (possible cycle)", maxDepth)
	}
	switch value := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		appendBool(buf, value)
	case string:
		appendString(buf, value)
	case int:
		buf.WriteString(strconv.FormatInt(int64(value), 10))
	case int8:
		buf.WriteString(strconv.FormatInt(int64(value), 10))
	case int16:
		buf.WriteString(strconv.FormatInt(int64(value), 10))
	case int32:
		buf.WriteString(strconv.FormatInt(int64(value), 10))
	case int64:
		buf.WriteString(strconv.FormatInt(value, 10))
	case uint:
		buf.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint8:
		buf.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint16:
		buf.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint32:
		buf.WriteString(strconv.FormatUint(uint64(value), 10))
	case uint64:
		buf.WriteString(strconv.FormatUint(value, 10))
	case float32:
		appendFloat(buf, float64(value))
	case float64:
		appendFloat(buf, value)
	case json.Number:
		return appendJSONNumber(buf, value)
	case []byte:
		// encoding/json base64-encodes []byte and Python has no JSON bytes
		// type; refuse rather than guess which canonical form was meant.
		return fmt.Errorf("%w: []byte", ErrUnsupported)
	case []any:
		if value == nil {
			buf.WriteString("null")
			return nil
		}
		buf.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := appendCanonical(buf, item, depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		if value == nil {
			buf.WriteString("null")
			return nil
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			appendString(buf, key)
			buf.WriteByte(':')
			if err := appendCanonical(buf, value[key], depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return appendReflect(buf, v, depth)
	}
	return nil
}

func appendBool(buf *bytes.Buffer, value bool) {
	if value {
		buf.WriteString("true")
		return
	}
	buf.WriteString("false")
}

func appendJSONNumber(buf *bytes.Buffer, value json.Number) error {
	text := string(value)
	if _, err := strconv.ParseInt(text, 10, 64); err == nil {
		// Keep the literal: it is already a canonical decimal integer.
		buf.WriteString(text)
		return nil
	}
	if _, err := strconv.ParseUint(text, 10, 64); err == nil {
		buf.WriteString(text)
		return nil
	}
	f, err := value.Float64()
	if err != nil {
		return fmt.Errorf("pyjson: invalid json.Number %q: %w", text, err)
	}
	appendFloat(buf, f)
	return nil
}

// appendReflect handles typed maps/slices and pointers so callers are not
// forced to convert everything to map[string]any before hashing.
func appendReflect(buf *bytes.Buffer, v any, depth int) error {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			buf.WriteString("null")
			return nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Bool:
		appendBool(buf, rv.Bool())
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		buf.WriteString(strconv.FormatInt(rv.Int(), 10))
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		buf.WriteString(strconv.FormatUint(rv.Uint(), 10))
		return nil
	case reflect.Float32, reflect.Float64:
		appendFloat(buf, rv.Float())
		return nil
	case reflect.String:
		appendString(buf, rv.String())
		return nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("%w: map key %s", ErrUnsupported, rv.Type().Key())
		}
		if rv.IsNil() {
			buf.WriteString("null")
			return nil
		}
		// Keep values with their keys: reflect.MapIndex would need a key of
		// the map's exact (possibly named) string type.
		type entry struct {
			key   string
			value reflect.Value
		}
		entries := make([]entry, 0, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			entries = append(entries, entry{key: iter.Key().String(), value: iter.Value()})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
		buf.WriteByte('{')
		for i, item := range entries {
			if i > 0 {
				buf.WriteByte(',')
			}
			appendString(buf, item.key)
			buf.WriteByte(':')
			if err := appendCanonical(buf, item.value.Interface(), depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			buf.WriteString("null")
			return nil
		}
		buf.WriteByte('[')
		for i := 0; i < rv.Len(); i++ {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := appendCanonical(buf, rv.Index(i).Interface(), depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	}
	return fmt.Errorf("%w: %T", ErrUnsupported, v)
}

func appendFloat(buf *bytes.Buffer, value float64) {
	switch {
	case math.IsNaN(value):
		buf.WriteString("NaN")
		return
	case math.IsInf(value, 1):
		buf.WriteString("Infinity")
		return
	case math.IsInf(value, -1):
		buf.WriteString("-Infinity")
		return
	}
	buf.WriteString(PyFloat(value))
}

// PyFloat formats a finite float64 exactly like CPython's repr/str and
// json.dumps:
//
//   - shortest digits that round-trip,
//   - fixed notation when the decimal point lands in (-4, 16],
//   - scientific notation ("1e+16", "5e-324", two-digit minimum exponent)
//     otherwise,
//   - a trailing ".0" for integral values (1.0, not 1).
//
// Non-finite values are not handled here; see Canonical.
func PyFloat(value float64) string {
	// 'e' with precision -1 produces the shortest mantissa that round-trips,
	// e.g. "-1.2345e+06".
	sci := strconv.FormatFloat(value, 'e', -1, 64)
	negative := strings.HasPrefix(sci, "-")
	if negative {
		sci = sci[1:]
	}
	eAt := strings.IndexByte(sci, 'e')
	if eAt < 0 {
		return sci
	}
	mantissa := sci[:eAt]
	exponent, err := strconv.Atoi(sci[eAt+1:])
	if err != nil {
		return sci
	}
	digits := strings.Replace(mantissa, ".", "", 1)
	// decpt is the position of the decimal point relative to the first digit:
	// value = 0.<digits> * 10^decpt.
	decpt := exponent + 1

	var out string
	switch {
	case decpt <= -4 || decpt > 16:
		var b strings.Builder
		b.WriteByte(digits[0])
		if len(digits) > 1 {
			b.WriteByte('.')
			b.WriteString(digits[1:])
		}
		b.WriteByte('e')
		if exponent < 0 {
			b.WriteByte('-')
		} else {
			b.WriteByte('+')
		}
		abs := exponent
		if abs < 0 {
			abs = -abs
		}
		text := strconv.Itoa(abs)
		for len(text) < 2 {
			text = "0" + text
		}
		b.WriteString(text)
		out = b.String()
	case decpt <= 0:
		out = "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		out = digits + strings.Repeat("0", decpt-len(digits)) + ".0"
	default:
		out = digits[:decpt] + "." + digits[decpt:]
	}
	if negative {
		return "-" + out
	}
	return out
}

const hexDigits = "0123456789abcdef"

// appendString writes a CPython-compatible JSON string:
// json.dumps(s, ensure_ascii=False).
func appendString(buf *bytes.Buffer, value string) {
	buf.WriteByte('"')
	for i := 0; i < len(value); {
		c := value[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				buf.WriteString(`\"`)
			case '\\':
				buf.WriteString(`\\`)
			case '\b':
				buf.WriteString(`\b`)
			case '\f':
				buf.WriteString(`\f`)
			case '\n':
				buf.WriteString(`\n`)
			case '\r':
				buf.WriteString(`\r`)
			case '\t':
				buf.WriteString(`\t`)
			default:
				if c < 0x20 {
					buf.WriteString(`\u00`)
					buf.WriteByte(hexDigits[c>>4])
					buf.WriteByte(hexDigits[c&0xf])
				} else {
					buf.WriteByte(c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid UTF-8: encoding/json substitutes U+FFFD. Do the same so
			// the output is always valid UTF-8.
			buf.WriteString("\uFFFD")
			i++
			continue
		}
		buf.WriteString(value[i : i+size])
		i += size
	}
	buf.WriteByte('"')
}
