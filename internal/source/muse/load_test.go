// SPDX-License-Identifier: AGPL-3.0-only

package muse

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func writeSession(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func created(seq int, ts int64, response string) string {
	return `{"id":"c` + itoa(seq) + `","stream":"session","sequence":` + itoa(seq) +
		`,"recorded_at":` + itoa(int(ts)) + `,"record_type":"event","payload_type":"runtime.session",` +
		`"payload":{"kind":"event","run_id":"run-1","event":{"kind":"model_response_created","response_id":"` + response + `"}}}`
}

func completed(seq int, id string, ts int64, model string, usage string, ms int64, finish string) string {
	finishJSON := "null"
	if finish != "" {
		finishJSON = `"` + finish + `"`
	}
	return `{"id":"` + id + `","stream":"session","sequence":` + itoa(seq) +
		`,"recorded_at":` + itoa(int(ts)) + `,"record_type":"event","payload_type":"runtime.session",` +
		`"payload":{"kind":"event","run_id":"run-1","event":{"kind":"model_completed","model":"` + model +
		`","usage":` + usage + `,"duration_ms":` + itoa(int(ms)) + `,"finish_reason":` + finishJSON + `}}}`
}

func usage(input, output, read, write, thinking int64) string {
	return `{"input_tokens":` + itoa(int(input)) + `,"output_tokens":` + itoa(int(output)) +
		`,"cache_read_tokens":` + itoa(int(read)) + `,"cache_write_tokens":` + itoa(int(write)) +
		`,"cached_tokens":` + itoa(int(read+write)) + `,"reasoning_tokens":` + itoa(int(thinking)) + `}`
}

func commit(seq int, ts int64, response string, tools ...string) string {
	var calls []string
	for i, name := range tools {
		calls = append(calls, `{"id":"t`+itoa(i)+`","call_id":"call-`+itoa(i)+`","name":"`+name+`","args":{}}`)
	}
	return `{"id":"k` + itoa(seq) + `","stream":"session","sequence":` + itoa(seq) +
		`,"recorded_at":` + itoa(int(ts)) + `,"record_type":"event","payload_type":"runtime.session",` +
		`"payload":{"kind":"event","run_id":"run-1","event":{"kind":"assistant_tool_calls_committed",` +
		`"message_id":"m1","response_id":"` + response + `","tool_calls":[` + strings.Join(calls, ",") + `]}}}`
}

// dayTS returns the microsecond timestamp for 2026-11-0d at 10:00:00 UTC
// plus extra seconds.
func dayTS(day int, extra int64) int64 {
	base := int64(1793527200000000) // 2026-11-01T10:00:00Z
	return base + int64(day-1)*86400*1000000 + extra*1000000
}

func TestLoadBasicRow(t *testing.T) {
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "2026/11/01/sess-a/session.jsonl"),
		created(1, dayTS(1, 0), "r1"),
		completed(2, "m1", dayTS(1, 20), "muse-spark-x", usage(6000, 400, 5000, 200, 100), 20000, "tool_calls"),
		commit(3, dayTS(1, 21), "r1", "muse.bash"),
	)

	source := Load(Filters{Root: root})
	if source.Counts.Rows != 1 {
		t.Fatalf("rows = %d, want 1", source.Counts.Rows)
	}
	row := source.Rows[0]
	if row.Out != 400 || row.Reas != 100 || row.Visible() != 300 {
		t.Fatalf("out/reas/visible = %d/%d/%d", row.Out, row.Reas, row.Visible())
	}
	if row.Inp != 6000 || row.CacheRead != 5000 || row.CacheWrite != 200 {
		t.Fatalf("inp/cache = %d/%d/%d", row.Inp, row.CacheRead, row.CacheWrite)
	}
	if row.Cost != 0 {
		t.Fatalf("cost = %v, want 0", row.Cost)
	}
	if row.DT == nil || *row.DT != 20 {
		t.Fatalf("DT = %v, want 20", row.DT)
	}
	if row.TPS == nil || *row.TPS != 20 {
		t.Fatalf("TPS = %v, want 20", row.TPS)
	}
	if row.Timing != "plausible" {
		t.Fatalf("timing = %q", row.Timing)
	}
	if !row.Tool || len(row.Tools) != 1 || row.Tools[0] != "muse.bash" {
		t.Fatalf("tools = %+v", row.Tools)
	}
	if !row.Thinking || row.Subagent {
		t.Fatalf("thinking/subagent = %v/%v", row.Thinking, row.Subagent)
	}
	if row.SessionID != "sess-a" || row.ProjectPath != "(unknown)" || row.Entrypoint != "muse" {
		t.Fatalf("session/project/entry = %q/%q/%q", row.SessionID, row.ProjectPath, row.Entrypoint)
	}
	if row.SessionTitle != nil || row.Branch != nil {
		t.Fatalf("title/branch without index = %v/%v", row.SessionTitle, row.Branch)
	}
}

