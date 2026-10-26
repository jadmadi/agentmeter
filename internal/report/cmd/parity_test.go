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
		name        string
		pythonArgs  []string
		options     Options
		ignorePaths map[string]bool
	}
	// Python bug: build() rebinds error_cost in the daily/hourly loops, so
	// totals.errorCostUsd is the last hourly bucket instead of the global
	// error cost. The independent audit enforces the global value, so the Go
	// builder emits that and the comparison excludes this one field.
	ignore := map[string]bool{"report.totals.errorCostUsd": true}
	modes := []mode{
		{name: "default", options: Options{IncludeTitles: true}, ignorePaths: ignore},
		{name: "models", pythonArgs: []string{"--models", "slow"}, options: Options{IncludeTitles: true, ModelFilters: []string{"slow"}}, ignorePaths: ignore},
		{name: "dates", pythonArgs: []string{"--since", "2026-09-01", "--until", "2026-09-04"}, options: Options{IncludeTitles: true, Since: "2026-09-01", Until: "2026-09-04"}, ignorePaths: ignore},
		{name: "no-titles", pythonArgs: []string{"--no-titles"}, options: Options{IncludeTitles: false}, ignorePaths: ignore},
		{name: "full-paths", pythonArgs: []string{"--full-paths"}, options: Options{IncludeTitles: true, FullPaths: true}, ignorePaths: ignore},
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
			filtered := diffs[:0]
			for _, diff := range diffs {
				path := strings.SplitN(diff, ":", 2)[0]
				if testMode.ignorePaths[path] {
					continue
				}
				filtered = append(filtered, diff)
			}
			if len(filtered) > 0 {
				limit := len(filtered)
				if limit > 20 {
					limit = 20
				}
				for _, diff := range filtered[:limit] {
					t.Error(diff)
				}
				t.Fatalf("%s: %d differences against the Python builder", testMode.name, len(filtered))
			}
		})
	}
}

// TestUnrepresentableDateWindowFailsClosed pins the known oracle
// inconsistency documented in the port plan: a date window whose kept rows
// exclude a date that still has ledger entries cannot satisfy
// "sum(daily.totalSpendUsd) + undated error cost == totals.totalSpendUsd".
// Both builders fail closed instead of writing a broken report.
func TestUnrepresentableDateWindowFailsClosed(t *testing.T) {
	root := fixtureRoot(t)
	source := sourcecmd.Load(sourcecmd.Filters{
		Root: root, IncludeTitles: true, Since: "2026-09-02", Until: "2026-09-04",
	})
	_, err := Build(source, Options{
		Root: root, IncludeTitles: true, Since: "2026-09-02", Until: "2026-09-04",
	})
	if err == nil {
		t.Fatal("expected the invariant failure to be reported")
	}
	if !strings.Contains(err.Error(), "sum(daily.totalSpendUsd)") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPythonAuditOnGoOutput runs the independent Python audit against a report
// written by the Go builder. This is the release-blocking oracle check.
func TestPythonAuditOnGoOutput(t *testing.T) {
	python := pythonBinary(t)
	if !pythonHasNumpy(t, python) {
		t.Skip("numpy not available; skipping audit oracle test")
	}
	root := fixtureRoot(t)
	toolDir := repoPath(t, "command-code-telemetry", "py")
	outDir := t.TempDir()
	report := buildFixture(t, Options{Root: root, IncludeTitles: true})
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
