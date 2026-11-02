// SPDX-License-Identifier: AGPL-3.0-only

package cmdtelemetry

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

func fixtureRoot(t *testing.T) string {
	t.Helper()
	base, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo: %v", err)
	}
	root := filepath.Join(base, "command-code-telemetry", "testdata", "fixture")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("fixture missing at %s: %v", root, err)
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
	if !strings.Contains(output, "SOURCE") || !strings.Contains(output, "8 files") {
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
	if totals["steps"].(float64) != 54 {
		t.Errorf("steps = %v, want 54", totals["steps"])
	}

	bundle, err := os.ReadFile(filepath.Join(outDir, "telemetry-data.js"))
	if err != nil {
		t.Fatalf("read js: %v", err)
	}
	const prefix = "window.CMD_TELEMETRY = "
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
	} else if !strings.Contains(output, "usage: agentmeter cmd") {
		t.Errorf("--help stdout = %q", output)
	}
	// Privacy flags reach the report.
	outDir := filepath.Join(t.TempDir(), "out")
	if code, _ := captured("--root", root, "--out", outDir, "--no-titles", "--full-paths", "--models", "slow", "--since", "2026-09-01"); code != 0 {
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
	if len(filters) != 1 || filters[0] != "slow" {
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