func TestLoadPairingSkipsAbandoned(t *testing.T) {
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "s/session.jsonl"),
		created(1, dayTS(1, 0), "r1"),
		created(2, dayTS(1, 5), "r2"), // abandoned: no completion
		completed(3, "m2", dayTS(1, 25), "m", usage(100, 100, 0, 0, 0), 20000, ""),
		commit(4, dayTS(1, 26), "r2", "muse.bash"),
	)
	source := Load(Filters{Root: root})
	if len(source.Rows) != 1 {
		t.Fatalf("rows = %d", len(source.Rows))
	}
	// Latest-before pairing attaches r2's tools despite the abandoned r1.
	if len(source.Rows[0].Tools) != 1 {
		t.Fatalf("tools = %+v, want the r2 commit", source.Rows[0].Tools)
	}
	if source.Counts.Incomplete != 1 || source.Events.UnmatchedCommits != 0 {
		t.Fatalf("incomplete=%d unmatched=%d", source.Counts.Incomplete, source.Events.UnmatchedCommits)
	}
	reasons := map[string]int64{}
	for _, entry := range source.Ledger {
		reasons[entry.Reason]++
	}
	if reasons[ReasonIncomplete] != 1 {
		t.Fatalf("ledger = %v", reasons)
	}
}

func TestLoadLedgerReasons(t *testing.T) {
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "s/session.jsonl"),
		created(1, dayTS(1, 0), "r1"),
		completed(2, "m1", dayTS(1, 20), "m", usage(100, 0, 0, 0, 0), 20000, ""),
		created(3, dayTS(1, 30), "r2"),
		completed(4, "m2", dayTS(1, 50), "m", usage(100, 100, 0, 0, 0), 20000, ""),
		completed(5, "m2", dayTS(1, 50), "m", usage(100, 100, 0, 0, 0), 20000, ""),
		`{"position":6,"retained_marker":true,"schema_version":1,"stream":"session","omitted_record":{"position":6}}`,
	)
	writeSession(t, filepath.Join(root, "s/subagent/sub-1/session.jsonl"),
		created(1, dayTS(1, 60), "r3"),
		completed(2, "m3", dayTS(1, 80), "m", usage(100, 100, 0, 0, 0), 20000, ""),
	)
	source := Load(Filters{Root: root})
	if source.Counts.Rows != 2 {
		t.Fatalf("rows = %d, want 2", source.Counts.Rows)
	}
	reasons := map[string]int64{}
	for _, entry := range source.Ledger {
		reasons[entry.Reason]++
	}
	if reasons[ReasonEmpty] != 1 || reasons[ReasonDuplicate] != 1 {
		t.Fatalf("ledger reasons = %v", reasons)
	}
	if source.Counts.Subagents != 1 || len(source.Events.Subagents) != 1 {
		t.Fatalf("subagents = %d/%d", source.Counts.Subagents, len(source.Events.Subagents))
	}
	if source.Counts.Omissions != 1 {
		t.Fatalf("omissions = %d", source.Counts.Omissions)
	}
	// The child log attributes to its parent: one session, and the
	// subagent set holds the child file id, not the parent.
	if len(source.Sessions) != 1 {
		t.Fatalf("sessions = %d", len(source.Sessions))
	}
	if _, ok := source.Events.Subagents["sub-1"]; !ok {
		t.Fatalf("subagent ids = %v", source.Events.Subagents)
	}
	found := false
	for _, row := range source.Rows {
		if row.Subagent {
			found = true
			if row.SessionID != "s" {
				t.Fatalf("child session = %q, want parent s", row.SessionID)
			}
		}
	}
	if !found {
		t.Fatal("no subagent row")
	}
}

func TestLoadNoTimestamp(t *testing.T) {
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "s/session.jsonl"),
		`{"id":"m1","stream":"session","sequence":1,"record_type":"event","payload_type":"runtime.session",`+
			`"payload":{"kind":"event","event":{"kind":"model_completed","model":"m",`+
			`"usage":{"input_tokens":100,"output_tokens":100},"duration_ms":20000}}}`,
	)
	source := Load(Filters{Root: root})
	if source.Counts.Rows != 0 || source.Counts.NoTimestamp != 1 {
		t.Fatalf("counts = %+v", source.Counts)
	}
}

