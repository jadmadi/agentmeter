// SPDX-License-Identifier: AGPL-3.0-only

package pyjson

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// reportSample uses only values whose Go and Python encodings agree byte for
// byte: integers, non-integral floats, strings, booleans, null, lists and
// objects. The expected text was produced with
//
//	json.dumps(data, indent=1, ensure_ascii=False)
//
// after ordering the keys the way encoding/json sorts them.
func reportSample() map[string]any {
	return map[string]any{
		"emptyList":     []any{},
		"emptyObject":   map[string]any{},
		"inexact":       0.1,
		"schemaVersion": int64(3),
		"title":         "é <script> & \"quoted\"\nline",
		"values":        []any{int64(1), "two", nil, true},
	}
}

const wantJSON = `{
 "emptyList": [],
 "emptyObject": {},
 "inexact": 0.1,
 "schemaVersion": 3,
 "title": "é <script> & \"quoted\"\nline",
 "values": [
  1,
  "two",
  null,
  true
 ]
}`

const wantJS = "window.CMD_TELEMETRY = {\"emptyList\":[],\"emptyObject\":{},\"inexact\":0.1,\"schemaVersion\":3,\"title\":\"é <script> & \\\"quoted\\\"\\nline\",\"values\":[1,\"two\",null,true]};\n"

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, reportSample()); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if got := buf.String(); got != wantJSON {
		t.Fatalf("WriteJSON mismatch\n got: %q\nwant: %q", got, wantJSON)
	}
	if strings.HasSuffix(buf.String(), "\n") {
		t.Error("WriteJSON added a trailing newline; Python's json.dumps does not")
	}
	if strings.Contains(buf.String(), "\\u003c") {
		t.Error("WriteJSON HTML-escaped '<'")
	}
	if !strings.Contains(buf.String(), "é") {
		t.Error("WriteJSON escaped non-ASCII")
	}
}

func TestJSONBytesMatchesWriteJSON(t *testing.T) {
	direct, err := JSONBytes(reportSample())
	if err != nil {
		t.Fatalf("JSONBytes: %v", err)
	}
	if string(direct) != wantJSON {
		t.Fatalf("JSONBytes mismatch\n got: %q\nwant: %q", direct, wantJSON)
	}
}

func TestWriteJS(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJS(&buf, "CMD_TELEMETRY", reportSample()); err != nil {
		t.Fatalf("WriteJS: %v", err)
	}
	if got := buf.String(); got != wantJS {
		t.Fatalf("WriteJS mismatch\n got: %q\nwant: %q", got, wantJS)
	}
	if !strings.HasPrefix(buf.String(), "window.CMD_TELEMETRY = ") {
		t.Error("WriteJS prefix is wrong")
	}
	if !strings.HasSuffix(buf.String(), ";\n") {
		t.Error("WriteJS terminator is wrong")
	}
	// The payload after the prefix must be valid JSON and equal the source.
	payload := strings.TrimSuffix(strings.TrimPrefix(buf.String(), "window.CMD_TELEMETRY = "), ";\n")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("JS payload is not JSON: %v", err)
	}
	if decoded["title"] != "é <script> & \"quoted\"\nline" {
		t.Fatalf("JS payload title = %v", decoded["title"])
	}
	if !strings.Contains(payload, "<script>") {
		t.Error("WriteJS HTML-escaped '<' inside the payload")
	}
}

func TestJSBytesMatchesWriteJS(t *testing.T) {
	direct, err := JSBytes("CMD_TELEMETRY", reportSample())
	if err != nil {
		t.Fatalf("JSBytes: %v", err)
	}
	if string(direct) != wantJS {
		t.Fatalf("JSBytes mismatch\n got: %q\nwant: %q", direct, wantJS)
	}
}

func TestWriteJSGlobalValidation(t *testing.T) {
	good := []string{"CMD_TELEMETRY", "OPENCODE_TELEMETRY"}
	for _, global := range good {
		var buf bytes.Buffer
		if err := WriteJS(&buf, global, map[string]any{}); err != nil {
			t.Errorf("WriteJS(%q): %v", global, err)
		}
	}
	bad := []string{"", "window.CMD_TELEMETRY", "CMD TELEMETRY", "CMD-TELEMETRY", "CMD;"}
	for _, global := range bad {
		var buf bytes.Buffer
		if err := WriteJS(&buf, global, map[string]any{}); err == nil {
			t.Errorf("WriteJS(%q) accepted an invalid global name", global)
		}
	}
}

// Encoding happens through encoding/json, which always escapes U+2028/U+2029.
// Python leaves them raw. The parsed JS string is identical, so this is a
// documented byte-level difference only.
func TestWriteJSEscapesLineSeparators(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJS(&buf, "CMD_TELEMETRY", map[string]any{"s": "a\u2028b\u2029c"}); err != nil {
		t.Fatalf("WriteJS: %v", err)
	}
	if !strings.Contains(buf.String(), `a\u2028b\u2029c`) {
		t.Fatalf("expected escaped line separators, got %q", buf.String())
	}
}

// encoding/json renders an integral float64 without the ".0" Python emits.
// Reports compare numerically (tests/parity/compare.py treats int and float as
// compatible), so this is accepted; the test documents the exact behaviour.
func TestWriteJSONIntegralFloatCaveat(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, map[string]any{"cost": 2.0, "zero": 0.0}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	want := "{\n \"cost\": 2,\n \"zero\": 0\n}"
	if buf.String() != want {
		t.Fatalf("integral float encoding = %q, want %q", buf.String(), want)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["cost"] != 2.0 {
		t.Fatalf("decoded cost = %v", decoded["cost"])
	}
}

func TestWriteJSONErrorsOnUnsupportedValue(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, map[string]any{"ch": make(chan int)}); err == nil {
		t.Fatal("expected an encoding error for a channel")
	}
	if buf.Len() != 0 {
		t.Fatal("partial output was written on error")
	}
}
