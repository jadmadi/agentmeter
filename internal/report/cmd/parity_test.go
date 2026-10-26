// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jadmadi/agentmeter/internal/emit"
	sourcecmd "github.com/jadmadi/agentmeter/internal/source/cmd"
)

// These tests use the Python implementation as an independent oracle. They
// skip when python3 (or numpy, for the audit) is unavailable so the Go test
// suite still runs standalone.

func pythonBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; skipping Python oracle test")
	}
	return path
}

func pythonHasNumpy(t *testing.T, python string) bool {
	t.Helper()
	return exec.Command(python, "-c", "import numpy").Run() == nil
}

func runPython(t *testing.T, python, dir string, args ...string) {
	t.Helper()
	command := exec.Command(python, args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "TZ=UTC")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("python %v failed: %v\n%s", args, err, output)
	}
}

// TestPythonBuilderParity builds the fixture with both implementations and
// compares the parsed reports. Only meta.generatedAt and the two documented
// fixture normalizations differ.
func TestPythonBuilderParity(t *testing.T) {
	python := pythonBinary(t)
	root := fixtureRoot(t)
	toolDir := repoPath(t, "command-code-telemetry", "py")
	script := filepath.Join(toolDir, "telemetry.py")

	type mode struct {
		name       string
		pythonArgs []string
		options    Options
	}
	modes := []mode{
		{name: "default", options: Options{IncludeTitles: true}},
		{name: "models", pythonArgs: []string{"--models", "slow"}, options: Options{IncludeTitles: true, ModelFilters: []string{"slow"}}},
		{name: "dates", pythonArgs: []string{"--since", "2026-09-01", "--until", "2026-09-04"}, options: Options{IncludeTitles: true, Since: "2026-09-01", Until: "2026-09-04"}},
		{name: "dates-narrow", pythonArgs: []string{"--since", "2026-09-02", "--until", "2026-09-04"}, options: Options{IncludeTitles: true, Since: "2026-09-02", Until: "2026-09-04"}},
		{name: "no-titles", pythonArgs: []string{"--no-titles"}, options: Options{IncludeTitles: false}},
		{name: "full-paths", pythonArgs: []string{"--full-paths"}, options: Options{IncludeTitles: true, FullPaths: true}},
	}
	for _, testMode := range modes {
		t.Run(testMode.name, func(t *testing.T) {
			outDir := t.TempDir()
			args := append([]string{script, "--root", root, "--out", outDir}, testMode.pythonArgs...)
			runPython(t, python, toolDir, args...)
			expected, err := readJSONFile(filepath.Join(outDir, "telemetry-data.json"))
			if err != nil {
				t.Fatalf("read python report: %v", err)
			}
			options := testMode.options
			options.Root = root
			actual := roundTrip(t, buildFixture(t, options))
			delete(expected["meta"].(map[string]any), "generatedAt")
			delete(actual["meta"].(map[string]any), "generatedAt")
			var diffs []string
			compareValues("report", expected, actual, &diffs)
			if len(diffs) > 0 {
				limit := len(diffs)
				if limit > 20 {
					limit = 20
				}
				for _, diff := range diffs[:limit] {
					t.Error(diff)
				}
				t.Fatalf("%s: %d differences against the Python builder", testMode.name, len(diffs))
			}
		})
	}
}

// TestDateWindowExcludesOutOfWindowLedger verifies that a date window whose
// kept rows exclude an earlier date with ledger entries filters out the
// out-of-window dated ledger rows so the spend invariant holds.
func TestDateWindowExcludesOutOfWindowLedger(t *testing.T) {
	root := fixtureRoot(t)
	source := sourcecmd.Load(sourcecmd.Filters{
		Root: root, IncludeTitles: true, Since: "2026-09-02", Until: "2026-09-04",
	})
	report, err := Build(source, Options{
		Root: root, IncludeTitles: true, Since: "2026-09-02", Until: "2026-09-04",
	})
	if err != nil {
		t.Fatalf("unexpected invariant failure on date window: %v", err)
	}
	if report["totals"].(map[string]any)["errorCostUsd"] == nil {
		t.Fatal("missing totals.errorCostUsd")
	}
}

// TestPythonAuditOnGoOutput runs the independent Python audit against reports
// written by the Go builder across all filter and option modes.
func TestPythonAuditOnGoOutput(t *testing.T) {
	python := pythonBinary(t)
	if !pythonHasNumpy(t, python) {
		t.Skip("numpy not available; skipping audit oracle test")
	}
	root := fixtureRoot(t)
	toolDir := repoPath(t, "command-code-telemetry", "py")
	modes := []struct {
		name    string
		options Options
	}{
		{name: "default", options: Options{Root: root, IncludeTitles: true}},
		{name: "models", options: Options{Root: root, IncludeTitles: true, ModelFilters: []string{"slow"}}},
		{name: "dates", options: Options{Root: root, IncludeTitles: true, Since: "2026-09-01", Until: "2026-09-04"}},
		{name: "dates-narrow", options: Options{Root: root, IncludeTitles: true, Since: "2026-09-02", Until: "2026-09-04"}},
		{name: "no-titles", options: Options{Root: root, IncludeTitles: false}},
		{name: "full-paths", options: Options{Root: root, IncludeTitles: true, FullPaths: true}},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			outDir := t.TempDir()
			report := buildFixture(t, mode.options)
			jsonPath, jsPath, htmlPath, err := emit.WriteReport(outDir, report, []byte("<html></html>"), "CMD_TELEMETRY")
			if err != nil {
				t.Fatalf("WriteReport: %v", err)
			}
			if _, err := os.Stat(htmlPath); err != nil {
				t.Fatalf("html not written: %v", err)
			}
			bundle, err := os.ReadFile(jsPath)
			if err != nil {
				t.Fatalf("read js: %v", err)
			}
			if !strings.HasPrefix(string(bundle), "window.CMD_TELEMETRY = ") {
				t.Fatalf("js prefix = %q", string(bundle[:40]))
			}
			command := exec.Command(python, filepath.Join(toolDir, "audit.py"), "--root", root, "--data", jsonPath)
			command.Dir = toolDir
			command.Env = append(os.Environ(), "TZ=UTC")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("python audit failed: %v\n%s", err, tail(string(output), 60))
			}
			if !strings.Contains(string(output), "ALL CHECKS PASSED") {
				t.Fatalf("audit did not report success:\n%s", tail(string(output), 20))
			}
		})
	}
}

func readJSONFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return data, nil
}

func tail(text string, lines int) string {
	parts := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}