func TestLoadFiltersAndWatermark(t *testing.T) {
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "s/session.jsonl"),
		created(1, dayTS(1, 0), "r1"),
		completed(2, "m1", dayTS(1, 20), "muse-spark-x", usage(100, 100, 0, 0, 0), 20000, ""),
		created(3, dayTS(3, 0), "r2"),
		completed(4, "m2", dayTS(3, 20), "muse-spark-y", usage(100, 100, 0, 0, 0), 20000, ""),
	)
	source := Load(Filters{Root: root, ModelFilters: []string{"spark-y"}})
	if source.Counts.FilteredModel != 1 || len(source.Rows) != 1 {
		t.Fatalf("model filter: %+v rows=%d", source.Counts, len(source.Rows))
	}
	if source.MaxTS == nil || source.MaxTS.UTC().Format("2006-01-02") != "2026-11-03" {
		t.Fatalf("maxTS = %v", source.MaxTS)
	}
	source = Load(Filters{Root: root, Since: "2026-11-02"})
	if source.Counts.FilteredDate != 1 || len(source.Rows) != 1 {
		t.Fatalf("date filter: %+v rows=%d", source.Counts, len(source.Rows))
	}
}

func TestLoadIndexLabels(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	writeSession(t, filepath.Join(sessions, "2026/11/01/sess-a/session.jsonl"),
		created(1, dayTS(1, 0), "r1"),
		completed(2, "m1", dayTS(1, 20), "m", usage(100, 100, 0, 0, 0), 20000, ""),
	)
	dbPath := filepath.Join(root, "session-index.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (session_id TEXT, workspace_root TEXT, git_branch TEXT,
		title TEXT, effective_long_title TEXT, msp_title TEXT, first_user_prompt TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO sessions VALUES ('sess-a', '/repo/alpha', 'feat', 'My title', NULL, NULL, 'secret prompt')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	source := Load(Filters{Root: sessions, IncludeTitles: true})
	if len(source.Rows) != 1 {
		t.Fatalf("rows = %d", len(source.Rows))
	}
	row := source.Rows[0]
	if row.ProjectPath != "/repo/alpha" || row.Branch == nil || *row.Branch != "feat" {
		t.Fatalf("project/branch = %q/%v", row.ProjectPath, row.Branch)
	}
	if row.SessionTitle == nil || *row.SessionTitle != "My title" {
		t.Fatalf("title = %v", row.SessionTitle)
	}
	source = Load(Filters{Root: sessions, IncludeTitles: false})
	if source.Rows[0].SessionTitle != nil {
		t.Fatalf("title kept with IncludeTitles=false: %v", source.Rows[0].SessionTitle)
	}
}

func TestLoadIgnoresNonSessionFiles(t *testing.T) {
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "s/session.jsonl"),
		created(1, dayTS(1, 0), "r1"),
		completed(2, "m1", dayTS(1, 20), "m", usage(100, 100, 0, 0, 0), 20000, ""),
	)
	writeSession(t, filepath.Join(root, "s/tui-history.jsonl"),
		completed(1, "other", dayTS(1, 20), "m", usage(100, 100, 0, 0, 0), 20000, ""),
	)
	if err := os.WriteFile(filepath.Join(root, "s/session-index.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := Load(Filters{Root: root})
	if len(source.Files) != 1 || len(source.Rows) != 1 {
		t.Fatalf("files=%d rows=%d", len(source.Files), len(source.Rows))
	}
}

func sessionEnd(seq int, ts int64, exit string) string {
	return `{"id":"e` + itoa(seq) + `","stream":"session","sequence":` + itoa(seq) +
		`,"recorded_at":` + itoa(int(ts)) + `,"record_type":"event","payload_type":"session.end",` +
		`"payload":{"kind":"session_end","record":{"exit_reason":"` + exit + `","session_id":"sess"}}}`
}

func branchEvent(seq int, ts int64, reference, workspace string) string {
	ref := `{"kind":"branch","name":"` + reference + `"}`
	if reference == "" {
		ref = "null"
	}
	return `{"id":"b` + itoa(seq) + `","stream":"session","sequence":` + itoa(seq) +
		`,"recorded_at":` + itoa(int(ts)) + `,"record_type":"event","payload_type":"session.workspace_branch.observed",` +
		`"payload":{"kind":"workspace_branch","record":{"vcs":"git",` +
		`"reference":` + ref + `,"commit":"abc123","workspace_root":"` + workspace + `"}}}`
}

func TestLoadSessionEndOutcome(t *testing.T) {
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "s/session.jsonl"),
		created(1, dayTS(1, 0), "r1"),
		completed(2, "m1", dayTS(1, 20), "m", usage(100, 100, 0, 0, 0), 20000, ""),
		sessionEnd(3, dayTS(1, 90), "clean"),
	)
	writeSession(t, filepath.Join(root, "s/subagent/sub-1/session.jsonl"),
		created(1, dayTS(1, 30), "r2"),
		completed(2, "m2", dayTS(1, 40), "m", usage(100, 100, 0, 0, 0), 20000, ""),
		sessionEnd(3, dayTS(1, 50), "crash_inferred"),
	)
	writeSession(t, filepath.Join(root, "t/session.jsonl"),
		created(1, dayTS(1, 0), "r3"),
		completed(2, "m3", dayTS(1, 20), "m", usage(100, 100, 0, 0, 0), 20000, ""),
	)

	source := Load(Filters{Root: root})
	// Only the main log's exit is a session outcome; the child exit is
	// ignored and the session without session_end has no entry.
	if source.Outcomes["s"] != "clean" {
		t.Fatalf("outcomes = %v", source.Outcomes)
	}
	if _, ok := source.Outcomes["t"]; ok {
		t.Fatalf("outcomes = %v, want no entry for t", source.Outcomes)
	}
	if _, ok := source.Outcomes["sub-1"]; ok {
		t.Fatalf("outcomes = %v, want no child entry", source.Outcomes)
	}
}

