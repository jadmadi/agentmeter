// SPDX-License-Identifier: AGPL-3.0-only

package ocsource

import (
	"database/sql"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

const testSchema = `
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

// testDB creates a writable v2 store in a temp dir and returns it with its
// absolute path.
func testDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}
	return db, path
}

func insertSession(t *testing.T, db *sql.DB, id, directory string, parent, fork, version any, title any) {
	t.Helper()
	_, err := db.Exec(
		"INSERT INTO session_v2 (id, title, slug, directory, parent_id, fork_session_id, version, time_created) VALUES (?,?,?,?,?,?,?,?)",
		id, title, "slug-"+id, directory, parent, fork, version, 1000,
	)
	if err != nil {
		t.Fatalf("insert session %s: %v", id, err)
	}
}

func insertMessage(t *testing.T, db *sql.DB, id, sessionID, kind string, timeCreated int64, payload any) {
	t.Helper()
	var data any
	switch value := payload.(type) {
	case nil:
		data = nil
	case string:
		data = value
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		data = string(raw)
	}
	_, err := db.Exec(
		"INSERT INTO session_message (id, session_id, type, time_created, data) VALUES (?,?,?,?,?)",
		id, sessionID, kind, timeCreated, data,
	)
	if err != nil {
		t.Fatalf("insert message %s: %v", id, err)
	}
}

// assistantPayload builds an OpenCode assistant message payload.
func assistantPayload(createdMs, streamedMs, completedMs int64, mutate func(map[string]any)) map[string]any {
	payload := map[string]any{
		"model": map[string]any{"providerID": "test", "id": "model-a"},
		"tokens": map[string]any{
			"input":     100,
			"output":    50,
			"reasoning": 10,
			"cache":     map[string]any{"read": 20, "write": 5},
		},
		"time": map[string]any{
			"created":   createdMs,
			"streamed":  streamedMs,
			"completed": completedMs,
		},
		"content": []any{map[string]any{"type": "text"}},
		"finish":  "stop",
		"agent":   "build",
		"cost":    0.0125,
	}
	if mutate != nil {
		mutate(payload)
	}
	return payload
}

func loadFixture(t *testing.T, dbPath string, opts LoadOptions) *Loaded {
	t.Helper()
	opts.DBPath = dbPath
	loaded, err := Load(opts)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return loaded
}

func TestMissingDatabase(t *testing.T) {
	_, err := Load(LoadOptions{DBPath: filepath.Join(t.TempDir(), "absent.db")})
	if err == nil {
		t.Fatal("expected an error for a missing database")
	}
	if !strings.Contains(err.Error(), "OpenCode database not found at") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "Pass --db <path>") {
		t.Fatalf("missing --db hint: %v", err)
	}
}

func TestV1OnlyStoreRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE message (id TEXT PRIMARY KEY); CREATE TABLE part (id TEXT PRIMARY KEY);"); err != nil {
		t.Fatalf("create v1 schema: %v", err)
	}
	_, err = Load(LoadOptions{DBPath: path})
	if err == nil {
		t.Fatal("expected v1-only store to be rejected")
	}
	message := err.Error()
	if !strings.Contains(message, "schema not recognized") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(message, "missing session_message, session_v2") {
		t.Fatalf("expected both missing tables, sorted: %v", err)
	}
}

func TestEmptyV2Store(t *testing.T) {
	_, path := testDB(t)
	loaded := loadFixture(t, path, LoadOptions{IncludeTitles: true})
	if len(loaded.Rows) != 0 || len(loaded.Ledger) != 0 {
		t.Fatalf("expected no rows, got %d rows / %d ledger", len(loaded.Rows), len(loaded.Ledger))
	}
	if loaded.SessionsTotal != 0 || loaded.MaxCreatedMs != 0 || loaded.Scanned != 0 {
		t.Fatalf("unexpected counters: %+v", loaded)
	}
}

func TestTokenNormalizationAndTiming(t *testing.T) {
	db, path := testDB(t)
	insertSession(t, db, "ses1", "/work/project", nil, nil, "1.0", "Fixture session")
	insertMessage(t, db, "msg1", "ses1", "assistant", 2000,
		assistantPayload(2_000_000, 2_001_500, 2_002_000, nil))

	loaded := loadFixture(t, path, LoadOptions{IncludeTitles: true})
	if len(loaded.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(loaded.Rows))
	}
	row := loaded.Rows[0]
	if row.Out != 50 || row.Reas != 10 || row.Gen != 60 {
		t.Fatalf("token normalization: out=%d reas=%d gen=%d", row.Out, row.Reas, row.Gen)
	}
	if row.Inp != 100 || row.CacheRead != 20 || row.CacheWrite != 5 || row.InpTotal() != 125 {
		t.Fatalf("input split: inp=%d cr=%d cw=%d", row.Inp, row.CacheRead, row.CacheWrite)
	}
	if row.DT == nil || *row.DT != 1.5 {
		t.Fatalf("llm seconds: %v", row.DT)
	}
	if row.TotalDT == nil || *row.TotalDT != 2.0 {
		t.Fatalf("total seconds: %v", row.TotalDT)
	}
	if row.ToolDT != nil || row.ToolSpanDT != nil || row.ToolGapDT != nil {
		t.Fatalf("tool timing must stay nil without tool blocks")
	}
	if row.OverheadDT == nil || math.Abs(*row.OverheadDT-0.5) > 1e-12 {
		t.Fatalf("overhead: %v", row.OverheadDT)
	}
	if row.TTFT != nil {
		t.Fatalf("text-only step must not invent a TTFT: %v", row.TTFT)
	}
	if row.SessionTitle == nil || *row.SessionTitle != "Fixture session" {
		t.Fatalf("session title: %v", row.SessionTitle)
	}
	if row.Project != "/work/project" || row.ProjectLabel != "project" {
		t.Fatalf("project path/label: %q / %q", row.Project, row.ProjectLabel)
	}
	if row.Entry != "root" || row.Provider != "test" || row.Short != "model-a" || row.Variant != "default" {
		t.Fatalf("model metadata: %+v", row)
	}
	if loaded.MaxCreatedMs != 2_000_000 {
		t.Fatalf("watermark: %d", loaded.MaxCreatedMs)
	}
}

func TestReasoningTTFTAndDecode(t *testing.T) {
	db, path := testDB(t)
	insertSession(t, db, "ses1", "/p", nil, nil, "1.0", nil)
	insertMessage(t, db, "msg1", "ses1", "assistant", 2000, assistantPayload(1_000_000, 1_003_000, 1_004_000, func(p map[string]any) {
		p["content"] = []any{
			map[string]any{"type": "reasoning", "time": map[string]any{"created": 1_000_200}},
			map[string]any{"type": "text"},
		}
	}))
	loaded := loadFixture(t, path, LoadOptions{})
	row := loaded.Rows[0]
	if row.NThinking != 1 || row.NText != 1 || !row.Thinking {
		t.Fatalf("block counts: %d/%d/%v", row.NThinking, row.NText, row.Thinking)
	}
	if row.TTFT == nil || math.Abs(*row.TTFT-0.2) > 1e-12 {
		t.Fatalf("ttft: %v", row.TTFT)
	}
	if row.DecodeDT == nil || math.Abs(*row.DecodeDT-2.8) > 1e-12 {
		t.Fatalf("decode seconds: %v", row.DecodeDT)
	}
	if row.DecodeRate == nil || math.Abs(*row.DecodeRate-60.0/2.8) > 1e-12 {
		t.Fatalf("decode rate: %v", row.DecodeRate)
	}
}

func TestToolIntervalsSpanGapAndAllocation(t *testing.T) {
	db, path := testDB(t)
	insertSession(t, db, "ses1", "/p", nil, nil, "1.0", nil)
	insertMessage(t, db, "msg1", "ses1", "assistant", 2000, assistantPayload(1_000_000, 1_002_000, 1_005_000, func(p map[string]any) {
		p["content"] = []any{
			map[string]any{"type": "tool", "name": "read", "time": map[string]any{"ran": 1_002_000, "completed": 1_003_000, "created": 1_002_100}},
			map[string]any{"type": "tool", "name": "bash", "time": map[string]any{"ran": 1_002_500, "completed": 1_004_000, "created": 1_002_600}},
			map[string]any{"type": "tool", "name": "read", "time": map[string]any{"ran": 1_004_500, "completed": 1_004_800}},
		}
	}))
	loaded := loadFixture(t, path, LoadOptions{})
	row := loaded.Rows[0]
	// Union: [2000,4000] (2.0s) + [4500,4800] (0.3s) = 2.3s; span 2.8s.
	if row.ToolDT == nil || math.Abs(*row.ToolDT-2.3) > 1e-12 {
		t.Fatalf("union: %v", row.ToolDT)
	}
	if row.ToolSpanDT == nil || math.Abs(*row.ToolSpanDT-2.8) > 1e-12 {
		t.Fatalf("span: %v", row.ToolSpanDT)
	}
	if row.ToolGapDT == nil || math.Abs(*row.ToolGapDT-0.5) > 1e-12 {
		t.Fatalf("gap: %v", row.ToolGapDT)
	}
	// overhead = total 5.0 - llm 2.0 - union 2.3 = 0.7
	if row.OverheadDT == nil || math.Abs(*row.OverheadDT-0.7) > 1e-12 {
		t.Fatalf("overhead: %v", row.OverheadDT)
	}
	allocations := AllocateToolTime(row.ToolDetails)
	if len(allocations) != 3 {
		t.Fatalf("allocations: %v", allocations)
	}
	// Durations 1.0, 1.5, 0.3 sum to 2.8 > union 2.3, so scale = 2.3/2.8.
	scale := 2.3 / 2.8
	want := []float64{1.0 * scale, 1.5 * scale, 0.3 * scale}
	var sum float64
	for i := range want {
		if math.Abs(allocations[i]-want[i]) > 1e-12 {
			t.Fatalf("allocation[%d] = %v, want %v", i, allocations[i], want[i])
		}
		sum += allocations[i]
	}
	if math.Abs(sum-2.3) > 1e-12 {
		t.Fatalf("allocations must sum to the union, got %v", sum)
	}
}

func TestDedupForkAndCollisions(t *testing.T) {
	db, path := testDB(t)
	insertSession(t, db, "ses-root", "/p", nil, nil, "1.0", nil)
	insertSession(t, db, "ses-fork", "/p", nil, "ses-root", "1.0", nil)
	base := assistantPayload(1_000_000, 1_002_000, 1_003_000, nil)
	insertMessage(t, db, "a1", "ses-root", "assistant", 1000, base)
	// A fork copy: identical normalized payload, different ids and session.
	forked := assistantPayload(1_000_000, 1_002_000, 1_003_000, func(p map[string]any) {
		p["id"] = "different"
		p["sessionID"] = "ses-fork"
		p["path"] = "/elsewhere"
	})
	insertMessage(t, db, "a2", "ses-fork", "assistant", 2000, forked)
	// Same fingerprint, different payload: a collision that must be kept.
	collision := assistantPayload(1_000_000, 1_002_000, 1_003_000, func(p map[string]any) {
		p["content"] = []any{map[string]any{"type": "text", "text": "different answer"}}
	})
	insertMessage(t, db, "a3", "ses-root", "assistant", 3000, collision)

	loaded := loadFixture(t, path, LoadOptions{})
	if loaded.Duplicates != 1 {
		t.Fatalf("duplicates: %d", loaded.Duplicates)
	}
	if loaded.DuplicateSessions["ses-fork"] != 1 {
		t.Fatalf("duplicate session attribution: %v", loaded.DuplicateSessions)
	}
	if loaded.DuplicateTokens != 60 || math.Abs(loaded.DuplicateCost-0.0125) > 1e-12 {
		t.Fatalf("duplicate accounting: tokens=%d cost=%v", loaded.DuplicateTokens, loaded.DuplicateCost)
	}
	if loaded.Collisions != 1 {
		t.Fatalf("collisions: %d", loaded.Collisions)
	}
	if len(loaded.Rows) != 2 {
		t.Fatalf("expected the original and the collision to be kept, got %d", len(loaded.Rows))
	}
	// Fork copies sort after non-fork rows, then by time_created, then id.
	if loaded.Rows[0].ID != "a1" || loaded.Rows[1].ID != "a3" {
		t.Fatalf("row order: %s, %s", loaded.Rows[0].ID, loaded.Rows[1].ID)
	}
}

func TestLedgerEventsAndWatermark(t *testing.T) {
	db, path := testDB(t)
	insertSession(t, db, "ses1", "/p", nil, nil, "1.5", nil)
	insertSession(t, db, "ses-child", "/p", "ses1", nil, nil, nil)
	insertMessage(t, db, "ok", "ses1", "assistant", 1000, assistantPayload(1_000_000, 1_001_000, 1_001_500, nil))
	// Zero generated tokens -> empty ledger.
	insertMessage(t, db, "empty", "ses1", "assistant", 2000, assistantPayload(1_000_000, 1_001_000, 1_001_500, func(p map[string]any) {
		p["tokens"] = map[string]any{"input": 10, "output": 0, "reasoning": 0}
		p["cost"] = 0.5
	}))
	// finish=error with tokens -> error ledger.
	insertMessage(t, db, "err", "ses1", "assistant", 3000, assistantPayload(1_000_000, 1_001_000, 1_001_500, func(p map[string]any) {
		p["finish"] = "error"
		p["cost"] = 0.25
	}))
	// Both flags.
	insertMessage(t, db, "both", "ses1", "assistant", 4000, assistantPayload(1_000_000, 1_001_000, 1_001_500, func(p map[string]any) {
		p["finish"] = "error"
		p["tokens"] = map[string]any{"input": 1}
	}))
	// No time.created -> skipped before the ledger.
	insertMessage(t, db, "nocreated", "ses1", "assistant", 5000, assistantPayload(0, 0, 0, nil))
	// Normally this row would be counted, but the model filter drops it and
	// the watermark must not see it.
	insertMessage(t, db, "other-model", "ses1", "assistant", 6000, assistantPayload(9_000_000, 9_001_000, 9_002_000, func(p map[string]any) {
		p["model"] = map[string]any{"providerID": "other", "id": "model-b"}
	}))
	insertMessage(t, db, "compaction", "ses1", "compaction", 7000, map[string]any{"tokensBefore": 12345})
	insertMessage(t, db, "switch", "ses1", "model-switched", 8000, map[string]any{"model": "next-model"})
	insertMessage(t, db, "idle", "ses1", "idle", 9000, nil)
	insertMessage(t, db, "user", "ses1", "user", 9500, map[string]any{"text": "hello"})

	loaded := loadFixture(t, path, LoadOptions{ModelFilters: []string{"model-a"}})
	if len(loaded.Rows) != 1 {
		t.Fatalf("rows: %d", len(loaded.Rows))
	}
	if len(loaded.Ledger) != 3 {
		t.Fatalf("ledger: %d", len(loaded.Ledger))
	}
	reasons := []string{loaded.Ledger[0].Reason, loaded.Ledger[1].Reason, loaded.Ledger[2].Reason}
	if reasons[0] != "empty" || reasons[1] != "error" || reasons[2] != "both" {
		t.Fatalf("ledger reasons: %v", reasons)
	}
	if loaded.EmptySteps != 2 || loaded.ErrorSteps != 2 {
		t.Fatalf("empty/error step counters: %d/%d", loaded.EmptySteps, loaded.ErrorSteps)
	}
	if loaded.NoCreated != 1 {
		t.Fatalf("noCreated: %d", loaded.NoCreated)
	}
	if loaded.MaxCreatedMs != 1_000_000 {
		t.Fatalf("the watermark must ignore model-filtered rows, got %d", loaded.MaxCreatedMs)
	}
	if loaded.Scanned != 6 {
		t.Fatalf("scanned assistant rows: %d", loaded.Scanned)
	}
	if loaded.SessionsTotal != 2 || loaded.SessionsRoot != 1 || loaded.SessionsSubagent != 1 {
		t.Fatalf("session tree: total=%d root=%d sub=%d", loaded.SessionsTotal, loaded.SessionsRoot, loaded.SessionsSubagent)
	}
	if loaded.Events.Compactions != 1 || loaded.Events.ModelSwitches != 1 || loaded.Events.Idle != 1 {
		t.Fatalf("event counters: %+v", loaded.Events)
	}
	if len(loaded.Events.CompactionEvents) != 1 || loaded.Events.CompactionEvents[0].TokensBefore != int64(12345) {
		t.Fatalf("compaction events: %+v", loaded.Events.CompactionEvents)
	}
	if len(loaded.Events.ModelSwitchEvents) != 1 || loaded.Events.ModelSwitchEvents[0].Model != "next-model" {
		t.Fatalf("model switch events: %+v", loaded.Events.ModelSwitchEvents)
	}
	if loaded.Events.Versions.Get("1.5") != 1 || loaded.Events.Versions.Get("unknown") != 1 {
		t.Fatalf("version counter: %v", loaded.Events.Versions.Items())
	}
	if loaded.Events.ToolCalls.Total() != 0 {
		t.Fatalf("tool calls: %v", loaded.Events.ToolCalls.Items())
	}
}

func TestDateFilterAndWatermark(t *testing.T) {
	db, path := testDB(t)
	insertSession(t, db, "ses1", "/p", nil, nil, "1.0", nil)
	// 2026-01-01T00:00:00Z, 2026-01-02T00:00:00Z, 2026-01-03T00:00:00Z.
	insertMessage(t, db, "d1", "ses1", "assistant", 1, assistantPayload(1_767_225_600_000, 1_767_225_601_000, 1_767_225_602_000, nil))
	insertMessage(t, db, "d2", "ses1", "assistant", 2, assistantPayload(1_767_312_000_000, 1_767_312_001_000, 1_767_312_002_000, nil))
	insertMessage(t, db, "d3", "ses1", "assistant", 3, assistantPayload(1_767_398_400_000, 1_767_398_401_000, 1_767_398_402_000, nil))

	loaded := loadFixture(t, path, LoadOptions{Since: "2026-01-02", Until: "2026-01-02"})
	if len(loaded.Rows) != 1 || loaded.Rows[0].ID != "d2" {
		t.Fatalf("date filter kept %d rows", len(loaded.Rows))
	}
	if loaded.Considered != 1 || loaded.Scanned != 3 {
		t.Fatalf("counters: considered=%d scanned=%d", loaded.Considered, loaded.Scanned)
	}
	// The watermark covers every created row, including those the date filter
	// dropped.
	if loaded.MaxCreatedMs != 1_767_398_400_000 {
		t.Fatalf("watermark: %d", loaded.MaxCreatedMs)
	}
}

func TestMissingStreamedFallsBackOnlyWithoutTools(t *testing.T) {
	db, path := testDB(t)
	insertSession(t, db, "ses1", "/p", nil, nil, "1.0", nil)
	insertMessage(t, db, "text", "ses1", "assistant", 1, assistantPayload(1_000_000, 0, 1_002_000, nil))
	insertMessage(t, db, "tool", "ses1", "assistant", 2, assistantPayload(1_000_000, 0, 1_002_000, func(p map[string]any) {
		p["content"] = []any{map[string]any{"type": "tool", "name": "read", "time": map[string]any{"ran": 1_000_500, "completed": 1_001_000}}}
	}))
	loaded := loadFixture(t, path, LoadOptions{})
	if len(loaded.Rows) != 2 {
		t.Fatalf("rows: %d", len(loaded.Rows))
	}
	if loaded.Rows[0].DT == nil || *loaded.Rows[0].DT != 2.0 {
		t.Fatalf("tool-free fallback to completed: %v", loaded.Rows[0].DT)
	}
	if loaded.Rows[1].DT != nil {
		t.Fatalf("tool steps without streamed must stay untimed: %v", loaded.Rows[1].DT)
	}
}

func TestPayloadParseErrorsAreFailClosed(t *testing.T) {
	db, path := testDB(t)
	insertSession(t, db, "ses1", "/p", nil, nil, "1.0", nil)
	// Invalid JSON is skipped, exactly like the Python ValueError branch.
	insertMessage(t, db, "bad", "ses1", "assistant", 1, "{not json")
	// A numeric string for a token count parses like Python's int().
	insertMessage(t, db, "numeric-string", "ses1", "assistant", 2, assistantPayload(1_000_000, 1_001_000, 1_002_000, func(p map[string]any) {
		p["tokens"].(map[string]any)["output"] = "50"
	}))
	loaded := loadFixture(t, path, LoadOptions{})
	if len(loaded.Rows) != 1 || loaded.Rows[0].Out != 50 {
		t.Fatalf("rows: %+v", loaded.Rows)
	}

	// A non-numeric string is a hard error, not a silent zero.
	insertMessage(t, db, "bad-token", "ses1", "assistant", 3, assistantPayload(1_000_000, 1_001_000, 1_002_000, func(p map[string]any) {
		p["tokens"].(map[string]any)["output"] = "many"
	}))
	if _, err := Load(LoadOptions{DBPath: path}); err == nil {
		t.Fatal("expected a parse error for a non-numeric token count")
	}
}
