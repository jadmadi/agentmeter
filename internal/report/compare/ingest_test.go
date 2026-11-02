// SPDX-License-Identifier: AGPL-3.0-only

package compare

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	shared "github.com/jadmadi/agentmeter/internal/compare"
)

func writeToolReport(t *testing.T, data map[string]any, meta map[string]any) string {
	t.Helper()
	reportMeta := data["meta"].(map[string]any)
	for key, value := range meta {
		reportMeta[key] = value
	}
	raw, err := json.MarshalIndent(data, "", " ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	path := filepath.Join(t.TempDir(), "telemetry-data.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	return path
}

func TestLoadRejectsWrongReportTool(t *testing.T) {
	cases := []struct {
		name   string
		flag   string
		marker map[string]any
		want   string
	}{
		{"cmd passed to oc", shared.ToolOC, map[string]any{"counts": map[string]any{"assistant": int64(1)}}, "--oc points at a Command Code report; pass it to --cmd"},
		{"muse passed to cmd", shared.ToolCMD, map[string]any{"counts": map[string]any{"subagents": int64(1)}}, "--cmd points at a Muse report; pass it to --muse"},
		{"oc passed to cmd", shared.ToolCMD, map[string]any{"source": "OpenCode SQLite"}, "--cmd points at an OpenCode report; pass it to --oc"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, data := syntheticReport(t, shared.ToolCMD)
			path := writeToolReport(t, data, test.marker)
			_, err := Load(shared.Input{Tool: test.flag, Kind: shared.KindReport, Path: path})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadAcceptsMatchingReportTool(t *testing.T) {
	_, cmdData := syntheticReport(t, shared.ToolCMD)
	path := writeToolReport(t, cmdData, map[string]any{"counts": map[string]any{"assistant": int64(1)}})
	if _, err := Load(shared.Input{Tool: shared.ToolCMD, Kind: shared.KindReport, Path: path}); err != nil {
		t.Fatalf("Load matching report: %v", err)
	}
}

func TestLoadSkipsUnknownReportShape(t *testing.T) {
	_, cmdData := syntheticReport(t, shared.ToolCMD)
	path := writeToolReport(t, cmdData, nil)
	if _, err := Load(shared.Input{Tool: shared.ToolCMD, Kind: shared.KindReport, Path: path}); err != nil {
		t.Fatalf("Load unknown shape: %v", err)
	}
}
