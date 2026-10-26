// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Fixed clock so a golden report is reproducible.
var fixtureNow = time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)

const fixtureSchema = `
CREATE TABLE session_v2 (
    id TEXT PRIMARY KEY,
    title TEXT,
    slug TEXT,
    directory TEXT,
    parent_id TEXT,
    fork_session_id TEXT,
    version TEXT,
    time_created INTEGER
);
CREATE TABLE session_message (
    id TEXT PRIMARY KEY,
    session_id TEXT,
    type TEXT,
    time_created INTEGER,
    data TEXT
);
`

// fixtureTime is a UTC instant on 2026-01-05 + day, hour:minute:second.
func fixtureTime(day, hour, minute, second int) int64 {
	return time.Date(2026, 1, 5+day, hour, minute, second, 0, time.UTC).UnixMilli()
}

type fixturePayload struct {
	Provider   string
	Model      string
	Variant    string
	Input      int64
	Output     int64
	Reasoning  int64
	CacheRead  int64
	CacheWrite int64
	Created    int64
	Streamed   int64
	Completed  int64
	Cost       float64
	Agent      string
	Finish     string
	Blocks     []any
}

func (p fixturePayload) mapValue() map[string]any {
	model := map[string]any{"providerID": p.Provider, "id": p.Model}
	if p.Variant != "" {
		model["variant"] = p.Variant
	}
	agent := p.Agent
	if agent == "" {
		agent = "build"
	}
	finish := p.Finish
	if finish == "" {
		finish = "stop"
	}
	blocks := p.Blocks
	if blocks == nil {
		blocks = []any{map[string]any{"type": "text"}}
	}
	return map[string]any{
		"model": model,
		"tokens": map[string]any{
			"input":     p.Input,
			"output":    p.Output,
			"reasoning": p.Reasoning,
			"cache":     map[string]any{"read": p.CacheRead, "write": p.CacheWrite},
		},
		"time": map[string]any{
			"created":   p.Created,
			"streamed":  p.Streamed,
			"completed": p.Completed,
		},
		"content": blocks,
		"finish":  finish,
		"agent":   agent,
		"cost":    p.Cost,
	}
}

func fixtureTool(name string, ran, completed, created int64, status string) map[string]any {
	block := map[string]any{
		"type": "tool",
		"name": name,
		"time": map[string]any{"ran": ran, "completed": completed, "created": created},
	}
	if status != "" {
		block["state"] = map[string]any{"status": status}
	}
	return block
}

func fixtureReasoning(created int64) map[string]any {
	return map[string]any{"type": "reasoning", "time": map[string]any{"created": created}}
}

func fixtureText() map[string]any { return map[string]any{"type": "text"} }

// modelARow is the deterministic model-A step shape. The fork copy and the
// fingerprint collision are both built from it so their normalized payloads
// are identical by construction (the fork copy must deduplicate, the collision
// must only share the timing/token fingerprint).
func modelARow(index int) fixturePayload {
	created := fixtureTime(index/5, 9+index%5, index*4, 0)
	gen := int64(40 + (index*37)%400)
	reasoning := gen / 4
	output := gen - reasoning
	rate := float64(100 + (index%5)*20)
	streamed := created + int64(float64(gen)/rate*1000)
	completed := streamed + 900
	blocks := []any{fixtureReasoning(created + 120)}
	switch {
	case index%2 == 0:
		ran := streamed + 50
		done := ran + 700
		blocks = append(blocks,
			fixtureTool("read", ran, done, ran+10, ""),
			fixtureTool("bash", ran+200, done+300, ran+210, "error"))
		completed = done + 400
	case index%3 == 0:
		ran := streamed + 30
		blocks = append(blocks, fixtureTool("grep", ran, ran+500, ran+5, ""))
		completed = ran + 800
	}
	switch {
	case index%13 == 0:
		streamed = created + 150 // short duration, high rate
	case index%17 == 0:
		streamed = created + 50_000 // slowRate
	case index%19 == 0:
		streamed = created + 150 // tooShortDuration
	case index == 5:
		// Tools outlast the step tail: overhead goes negative.
		completed = streamed + 100
	}
	blocks = append(blocks, fixtureText())
	return fixturePayload{
		Provider: "alpha", Model: "model-a", Variant: variantFor(index),
		Input: 500 + int64(index)*13, Output: output, Reasoning: reasoning,
		CacheRead: int64(index%4) * 1000, CacheWrite: int64(index%2) * 200,
		Created: created, Streamed: streamed, Completed: completed,
		Cost: 0.000123 * float64(index+1), Blocks: blocks,
		Agent: agentFor(index),
	}
}

