// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func encodeRawZstdFrame(payload []byte) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0xFD2FB528))
	buf.WriteByte(0xA0)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(payload)))
	blockHeader := uint32(1) | (uint32(len(payload)) << 3)
	buf.WriteByte(byte(blockHeader))
	buf.WriteByte(byte(blockHeader >> 8))
	buf.WriteByte(byte(blockHeader >> 16))
	buf.Write(payload)
	return buf.Bytes()
}

func TestLoadVersionDedupAndZstd(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "sessions", "--mnt-Jad-github-lab-sila--", "20261102223821-abcd1234")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	v0Content := `{"type":"session","version":0,"id":"20261102223821-abcd1234","createdAt":1762000000000,"cwd":"/mnt/Jad/github/lab/sila"}
{"time":1762000001000,"type":"step/start","data":{"turn":1,"step":1}}
{"time":1762000003000,"type":"assistant/message","data":{"turn":1,"step":1,"message":{"id":"msg_old","source":{"model":"deepseek-chat","provider":"deepseek"}},"usage":{"inputTokens":10,"outputTokens":20,"reasoningTokens":5,"cacheReadTokens":100,"cacheWriteTokens":0,"totalTokens":130}}}
`
	if err := os.WriteFile(filepath.Join(sessDir, "session.jsonl.zstd"), encodeRawZstdFrame([]byte(v0Content)), 0o600); err != nil {
		t.Fatal(err)
	}

	v4Content := `{"type":"session","version":4,"id":"20261102223821-abcd1234","createdAt":1762000000000,"cwd":"/mnt/Jad/github/lab/sila","delegationDepth":0}
{"time":1762000000100,"type":"session/title","data":{"title":"Fix sila hooks"}}
{"time":1762000000500,"type":"request/header","data":{"turn":1,"step":1,"header":{"config":{"provider":"deepseek","model":"deepseek-reasoner","reasoningEffort":"high"}}}}
{"time":1762000001000,"type":"step/start","data":{"turn":1,"step":1}}
{"time":1762000003500,"type":"assistant/message","data":{"turn":1,"step":1,"message":{"id":"msg_1","source":{"model":"deepseek-reasoner","provider":"deepseek"}},"usage":{"inputTokens":150,"outputTokens":80,"reasoningTokens":30,"cacheReadTokens":1000,"cacheWriteTokens":50,"totalTokens":1280}}}
{"time":1762000003600,"type":"tool/call","data":{"turn":1,"step":1,"name":"bash","callId":"call_1"}}
{"time":1762000003700,"type":"tool/result","data":{"turn":1,"step":1,"error":"exit 1","message":{"toolCallId":"call_1"}}}
{"time":1762000004000,"type":"model/selection","data":{"provider":"deepseek","model":"deepseek-chat","reasoningEffort":"medium"}}
{"time":1762000005000,"type":"step/start","data":{"turn":1,"step":2}}
{"time":1762000007000,"type":"assistant/message","data":{"turn":1,"step":2,"message":{"id":"msg_2","source":{"model":"deepseek-chat","provider":"deepseek"}},"usage":{"inputTokens":200,"outputTokens":40,"reasoningTokens":0,"cacheReadTokens":1100,"cacheWriteTokens":0,"totalTokens":1340}}}
{"time":1762000007500,"type":"compaction/summary","data":{"compactionId":"c1","shadowedTokenCount":1200,"usage":{"inputTokens":1350}}}
{"time":1762000008000,"type":"step/start","data":{"turn":2,"step":1}}
{"time":1762000009000,"type":"turn/end","data":{"turn":2,"reason":{"kind":"completed"}}}
`
	if err := os.WriteFile(filepath.Join(sessDir, "session.v4.jsonl.zstd"), encodeRawZstdFrame([]byte(v4Content)), 0o600); err != nil {
		t.Fatal(err)
	}

	source := Load(Filters{Root: root, IncludeTitles: true})
	if source.Counts.Files != 1 {
		t.Fatalf("expected 1 deduplicated session file, got %d", source.Counts.Files)
	}
	if len(source.Rows) != 2 {
		t.Fatalf("expected 2 step rows, got %d", len(source.Rows))
	}
	r0 := source.Rows[0]
	if r0.Model != "deepseek-reasoner" || r0.Provider != "deepseek" {
		t.Errorf("row 0 model/provider = %s/%s", r0.Model, r0.Provider)
	}
	if r0.Inp != 150 || r0.Out != 80 || r0.Reas != 30 || r0.Visible() != 50 || r0.CacheRead != 1000 || r0.CacheWrite != 50 {
		t.Errorf("row 0 tokens mismatch: %+v", r0)
	}
	if r0.DT == nil || *r0.DT != 2.5 {
		t.Errorf("row 0 DT = %v, want 2.5", r0.DT)
	}
	if r0.NTools != 1 || r0.ToolErrors != 1 || len(r0.Tools) != 1 || r0.Tools[0] != "bash" {
		t.Errorf("row 0 tools = %+v (calls=%d errs=%d)", r0.Tools, r0.NTools, r0.ToolErrors)
	}
	if r0.ProjectPath != "/mnt/Jad/github/lab/sila" || r0.Effort != "high" {
		t.Errorf("row 0 project=%q effort=%q", r0.ProjectPath, r0.Effort)
	}
	if r0.SessionTitle == nil || *r0.SessionTitle != "Fix sila hooks" {
		t.Errorf("row 0 title = %v", r0.SessionTitle)
	}

	if source.Counts.Incomplete != 1 {
		t.Errorf("expected 1 incomplete skip for unpaired step/start, got %d", source.Counts.Incomplete)
	}
	if source.Events.Compactions != 1 || source.Events.CompactionTokens != 1350 {
		t.Errorf("compactions = %d (%d tokens)", source.Events.Compactions, source.Events.CompactionTokens)
	}
	if source.Events.ModelChanges != 1 {
		t.Errorf("modelChanges=%d", source.Events.ModelChanges)
	}
	if source.Outcomes["20261102223821-abcd1234"] != "completed" {
		t.Errorf("session outcome = %q", source.Outcomes["20261102223821-abcd1234"])
	}
}

