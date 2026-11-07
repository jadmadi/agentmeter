// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jadmadi/agentmeter/internal/contract"
	sourcedsh "github.com/jadmadi/agentmeter/internal/source/dsh"
)

func TestBuildValidatesDshContract(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "sessions", "--mnt-Jad-github-lab-telemetry--", "20261102223821-abcd1234")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"session","version":4,"id":"20261102223821-abcd1234","createdAt":1762000000000,"cwd":"/mnt/Jad/github/lab/telemetry","delegationDepth":0}
{"time":1762000000100,"type":"session/title","data":{"title":"Build DSH telemetry"}}
{"time":1762000000500,"type":"request/header","data":{"turn":1,"step":1,"header":{"config":{"provider":"deepseek","model":"deepseek-reasoner","reasoningEffort":"high"}}}}
{"time":1762000001000,"type":"step/start","data":{"turn":1,"step":1}}
{"time":1762000003500,"type":"assistant/message","data":{"turn":1,"step":1,"message":{"id":"msg_1","source":{"model":"deepseek-reasoner","provider":"deepseek"}},"usage":{"inputTokens":200,"outputTokens":100,"reasoningTokens":40,"cacheReadTokens":1000,"cacheWriteTokens":50,"totalTokens":1350}}}
{"time":1762000003600,"type":"tool/call","data":{"turn":1,"step":1,"name":"bash","callId":"call_1"}}
{"time":1762000003700,"type":"tool/result","data":{"turn":1,"step":1,"error":"exit 1","message":{"toolCallId":"call_1"}}}
{"time":1762000004000,"type":"model/selection","data":{"provider":"deepseek","model":"deepseek-chat","reasoningEffort":"medium"}}
{"time":1762000005000,"type":"step/start","data":{"turn":1,"step":2}}
{"time":1762000007000,"type":"assistant/message","data":{"turn":1,"step":2,"message":{"id":"msg_2","source":{"model":"deepseek-chat","provider":"deepseek"}},"usage":{"inputTokens":300,"outputTokens":60,"reasoningTokens":0,"cacheReadTokens":1200,"cacheWriteTokens":0,"totalTokens":1560}}}
{"time":1762000007500,"type":"compaction/summary","data":{"compactionId":"c1","shadowedTokenCount":1400,"usage":{"inputTokens":1500}}}
{"time":1762000008000,"type":"step/start","data":{"turn":1,"step":3}}
{"time":1762000010000,"type":"assistant/message","data":{"turn":1,"step":3,"message":{"id":"msg_3","source":{"model":"deepseek-chat","provider":"deepseek"}},"usage":{"inputTokens":250,"outputTokens":80,"reasoningTokens":0,"cacheReadTokens":600,"cacheWriteTokens":0,"totalTokens":930}}}
{"time":1762000011000,"type":"turn/end","data":{"turn":1,"reason":{"kind":"completed"}}}
`
	if err := os.WriteFile(filepath.Join(sessDir, "session.v4.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	source := sourcedsh.Load(sourcedsh.Filters{
		Root:          root,
		IncludeTitles: true,
	})
	report, err := Build(source, Options{
		Root:          root,
		IncludeTitles: true,
		Now:           time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if issues := contract.ValidateDsh(report); len(issues) != 0 {
		t.Fatalf("ValidateDsh issues: %v", issues)
	}

	totals := report["totals"].(map[string]any)
	if totals["steps"] != int64(3) {
		t.Errorf("steps = %v, want 3", totals["steps"])
	}
	if totals["generatedTokens"] != int64(240) || totals["reasoningTokens"] != int64(40) || totals["visibleOutputTokens"] != int64(200) {
		t.Errorf("output totals = %+v", totals)
	}
	// Fresh input is 200+300+250 = 750; cacheRead is 2800; cacheWrite is 50; inputTotal is 3600.
	if totals["freshInputTokens"] != int64(750) || totals["inputTotal"] != int64(3600) {
		t.Errorf("input totals = fresh %v total %v, want 750 / 3600", totals["freshInputTokens"], totals["inputTotal"])
	}
	byModel := report["byModel"].([]any)
	for _, m := range byModel {
		row := m.(map[string]any)
		if row["inputIncludesCache"] != false {
			t.Errorf("model %v inputIncludesCache = %v, want false", row["model"], row["inputIncludesCache"])
		}
	}
	compaction := report["compactionImpact"].(map[string]any)
	if compaction["events"] != int64(1) || compaction["paired"] != int64(1) {
		t.Errorf("compactionImpact = %+v", compaction)
	}
}