// buildFixtureDB writes the deterministic fixture store used by the report,
// parity and CLI tests. It is deliberately varied: three providers, priced and
// free models, overlapping tools, reasoning, censored timing buckets, error and
// empty ledger rows, a fork copy, a fingerprint collision, a nested subagent
// tree, compaction and model-switch events, and non-ASCII metadata.
func buildFixtureDB(t *testing.T, path string) {
	t.Helper()
	db, err := openAndSchema(path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()

	sessions := []struct {
		id        string
		title     any
		slug      any
		directory string
		parent    any
		fork      any
		version   any
	}{
		{"ses-root-a", "Root A — café ☕", "root-a", "/work/project-a", nil, nil, "1.15.0"},
		{"ses-root-b", nil, "root-b-slug", "/work/other/project-b", nil, nil, "1.14.0"},
		{"ses-dupbase", "Dup base", nil, "/work/one/project-a", nil, nil, nil},
		{"ses-sub", "Subagent", "sub", "/work/project-a", "ses-root-a", nil, "1.15.0"},
		{"ses-child", "Nested subagent", nil, "/work/project-a", "ses-sub", nil, "1.15.0"},
		{"ses-fork", "Forked session", "fork", "/work/project-a", nil, "ses-root-a", "1.15.0"},
		{"ses-collision", "Collision session", nil, "/work/project-a", nil, nil, "1.15.0"},
		{"ses-ledger", "Ledger only", nil, "/work/ledger", nil, nil, "1.0.0"},
	}
	for i, session := range sessions {
		if _, err := db.Exec(
			"INSERT INTO session_v2 (id, title, slug, directory, parent_id, fork_session_id, version, time_created) VALUES (?,?,?,?,?,?,?,?)",
			session.id, session.title, session.slug, session.directory, session.parent, session.fork, session.version, fixtureTime(0, 8, i, 0),
		); err != nil {
			t.Fatalf("insert session %s: %v", session.id, err)
		}
	}

	insert := func(id, session, kind string, createdAt int64, payload any) {
		t.Helper()
		var data any
		if payload != nil {
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal %s: %v", id, err)
			}
			data = string(raw)
		}
		if _, err := db.Exec(
			"INSERT INTO session_message (id, session_id, type, time_created, data) VALUES (?,?,?,?,?)",
			id, session, kind, createdAt, data,
		); err != nil {
			t.Fatalf("insert message %s: %v", id, err)
		}
	}

	// ---- model A: tool steps with reasoning, censoring and a negative
	// overhead row.
	for i := 0; i < 14; i++ {
		row := modelARow(i)
		insert("a-"+itoa(i), "ses-root-a", "assistant", row.Created, row.mapValue())
	}

	// Short, non-canonical model rows (censored buckets and no timing at all).
	insert("a-nosmall", "ses-root-a", "assistant", fixtureTime(2, 20, 0, 0), fixturePayload{
		Provider: "alpha", Model: "model-a", Input: 100, Output: 10, Reasoning: 0,
		Created: fixtureTime(2, 20, 0, 0), Streamed: fixtureTime(2, 20, 0, 0) + 900, Completed: fixtureTime(2, 20, 0, 0) + 1000,
		Cost: 0.01,
	}.mapValue())
	insert("a-nostream-text", "ses-root-a", "assistant", fixtureTime(2, 21, 0, 0), fixturePayload{
		Provider: "alpha", Model: "model-a", Input: 300, Output: 120, Reasoning: 0,
		Created: fixtureTime(2, 21, 0, 0), Completed: fixtureTime(2, 21, 0, 0) + 2200,
		Cost: 0.02,
	}.mapValue())
	insert("a-nostream-tool", "ses-root-a", "assistant", fixtureTime(2, 22, 0, 0), fixturePayload{
		Provider: "alpha", Model: "model-a", Input: 300, Output: 220, Reasoning: 0,
		Created: fixtureTime(2, 22, 0, 0), Completed: fixtureTime(2, 22, 0, 0) + 3000,
		Cost:   0.03,
		Blocks: []any{fixtureTool("read", fixtureTime(2, 22, 0, 0)+500, fixtureTime(2, 22, 0, 0)+1500, fixtureTime(2, 22, 0, 0)+510, ""), fixtureText()},
	}.mapValue())

	// A fork copy of a-3: identical normalized payload, different ids, in a
	// forked session (so it sorts after the non-fork rows and deduplicates).
	forkPayload := modelARow(3).mapValue()
	forkPayload["id"] = "a-3-fork"
	forkPayload["sessionID"] = "ses-fork"
	forkPayload["path"] = "/work/project-a"
	insert("fork-a3", "ses-fork", "assistant", fixtureTime(1, 1, 0, 0), forkPayload)

	// Same timing/token fingerprint, different payload: a collision that stays.
	collisionPayload := modelARow(3).mapValue()
	collisionPayload["content"] = []any{fixtureText(), map[string]any{"type": "text", "text": "a different answer"}}
	insert("collision-a3", "ses-collision", "assistant", fixtureTime(2, 23, 0, 0), collisionPayload)

	// ---- model B: 32 priced rows spread over twelve days (price fit, linear
	// fit and enough daily groups for the anomaly detector).
	for i := 0; i < 32; i++ {
		created := fixtureTime(i%12, 8+i%11, i*2, 0)
		gen := int64(100 + i*3)
		reasoning := int64(i % 4 * 5)
		output := gen - reasoning
		input := int64(800 + i*7)
		cacheRead := int64(i%3) * 2000
		cacheWrite := int64(i%6%2) * 300
		rate := 120.0 + float64(i%4)*10
		streamedMs := created + int64(float64(gen)/rate*1000)
		completed := streamedMs + 350
		cost := float64(input)/1e6*3.0 + float64(cacheRead)/1e6*0.3 + float64(cacheWrite)/1e6*3.75 + float64(gen)/1e6*15.0
		insert("b-"+itoa(i), "ses-root-b", "assistant", created, fixturePayload{
			Provider: "beta", Model: "model-b", Input: input, Output: output, Reasoning: reasoning,
			CacheRead: cacheRead, CacheWrite: cacheWrite,
			Created: created, Streamed: streamedMs, Completed: completed,
			Cost: cost, Agent: "plan",
		}.mapValue())
	}

	// A high-cost day makes the daily spend anomaly detector fire.
	insert("b-spike", "ses-root-b", "assistant", fixtureTime(3, 19, 0, 0), fixturePayload{
		Provider: "beta", Model: "model-b", Input: 5000, Output: 400, Reasoning: 100,
		Created: fixtureTime(3, 19, 0, 0), Streamed: fixtureTime(3, 19, 0, 0) + 3200, Completed: fixtureTime(3, 19, 0, 0) + 3600,
		Cost: 5.5, Agent: "plan",
	}.mapValue())

	// Large-context rows exercise the 50-150k and 400k+ context buckets.
	insert("a-bigctx", "ses-root-a", "assistant", fixtureTime(4, 12, 0, 0), fixturePayload{
		Provider: "alpha", Model: "model-a", Input: 400_000, CacheRead: 50_000, Output: 300, Reasoning: 100,
		Created: fixtureTime(4, 12, 0, 0), Streamed: fixtureTime(4, 12, 0, 0) + 3000, Completed: fixtureTime(4, 12, 0, 0) + 3400,
		Cost: 0.05, Agent: "plan",
	}.mapValue())
	insert("c-midctx", "ses-dupbase", "assistant", fixtureTime(4, 13, 0, 0), fixturePayload{
		Provider: "gamma", Model: "model-c", Input: 80_000, Output: 200,
		Created: fixtureTime(4, 13, 0, 0), Streamed: fixtureTime(4, 13, 0, 0) + 2000, Completed: fixtureTime(4, 13, 0, 0) + 2300,
		Cost: 0, Agent: "explore",
	}.mapValue())

	// ---- model C: free rows in a project with a duplicate basename.
	for i := 0; i < 6; i++ {
		created := fixtureTime(1, 14, i*5, 0)
		gen := int64(60 + i*40)
		insert("c-"+itoa(i), "ses-dupbase", "assistant", created, fixturePayload{
			Provider: "gamma", Model: "model-c", Variant: "xhigh",
			Input: 400, Output: gen, Reasoning: 0, CacheRead: 100,
			Created: created, Streamed: created + 1200, Completed: created + 1500,
			Cost: 0, Agent: "explore",
		}.mapValue())
	}

	// ---- subagent tree rows.
	for i := 0; i < 3; i++ {
		created := fixtureTime(2, 10, i*7, 0)
		insert("sub-"+itoa(i), "ses-sub", "assistant", created, fixturePayload{
			Provider: "beta", Model: "model-b", Input: 300, Output: 150 + int64(i)*20, Reasoning: 10,
			Created: created, Streamed: created + 1400, Completed: created + 1700,
			Cost: 0.004 + float64(i)*0.001, Agent: "build",
		}.mapValue())
	}
	for i := 0; i < 2; i++ {
		created := fixtureTime(2, 11, i*9, 0)
		insert("child-"+itoa(i), "ses-child", "assistant", created, fixturePayload{
			Provider: "alpha", Model: "model-a", Input: 200, Output: 80 + int64(i)*10, Reasoning: 0,
			Created: created, Streamed: created + 1000, Completed: created + 1300,
			Cost: 0.002, Agent: "build",
			Blocks: []any{fixtureTool("read", created+900, created+1200, created+910, "error"), fixtureText()},
		}.mapValue())
	}

	// ---- ledger rows: error, empty and both, including a ledger-only session.
	insert("err-1", "ses-root-a", "assistant", fixtureTime(2, 12, 0, 0), fixturePayload{
		Provider: "alpha", Model: "model-a", Input: 900, Output: 300, Reasoning: 50,
		Created: fixtureTime(2, 12, 0, 0), Streamed: fixtureTime(2, 12, 0, 0) + 1500, Completed: fixtureTime(2, 12, 0, 0) + 2500,
		Cost: 0.0123, Finish: "error", Agent: "build",
		Blocks: []any{fixtureTool("bash", fixtureTime(2, 12, 0, 0)+1400, fixtureTime(2, 12, 0, 0)+2400, fixtureTime(2, 12, 0, 0)+1410, "error"), fixtureText()},
	}.mapValue())
	insert("empty-1", "ses-root-a", "assistant", fixtureTime(2, 13, 0, 0), fixturePayload{
		Provider: "alpha", Model: "model-a", Input: 50,
		Created: fixtureTime(2, 13, 0, 0), Streamed: fixtureTime(2, 13, 0, 0) + 800, Completed: fixtureTime(2, 13, 0, 0) + 900,
		Cost: 0.0004,
	}.mapValue())
	insert("both-1", "ses-ledger", "assistant", fixtureTime(2, 14, 0, 0), fixturePayload{
		Provider: "gamma", Model: "model-c", Input: 20, Finish: "error",
		Created: fixtureTime(2, 14, 0, 0), Streamed: fixtureTime(2, 14, 0, 0) + 400, Completed: fixtureTime(2, 14, 0, 0) + 600,
		Cost: 0.0007,
	}.mapValue())
	insert("empty-2", "ses-ledger", "assistant", fixtureTime(2, 15, 0, 0), fixturePayload{
		Provider: "gamma", Model: "model-c", Input: 30,
		Created: fixtureTime(2, 15, 0, 0), Streamed: fixtureTime(2, 15, 0, 0) + 500, Completed: fixtureTime(2, 15, 0, 0) + 700,
		Cost: 0.0002,
	}.mapValue())
	// A row without time.created is skipped before the ledger.
	insert("nocreated", "ses-root-a", "assistant", fixtureTime(2, 16, 0, 0), fixturePayload{
		Provider: "alpha", Model: "model-a", Input: 10, Output: 5,
	}.mapValue())

	// ---- store-wide events.
	insert("compaction-1", "ses-root-a", "compaction", fixtureTime(2, 8, 0, 0), map[string]any{"tokensBefore": 48000})
	insert("compaction-2", "ses-sub", "compaction", fixtureTime(2, 9, 0, 0), map[string]any{"tokens": 12000})
	insert("switch-1", "ses-root-b", "model-switched", fixtureTime(2, 9, 30, 0), map[string]any{"model": "beta/model-b"})
	insert("switch-2", "ses-root-a", "model-switched", fixtureTime(2, 9, 45, 0), map[string]any{"to": "alpha/model-a"})
	insert("idle-1", "ses-root-a", "idle", fixtureTime(2, 10, 0, 0), nil)
	insert("idle-2", "ses-root-b", "idle", fixtureTime(2, 10, 5, 0), nil)
	insert("shell-1", "ses-root-a", "shell", fixtureTime(2, 10, 10, 0), nil)
	insert("agent-switch-1", "ses-root-a", "agent-switched", fixtureTime(2, 10, 15, 0), map[string]any{"agent": "plan"})
	insert("user-1", "ses-root-a", "user", fixtureTime(2, 10, 20, 0), map[string]any{"text": "hello"})
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func agentFor(index int) string {
	if index%3 == 0 {
		return "plan"
	}
	return "build"
}

func variantFor(index int) string {
	if index%4 == 0 {
		return "high"
	}
	return ""
}

// runPythonBuilder runs the reference builder on the fixture store with the
// given options and returns the parsed report. Used only to refresh goldens.
func runPythonBuilder(t *testing.T, dbPath, outDir string, extraArgs ...string) map[string]any {
	t.Helper()
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	script := filepath.Join(repoRoot, "opencode-telemetry", "py", "telemetry.py")
	args := append([]string{script, "--db", dbPath, "--out", outDir}, extraArgs...)
	cmd := exec.Command("python3", args...)
	cmd.Env = append(os.Environ(), "TZ=UTC")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python builder failed: %v\n%s", err, output)
	}
	return readJSONFile(t, filepath.Join(outDir, "telemetry-data.json"))
}

// openAndSchema opens a SQLite file and creates the v2 fixture schema.
func openAndSchema(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(fixtureSchema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