func TestLoadSkipReasonsAndProjCache(t *testing.T) {
	root := t.TempDir()
	sessDir := filepath.Join(root, "sessions", "work", "sess-2")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(root, "storages", "session_projcache", "sessions")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	projJSON := `{"identity":{"id":"sess-2","cwd":"/mnt/Jad/github/lab/telemetry","createdAt":1762000000000},"rows":{"title":{"val":"Add DSH support"},"updatedAt":{"val":1762000010000}}}`
	if err := os.WriteFile(filepath.Join(projDir, "sess-2.json"), []byte(projJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	content := `{"type":"session","version":4,"id":"sess-2","createdAt":1762000000000}
{"time":1762000001000,"type":"assistant/message","data":{"turn":1,"step":1,"message":{"id":"dup_1","source":{"model":"deepseek-chat","provider":"deepseek"}},"usage":{"inputTokens":50,"outputTokens":10}}}
{"time":1762000002000,"type":"assistant/message","data":{"turn":1,"step":1,"message":{"id":"dup_1","source":{"model":"deepseek-chat","provider":"deepseek"}},"usage":{"inputTokens":50,"outputTokens":10}}}
{"time":1762000003000,"type":"assistant/message","data":{"turn":1,"step":2,"message":{"id":"empty_1","source":{"model":"deepseek-chat","provider":"deepseek"}},"usage":{"inputTokens":0,"outputTokens":0}}}
{"type":"assistant/message","data":{"turn":1,"step":3,"message":{"id":"nots_1","source":{"model":"deepseek-chat","provider":"deepseek"}},"usage":{"inputTokens":10,"outputTokens":10}}}
`
	if err := os.WriteFile(filepath.Join(sessDir, "session.v4.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	source := Load(Filters{Root: root, IncludeTitles: true})
	if len(source.Rows) != 1 {
		t.Fatalf("expected 1 valid row, got %d", len(source.Rows))
	}
	if source.Rows[0].ProjectPath != "/mnt/Jad/github/lab/telemetry" {
		t.Errorf("expected projcache cwd fallback /mnt/Jad/github/lab/telemetry, got %q", source.Rows[0].ProjectPath)
	}
	if source.Rows[0].SessionTitle == nil || *source.Rows[0].SessionTitle != "Add DSH support" {
		t.Errorf("expected projcache title, got %v", source.Rows[0].SessionTitle)
	}
	if source.Counts.Duplicates != 1 || source.Counts.Empty != 1 || source.Counts.NoTimestamp != 1 {
		t.Errorf("unexpected skip counts: %+v", source.Counts)
	}
}

func TestResolveRootRejectsInvalidRoot(t *testing.T) {
	if _, err := ResolveRoot(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error for missing root")
	}
	filePath := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRoot(filePath); err == nil {
		t.Fatal("expected error when root is a file")
	}
}
