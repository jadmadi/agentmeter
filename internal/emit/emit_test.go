// SPDX-License-Identifier: AGPL-3.0-only

package emit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jadmadi/agentmeter/internal/emit"
	"github.com/jadmadi/agentmeter/internal/pyjson"
)

const testHTML = `<!doctype html>
<html>
<head><title>report</title></head>
<body>
<div id="boot-error">Could not load <code>data/telemetry-data.js</code>. Run the builder.</div>
<script src="data/telemetry-data.js"></script>
</body>
</html>
`

func testData() map[string]any {
	return map[string]any{
		"schemaVersion": int64(3),
		"title":         "é <tag> & \"quoted\"",
		"values":        []any{int64(1), 0.5, nil, true},
		"nested":        map[string]any{"b": int64(2), "a": []any{"x"}},
	}
}

func TestWriteReport(t *testing.T) {
	dir := t.TempDir()
	jsonPath, jsPath, htmlPath, err := emit.WriteReport(dir, testData(), []byte(testHTML), "CMD_TELEMETRY")
	if err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	if jsonPath != filepath.Join(dir, emit.JSONName) {
		t.Errorf("jsonPath = %q", jsonPath)
	}
	if jsPath != filepath.Join(dir, emit.JSName) {
		t.Errorf("jsPath = %q", jsPath)
	}
	if htmlPath != filepath.Join(dir, emit.HTMLName) {
		t.Errorf("htmlPath = %q", htmlPath)
	}

	jsonBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	wantJSON, err := pyjson.JSONBytes(testData())
	if err != nil {
		t.Fatalf("JSONBytes: %v", err)
	}
	if string(jsonBytes) != string(wantJSON) {
		t.Fatalf("telemetry-data.json mismatch\n got: %q\nwant: %q", jsonBytes, wantJSON)
	}
	if strings.HasSuffix(string(jsonBytes), "\n") {
		t.Error("telemetry-data.json has a trailing newline")
	}
	if !strings.Contains(string(jsonBytes), "<tag>") {
		t.Error("telemetry-data.json HTML-escaped the payload")
	}
	if !strings.Contains(string(jsonBytes), "é") {
		t.Error("telemetry-data.json escaped non-ASCII")
	}
	if !strings.Contains(string(jsonBytes), "\n \"schemaVersion\": 3,\n") {
		t.Errorf("telemetry-data.json is not one-space indented:\n%s", jsonBytes)
	}

	jsBytes, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("read js: %v", err)
	}
	wantJS, err := pyjson.JSBytes("CMD_TELEMETRY", testData())
	if err != nil {
		t.Fatalf("JSBytes: %v", err)
	}
	if string(jsBytes) != string(wantJS) {
		t.Fatalf("telemetry-data.js mismatch\n got: %q\nwant: %q", jsBytes, wantJS)
	}
	if !strings.HasPrefix(string(jsBytes), "window.CMD_TELEMETRY = {") {
		t.Errorf("JS prefix is wrong: %q", string(jsBytes[:40]))
	}
	if !strings.HasSuffix(string(jsBytes), ";\n") {
		t.Error("JS terminator is wrong")
	}

	htmlBytes, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read html: %v", err)
	}
	htmlText := string(htmlBytes)
	if !strings.Contains(htmlText, `<script src="telemetry-data.js"></script>`) {
		t.Errorf("script source not rewritten:\n%s", htmlText)
	}
	if strings.Contains(htmlText, `src="data/telemetry-data.js"`) {
		t.Errorf("old script source still present:\n%s", htmlText)
	}
	if !strings.Contains(htmlText, "<code>data/telemetry-data.js</code>") {
		t.Errorf("unrelated data path was rewritten:\n%s", htmlText)
	}
}

func TestWriteReportCreatesDirectories(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b", "c")
	if _, _, _, err := emit.WriteReport(dir, testData(), []byte(testHTML), "OC_TELEMETRY"); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	for _, name := range []string{emit.JSONName, emit.JSName, emit.HTMLName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", name)
		}
	}
}

func TestWriteReportOverwritesAndLeavesNoTemps(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, err := emit.WriteReport(dir, testData(), []byte(testHTML), "CMD_TELEMETRY"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	second := map[string]any{"schemaVersion": int64(3), "title": "second"}
	jsonPath, _, _, err := emit.WriteReport(dir, second, []byte(testHTML), "CMD_TELEMETRY")
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	jsonBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	if !strings.Contains(string(jsonBytes), `"second"`) {
		t.Fatalf("second write did not replace the report:\n%s", jsonBytes)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestWriteReportRejectsBadInputs(t *testing.T) {
	if _, _, _, err := emit.WriteReport("", testData(), []byte(testHTML), "CMD_TELEMETRY"); err == nil {
		t.Error("empty outDir accepted")
	}
	dir := t.TempDir()
	if _, _, _, err := emit.WriteReport(dir, testData(), nil, "CMD_TELEMETRY"); err == nil {
		t.Error("nil html accepted")
	}
	if _, _, _, err := emit.WriteReport(dir, testData(), []byte(testHTML), ""); err == nil {
		t.Error("empty global name accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("failed writes left files behind: %v", entries)
	}
}

func TestRewriteHTML(t *testing.T) {
	input := []byte(testHTML)
	got := string(emit.RewriteHTML(input))
	if strings.Count(got, `src="telemetry-data.js"`) != 1 {
		t.Fatalf("rewrite count wrong:\n%s", got)
	}
	if !strings.Contains(got, `Could not load <code>data/telemetry-data.js</code>`) {
		t.Fatalf("rewrite touched the help text:\n%s", got)
	}
	// Idempotent: a second pass changes nothing.
	if twice := string(emit.RewriteHTML([]byte(got))); twice != got {
		t.Fatal("RewriteHTML is not idempotent")
	}
}
