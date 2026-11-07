// SPDX-License-Identifier: AGPL-3.0-only

package dshtelemetry

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Reports embed the local UTC offset in meta.timezone; pin it so the
	// generated output is deterministic.
	time.Local = time.UTC
	os.Exit(m.Run())
}

func zstdRawFrame(payload []byte) []byte {
	n := len(payload)
	blockHeader := uint32(1) | (uint32(0) << 1) | (uint32(n) << 3)
	out := make([]byte, 0, 4+1+4+3+n)
	out = append(out, 0x28, 0xb5, 0x2f, 0xfd)
	out = append(out, 0xa0)
	out = append(out, byte(n), byte(n>>8), byte(n>>16), byte(n>>24))
	out = append(out, byte(blockHeader), byte(blockHeader>>8), byte(blockHeader>>16))
	out = append(out, payload...)
	return out
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	base, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo: %v", err)
	}
	root := filepath.Join(base, "dsh-telemetry", "testdata", "fixture")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("fixture missing at %s: %v", root, err)
	}
	alphaDir := filepath.Join(root, "sessions", "--work-alpha--", "20260910-aaaa1111")
	v4Path := filepath.Join(alphaDir, "session.v4.jsonl.zstd")
	v4Lines := strings.Join([]string{
		`{"type":"session","version":4,"id":"20260910-aaaa1111","createdAt":"2026-09-10T10:00:00Z","cwd":"/work/alpha","delegationDepth":0}`,
		`{"time":"2026-09-10T10:00:00Z","type":"session/title","data":{"title":"Alpha session"}}`,
		`{"time":"2026-09-10T10:00:00Z","type":"request/header","data":{"turn":1,"step":1,"header":{"config":{"provider":"deepseek","model":"deepseek-reasoner","reasoningEffort":"high"}}}}`,
		`{"time":"2026-09-10T10:00:00Z","type":"model/selection","data":{"provider":"deepseek","model":"deepseek-reasoner","reasoningEffort":"high"}}`,
		`{"time":"2026-09-10T10:00:00Z","type":"step/start","data":{"turn":1,"step":1}}`,
		`{"time":"2026-09-10T10:00:20Z","type":"assistant/message","data":{"turn":1,"step":1,"message":{"id":"msg_a1","source":{"model":"deepseek-reasoner","provider":"deepseek"}},"usage":{"inputTokens":800,"outputTokens":400,"reasoningTokens":100,"cacheReadTokens":5000,"cacheWriteTokens":200,"totalTokens":6400}}}`,
		`{"time":"2026-09-10T10:00:20Z","type":"tool/call","data":{"turn":1,"step":1,"name":"bash","callId":"c1"}}`,
		`{"time":"2026-09-10T10:00:21Z","type":"tool/result","data":{"turn":1,"step":1,"message":{"toolCallId":"c1"}}}`,
		`{"time":"2026-09-10T10:00:25Z","type":"step/start","data":{"turn":1,"step":2}}`,
		`{"time":"2026-09-10T10:00:45Z","type":"assistant/message","data":{"turn":1,"step":2,"message":{"id":"msg_a2","source":{"model":"deepseek-reasoner","provider":"deepseek"}},"usage":{"inputTokens":1000,"outputTokens":600,"reasoningTokens":150,"cacheReadTokens":8000,"cacheWriteTokens":0,"totalTokens":9600}}}`,
		`{"time":"2026-09-10T10:00:50Z","type":"compaction/summary","data":{"compactionId":"comp_1","shadowedTokenCount":7600,"usage":{"inputTokens":9600}}}`,
		`{"time":"2026-09-10T10:01:00Z","type":"turn/end","data":{"turn":1,"reason":{"kind":"completed"}}}`,
	}, "\n") + "\n"
	wantV4 := zstdRawFrame([]byte(v4Lines))
	if got, err := os.ReadFile(v4Path); err != nil || string(got) != string(wantV4) {
		if err := os.WriteFile(v4Path, wantV4, 0o644); err != nil {
			t.Fatalf("write %s: %v", v4Path, err)
		}
	}
	v0Path := filepath.Join(alphaDir, "session.jsonl.zstd")
	v0Lines := `{"type":"session","version":0,"id":"20260910-aaaa1111","createdAt":"2026-09-10T10:00:00Z","cwd":"/work/alpha"}` + "\n" +
		`{"time":"2026-09-10T10:00:01Z","type":"assistant/message","data":{"turn":1,"step":1,"message":{"id":"msg_old","source":{"model":"stale-model","provider":"deepseek"}},"usage":{"inputTokens":99999,"outputTokens":99999}}}` + "\n"
	wantV0 := zstdRawFrame([]byte(v0Lines))
	if got, err := os.ReadFile(v0Path); err != nil || string(got) != string(wantV0) {
		if err := os.WriteFile(v0Path, wantV0, 0o644); err != nil {
			t.Fatalf("write %s: %v", v0Path, err)
		}
	}
	return root
}

