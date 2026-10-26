// SPDX-License-Identifier: AGPL-3.0-only

package contract_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jadmadi/agentmeter/internal/contract"
)

// loadFixture decodes a report twice: with the default float64 numbers and
// with json.Number, so the validator is exercised on both representations.
func loadFixture(t *testing.T, name string) (floats map[string]any, exact map[string]any) {
	t.Helper()
	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, &floats); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&exact); err != nil {
		t.Fatalf("decode %s with UseNumber: %v", path, err)
	}
	return floats, exact
}

func TestEmptyCmdReportValidates(t *testing.T) {
	floats, exact := loadFixture(t, "empty-cmd-report.json")

	for name, data := range map[string]map[string]any{"float64": floats, "json.Number": exact} {
		if issues := contract.ValidateCommonIssues(data); len(issues) != 0 {
			t.Fatalf("%s: empty cmd report failed ValidateCommon:\n%v", name, issues.Strings())
		}
		if issues := contract.ValidateCmdIssues(data); len(issues) != 0 {
			t.Fatalf("%s: empty cmd report failed ValidateCmd:\n%v", name, issues.Strings())
		}
	}

	assertEmptyReportShape(t, floats)
	if got := floats["meta"].(map[string]any)["timezone"]; got != "UTC+00:00" {
		t.Fatalf("empty cmd timezone = %v", got)
	}
}

func TestEmptyOCReportValidates(t *testing.T) {
	floats, exact := loadFixture(t, "empty-oc-report.json")

	for name, data := range map[string]map[string]any{"float64": floats, "json.Number": exact} {
		if issues := contract.ValidateCommonIssues(data); len(issues) != 0 {
			t.Fatalf("%s: empty oc report failed ValidateCommon:\n%v", name, issues.Strings())
		}
		if issues := contract.ValidateOCIssues(data); len(issues) != 0 {
			t.Fatalf("%s: empty oc report failed ValidateOC:\n%v", name, issues.Strings())
		}
	}

	assertEmptyReportShape(t, floats)
	if got := floats["meta"].(map[string]any)["timezone"]; got != "UTC+00:00" {
		t.Fatalf("empty oc timezone = %v", got)
	}
}

func assertEmptyReportShape(t *testing.T, data map[string]any) {
	t.Helper()
	if got := data["schemaVersion"]; got != float64(contract.SchemaVersion) {
		t.Fatalf("schemaVersion = %v", got)
	}
	totals, ok := data["totals"].(map[string]any)
	if !ok {
		t.Fatal("totals is not an object")
	}
	for _, key := range []string{
		"steps", "generatedTokens", "freshInputTokens", "inputTotal",
		"cacheReadTokens", "cacheWriteTokens", "costUsd", "totalSpendUsd",
	} {
		if totals[key] != float64(0) {
			t.Fatalf("totals.%s = %v, want 0", key, totals[key])
		}
	}
	for _, section := range []string{"byModel", "sessions", "projects", "tools", "daily", "steps"} {
		list, ok := data[section].([]any)
		if !ok {
			t.Fatalf("%s is not an array", section)
		}
		if len(list) != 0 {
			t.Fatalf("%s has %d entries, want 0", section, len(list))
		}
	}
	hourly, ok := data["hourly"].([]any)
	if !ok || len(hourly) != 24 {
		t.Fatalf("hourly has %d rows, want 24", len(hourly))
	}
	for i, raw := range hourly {
		row := raw.(map[string]any)
		if row["nAll"] != float64(0) || row["totalSpendUsd"] != float64(0) {
			t.Fatalf("hourly[%d] not zeroed: %v", i, row)
		}
	}
	timedAll, ok := data["timed"].(map[string]any)["all"].(map[string]any)
	if !ok {
		t.Fatal("timed.all is not an object")
	}
	if timedAll["n"] != float64(0) {
		t.Fatalf("timed.all.n = %v", timedAll["n"])
	}
}