func TestLoadWorkspaceBranchLabels(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	writeSession(t, filepath.Join(sessions, "s/session.jsonl"),
		branchEvent(1, dayTS(1, 0), "feat", "/repo/event"),
		created(2, dayTS(1, 10), "r1"),
		completed(3, "m1", dayTS(1, 20), "m", usage(100, 100, 0, 0, 0), 20000, ""),
		branchEvent(4, dayTS(1, 30), "main", "/repo/event"),
		// A later branchless event keeps the last known branch.
		branchEvent(5, dayTS(1, 40), "", "/repo/event"),
	)
	writeSession(t, filepath.Join(sessions, "t/session.jsonl"),
		created(1, dayTS(1, 10), "r2"),
		completed(2, "m2", dayTS(1, 20), "m", usage(100, 100, 0, 0, 0), 20000, ""),
	)
	dbPath := filepath.Join(root, "session-index.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (session_id TEXT, workspace_root TEXT, git_branch TEXT,
		title TEXT, effective_long_title TEXT, msp_title TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO sessions VALUES ('s', '/repo/index', 'old', NULL, NULL, NULL),
		('t', '/repo/index-t', 'idx-branch', NULL, NULL, NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	source := Load(Filters{Root: sessions})
	bySession := map[string]*Row{}
	for _, row := range source.Rows {
		bySession[row.SessionID] = row
	}
	// Latest in-session observation wins over the index.
	row := bySession["s"]
	if row.ProjectPath != "/repo/event" || row.Branch == nil || *row.Branch != "main" {
		t.Fatalf("event labels = %q/%v", row.ProjectPath, row.Branch)
	}
	// Without events the index is the fallback.
	row = bySession["t"]
	if row.ProjectPath != "/repo/index-t" || row.Branch == nil || *row.Branch != "idx-branch" {
		t.Fatalf("index labels = %q/%v", row.ProjectPath, row.Branch)
	}
	if len(source.Events.Branches) != 2 {
		t.Fatalf("branches = %v", source.Events.Branches)
	}
}

func TestParentSessionID(t *testing.T) {
	cases := map[string]string{
		"root/2026/11/01/sess-a/session.jsonl":         "sess-a",
		"root/s/subagent/sub-1/session.jsonl":          "s",
		"root/s/subagent/c1/subagent/g1/session.jsonl": "s",
	}
	for path, want := range cases {
		if got := parentSessionID(path); got != want {
			t.Errorf("parentSessionID(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestLoadMissingRootIsEmpty(t *testing.T) {
	source := Load(Filters{Root: filepath.Join(t.TempDir(), "nope")})
	if len(source.Rows) != 0 || len(source.Files) != 0 {
		t.Fatalf("expected empty source, got %+v", source.Counts)
	}
}

func TestTimingBucket(t *testing.T) {
	sec := func(v float64) *float64 { return &v }
	cases := []struct {
		dt   *float64
		out  int64
		want string
	}{
		{nil, 100, "noTiming"},
		{sec(0), 100, "noTiming"},
		{sec(10), 19, "tooShort"},
		{sec(0.4), 100, "tooShortDuration"},
		{sec(181), 100, "tooLong"},
		{sec(100), 100, "slowRate"},
		{sec(10), 5000, "fastRate"},
		{sec(10), 200, "plausible"},
	}
	for _, tc := range cases {
		if got := TimingBucket(tc.dt, tc.out); got != tc.want {
			t.Errorf("TimingBucket(%v, %d) = %q, want %q", tc.dt, tc.out, got, tc.want)
		}
	}
}