// captureStdout runs fn with os.Stdout redirected so CLI output does not spam
// the test log, and returns what was printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	original := os.Stdout
	os.Stdout = writer
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- string(data)
	}()
	fn()
	os.Stdout = original
	writer.Close()
	output := <-done
	reader.Close()
	return output
}

func TestRunWritesReport(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "out")
	var code int
	output := captureStdout(t, func() {
		code = Run([]string{"--root", fixtureRoot(t), "--out", outDir})
	})
	if code != 0 {
		t.Fatalf("Run exit = %d", code)
	}
	if !strings.Contains(output, "SOURCE") || !strings.Contains(output, "3 files") {
		t.Errorf("summary missing source line:\n%s", output)
	}
	if !strings.Contains(output, "ROOT") || !strings.Contains(output, fixtureRoot(t)) {
		t.Errorf("summary missing resolved root:\n%s", output)
	}
	if !strings.Contains(output, filepath.Join(outDir, "telemetry-data.json")) {
		t.Errorf("summary missing json path:\n%s", output)
	}
	if !strings.Contains(output, "Open ") {
		t.Errorf("summary missing open hint:\n%s", output)
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "telemetry-data.json"))
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("parse json: %v", err)
	}
	if data["schemaVersion"].(float64) != 3 {
		t.Errorf("schemaVersion = %v", data["schemaVersion"])
	}
	totals := data["totals"].(map[string]any)
	if totals["steps"].(float64) != 5 {
		t.Errorf("steps = %v, want 5", totals["steps"])
	}

	bundle, err := os.ReadFile(filepath.Join(outDir, "telemetry-data.js"))
	if err != nil {
		t.Fatalf("read js: %v", err)
	}
	const prefix = "window.DSH_TELEMETRY = "
	if !strings.HasPrefix(string(bundle), prefix) {
		t.Fatalf("js prefix = %q", string(bundle[:40]))
	}
	if !strings.HasSuffix(string(bundle), ";\n") {
		t.Errorf("js bundle does not end with \";\\n\"")
	}
	var jsData map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(string(bundle), prefix), ";\n")), &jsData); err != nil {
		t.Fatalf("parse js payload: %v", err)
	}
	if !deepEqual(data, jsData) {
		t.Error("js payload differs from the json report")
	}

	page, err := os.ReadFile(filepath.Join(outDir, "index.html"))
	if err != nil {
		t.Fatalf("read html: %v", err)
	}
	if !strings.Contains(string(page), `src="telemetry-data.js"`) {
		t.Error("html does not reference the local data bundle")
	}
	if strings.Contains(string(page), `src="data/telemetry-data.js"`) {
		t.Error("html kept the tool-relative data path")
	}
}

func TestRunEmptyRootFails(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "out")
	root := t.TempDir()
	var code int
	captureStdout(t, func() { code = Run([]string{"--root", root, "--out", outDir}) })
	if code != 1 {
		t.Fatalf("Run exit = %d, want 1", code)
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("empty root wrote output: %v", err)
	}
}

func TestRunMissingRootFails(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "out")
	root := filepath.Join(t.TempDir(), "missing")
	var code int
	captureStdout(t, func() { code = Run([]string{"--root", root, "--out", outDir}) })
	if code != 1 {
		t.Fatalf("Run exit = %d, want 1", code)
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Errorf("missing root wrote output: %v", err)
	}
}

func TestRunFileRootFails(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "out")
	root := filepath.Join(t.TempDir(), "root.jsonl")
	if err := os.WriteFile(root, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	captureStdout(t, func() { code = Run([]string{"--root", root, "--out", outDir}) })
	if code != 1 {
		t.Fatalf("Run exit = %d, want 1", code)
	}
}

func TestRunFilteredEmptyRootSucceeds(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "out")
	var code int
	output := captureStdout(t, func() {
		code = Run([]string{"--root", fixtureRoot(t), "--models", "no-such-model", "--out", outDir})
	})
	if code != 0 {
		t.Fatalf("Run exit = %d, want 0", code)
	}
	if !strings.Contains(output, "FILTER") || !strings.Contains(output, "no-such-model") {
		t.Errorf("summary missing FILTER line:\n%s", output)
	}
	data, err := readReport(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if data["totals"].(map[string]any)["steps"].(float64) != 0 {
		t.Errorf("steps = %v", data["totals"].(map[string]any)["steps"])
	}
}

func TestRunJSONOutput(t *testing.T) {
	outDir := filepath.Join(t.TempDir(), "out")
	var code int
	output := captureStdout(t, func() {
		code = Run([]string{"--root", fixtureRoot(t), "--out", outDir, "--json"})
	})
	if code != 0 {
		t.Fatalf("Run exit = %d", code)
	}
	if !json.Valid([]byte(output)) {
		t.Fatalf("stdout is not valid JSON:\n%s", output)
	}
	if strings.Contains(output, "AgentMeter") || strings.Contains(output, "Open ") {
		t.Errorf("stdout contains human summary:\n%s", output)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(output), &data); err != nil {
		t.Fatal(err)
	}
	if data["schemaVersion"].(float64) != 3 {
		t.Errorf("schemaVersion = %v", data["schemaVersion"])
	}
	if _, err := os.Stat(filepath.Join(outDir, "telemetry-data.json")); err != nil {
		t.Errorf("report file missing: %v", err)
	}
}

func TestRunFlagsAndErrors(t *testing.T) {
	root := fixtureRoot(t)
	captured := func(args ...string) (int, string) {
		code := -1
		output := captureStdout(t, func() { code = Run(args) })
		return code, output
	}
	// Unknown flag: exit 2 with usage.
	if code, _ := captured("--nope"); code != 2 {
		t.Errorf("unknown flag exit = %d, want 2", code)
	}
	// Unexpected positional argument: exit 2.
	if code, _ := captured("--root", root, "extra"); code != 2 {
		t.Errorf("extra arg exit = %d, want 2", code)
	}
	// Invalid dates are usage errors before any scan.
	if code, _ := captured("--since", "2026-13-45"); code != 2 {
		t.Errorf("bad --since exit = %d, want 2", code)
	}
	if code, _ := captured("--since", "2026-05-01", "--until", "2026-01-01"); code != 2 {
		t.Errorf("inverted window exit = %d, want 2", code)
	}
	// --help returns success and writes the page to stdout.
	if code, output := captured("--help"); code != 0 {
		t.Errorf("--help exit = %d, want 0", code)
	} else if !strings.Contains(output, "usage: agentmeter dsh") {
		t.Errorf("--help stdout = %q", output)
	}
	// Privacy flags reach the report.
	outDir := filepath.Join(t.TempDir(), "out")
	if code, _ := captured("--root", root, "--out", outDir, "--no-titles", "--full-paths", "--models", "deepseek-chat", "--since", "2026-09-01"); code != 0 {
		t.Fatalf("flag run exit = %d", code)
	}
	data, err := readReport(outDir)
	if err != nil {
		t.Fatal(err)
	}
	meta := data["meta"].(map[string]any)
	if meta["includeTitles"].(bool) || !meta["fullPaths"].(bool) {
		t.Errorf("privacy flags = %v/%v", meta["includeTitles"], meta["fullPaths"])
	}
	if meta["since"] != "2026-09-01" || meta["until"] != nil {
		t.Errorf("date flags = %v/%v", meta["since"], meta["until"])
	}
	filters := meta["modelFilters"].([]any)
	if len(filters) != 1 || filters[0] != "deepseek-chat" {
		t.Errorf("modelFilters = %v", filters)
	}
}

func TestParseModelFilters(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{" a , b ,, c ", []string{"a", "b", "c"}},
		{",", nil},
	}
	for _, testCase := range cases {
		got := parseModelFilters(testCase.input)
		if len(got) != len(testCase.want) {
			t.Errorf("parseModelFilters(%q) = %v, want %v", testCase.input, got, testCase.want)
			continue
		}
		for index := range testCase.want {
			if got[index] != testCase.want[index] {
				t.Errorf("parseModelFilters(%q)[%d] = %q, want %q", testCase.input, index, got[index], testCase.want[index])
			}
		}
	}
}

func readReport(dir string) (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "telemetry-data.json"))
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func deepEqual(left, right any) bool {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}
