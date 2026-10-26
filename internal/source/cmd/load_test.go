// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Pin the process zone: local dates, hourly buckets and meta.timezone all
	// depend on time.Local. Golden data is generated with TZ=UTC.
	time.Local = time.UTC
	os.Exit(m.Run())
}

// fixtureRoot resolves command-code-telemetry/testdata/fixture from the
// package directory.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "command-code-telemetry", "testdata", "fixture"))
	if err != nil {
		t.Fatalf("resolve fixture: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("fixture missing at %s: %v", root, err)
	}
	return root
}

func TestLoadFixtureCounts(t *testing.T) {
	source := Load(Filters{Root: fixtureRoot(t), IncludeTitles: true})
	if got, want := len(source.Files), 8; got != want {
		t.Fatalf("files = %d, want %d (%v)", got, want, source.Files)
	}
	counts := source.Counts
	checks := []struct {
		name      string
		got, want int64
	}{
		{"files", counts.Files, 8},
		{"scanned", counts.Scanned, 59},
		{"assistant", counts.Assistant, 59},
		{"considered", counts.Considered, 59},
		{"rows", counts.Rows, 54},
		{"duplicates", counts.Duplicates, 2},
		{"noOutput", counts.NoOutput, 2},
		{"noTimestamp", counts.NoTimestamp, 1},
		{"filteredModel", counts.FilteredModel, 0},
		{"filteredDate", counts.FilteredDate, 0},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %d, want %d", check.name, check.got, check.want)
		}
	}
	if got, want := int64(len(source.Rows)), counts.Rows; got != want {
		t.Errorf("len(rows) = %d, want %d", got, want)
	}
	if got, want := len(source.Sessions), 7; got != want {
		t.Errorf("sessions = %d, want %d", got, want)
	}
	timing := map[string]int64{
		"plausible": 47, "noTiming": 2, "tooShort": 1, "tooShortDuration": 1,
		"tooLong": 1, "slowRate": 1, "fastRate": 1,
	}
	for reason, want := range timing {
		if got := source.Timing[reason]; got != want {
			t.Errorf("timing[%s] = %d, want %d", reason, got, want)
		}
	}
	var total int64
	for _, count := range source.Timing {
		total += count
	}
	if total != int64(len(source.Rows)) {
		t.Errorf("timing total = %d, want %d", total, len(source.Rows))
	}
}

func TestLoadFixtureRowOrderAndDedup(t *testing.T) {
	source := Load(Filters{Root: fixtureRoot(t), IncludeTitles: true})
	if len(source.Rows) == 0 {
		t.Fatal("no rows")
	}
	// Files are visited in lexicographic order: alpha/deep/session-b.jsonl
	// first, so its dup-1 copy is the kept one.
	if got, want := source.Rows[0].ID, "dup-1"; got != want {
		t.Errorf("first row id = %q, want %q", got, want)
	}
	// File order is preserved within a file (insertion order of message ids).
	if got, want := source.Rows[1].ID, "b-0"; got != want {
		t.Errorf("second row id = %q, want %q", got, want)
	}
	// The ledger keeps the Python order: noOutput, noTimestamp, duplicate...
	reasons := make([]string, 0, len(source.Ledger))
	for _, entry := range source.Ledger {
		reasons = append(reasons, entry.Reason+":"+entry.ID)
	}
	want := []string{
		"noOutput:a-noout",
		"noTimestamp:a-nots",
		"duplicate:dup-1",
		"noOutput:bad-utf8",
		"duplicate:dup-1",
	}
	if len(reasons) != len(want) {
		t.Fatalf("ledger = %v, want %v", reasons, want)
	}
	for index := range want {
		if reasons[index] != want[index] {
			t.Errorf("ledger[%d] = %q, want %q", index, reasons[index], want[index])
		}
	}
	// The invalid-UTF-8 record survives byte stripping and counts as empty.
	var badUTF8 *LedgerEntry
	for _, entry := range source.Ledger {
		if entry.ID == "bad-utf8" {
			badUTF8 = entry
		}
	}
	if badUTF8 == nil {
		t.Fatal("bad-utf8 record was dropped")
	}
	if badUTF8.OutputTokens != 0 || badUTF8.SessionID != "sess-alpha" {
		t.Errorf("bad-utf8 ledger = %+v", badUTF8)
	}
}

func TestLoadFixtureTimingAndParent(t *testing.T) {
	source := Load(Filters{Root: fixtureRoot(t), IncludeTitles: true})
	byID := map[string]*Row{}
	for _, row := range source.Rows {
		byID[row.ID] = row
	}
	row := byID["a-0"]
	if row == nil {
		t.Fatal("a-0 missing")
	}
	if row.DT == nil || *row.DT != 4.0 {
		t.Errorf("a-0 dt = %v, want 4.0", row.DT)
	}
	if row.TPS == nil || *row.TPS != 25.0 {
		t.Errorf("a-0 tps = %v, want 25", row.TPS)
	}
	if row.Timing != "plausible" || row.Tool {
		t.Errorf("a-0 timing = %q tool = %v", row.Timing, row.Tool)
	}
	if row.Effort != "high" || !row.Thinking {
		t.Errorf("a-0 effort/thinking = %q/%v", row.Effort, row.Thinking)
	}
	if row.SessionTitle == nil || *row.SessionTitle != "Alpha ✓ session" {
		t.Errorf("a-0 title = %v", row.SessionTitle)
	}
	if row.Entrypoint != "print" || row.EntrypointRaw == nil || *row.EntrypointRaw != "print" {
		t.Errorf("a-0 entrypoint = %q raw = %v", row.Entrypoint, row.EntrypointRaw)
	}
	if row.Branch == nil || *row.Branch != "feature/hooks" {
		t.Errorf("a-0 branch = %v, want feature/hooks (last truthy gitBranch in file)", row.Branch)
	}
	if got := byID["a-noparent"].Timing; got != "noTiming" {
		t.Errorf("a-noparent timing = %q", got)
	}
	if byID["a-noparent"].DT != nil {
		t.Errorf("a-noparent dt = %v, want nil", byID["a-noparent"].DT)
	}
	if got := byID["a-negparent"].Timing; got != "noTiming" {
		t.Errorf("a-negparent timing = %q (negative duration must be dropped)", got)
	}
	if got := byID["a-fast"].Timing; got != "fastRate" {
		t.Errorf("a-fast timing = %q", got)
	}
	if got := byID["a-slow"].Timing; got != "slowRate" {
		t.Errorf("a-slow timing = %q", got)
	}
	if got := byID["a-tooshort"].Timing; got != "tooShort" {
		t.Errorf("a-tooshort timing = %q", got)
	}
	if got := byID["a-shortdur"].Timing; got != "tooShortDuration" {
		t.Errorf("a-shortdur timing = %q", got)
	}
	if got := byID["a-long"].Timing; got != "tooLong" {
		t.Errorf("a-long timing = %q", got)
	}
	if got := byID["a-nomodel"]; got.Model != "" || got.Effort != "high" {
		t.Errorf("a-nomodel model/effort = %q/%q", got.Model, got.Effort)
	}
	web := byID["a-web"]
	if !web.Web || len(web.Tools) != 1 || web.Tools[0] != "web_search" {
		t.Errorf("a-web web/tools = %v/%v", web.Web, web.Tools)
	}
	// Multi-tool step keeps call order and equal-share details.
	multi := byID["a-2"]
	if len(multi.ToolDetails) != 2 || multi.ToolDetails[0].Name != "grep" || multi.ToolDetails[1].Name != "bash" {
		t.Errorf("a-2 tools = %v", multi.ToolDetails)
	}
	if multi.PrimaryTool == nil || *multi.PrimaryTool != "grep" || multi.NTools != 2 {
		t.Errorf("a-2 primary/nTools = %v/%d", multi.PrimaryTool, multi.NTools)
	}
	// The unknown project and file-stem session id.
	unknown := byID["e-0"]
	if unknown.ProjectPath != "(unknown)" || unknown.SessionID != "session-e" {
		t.Errorf("e-0 project/session = %q/%q", unknown.ProjectPath, unknown.SessionID)
	}
	if unknown.Entrypoint != "interactive" || unknown.EntrypointRaw != nil {
		t.Errorf("e-0 entrypoint = %q raw = %v", unknown.Entrypoint, unknown.EntrypointRaw)
	}
}

func TestLoadFixtureEventsAndWatermark(t *testing.T) {
	source := Load(Filters{Root: fixtureRoot(t), IncludeTitles: true})
	events := source.Events
	if events.Compactions != 1 || events.CompactionTokens != 150000 {
		t.Errorf("compactions = %d/%d", events.Compactions, events.CompactionTokens)
	}
	if len(events.CompactionEvents) != 1 || events.CompactionEvents[0].SessionID == nil {
		t.Errorf("compaction events = %+v", events.CompactionEvents)
	}
	if events.ModelChanges != 2 || len(events.ModelChangeEvents) != 2 {
		t.Errorf("model changes = %d", events.ModelChanges)
	}
	if got := events.ModelChangeEvents[1].Model; got != "TestCorp/Fast-1" {
		t.Errorf("model_change fallback to 'to' = %q", got)
	}
	if events.FileSnapshots != 1 {
		t.Errorf("file snapshots = %d", events.FileSnapshots)
	}
	if len(events.Hooks) != 6 {
		t.Errorf("hooks = %d", len(events.Hooks))
	}
	if len(events.ToolWindows) != 1 {
		t.Fatalf("tool windows = %+v", events.ToolWindows)
	}
	if math.Abs(events.ToolWindows[0].Seconds-3.0) > 1e-12 {
		t.Errorf("tool window = %v, want 3.0", events.ToolWindows[0].Seconds)
	}
	if events.ToolWindows[0].Tool == nil || *events.ToolWindows[0].Tool != "read_file" {
		t.Errorf("tool window tool = %v", events.ToolWindows[0].Tool)
	}
	branches := sortedKeys(events.Branches)
	wantBranches := []string{"feature/hooks", "main", "work"}
	if len(branches) != len(wantBranches) {
		t.Fatalf("branches = %v", branches)
	}
	for index := range wantBranches {
		if branches[index] != wantBranches[index] {
			t.Errorf("branch[%d] = %q, want %q", index, branches[index], wantBranches[index])
		}
	}
	if source.MaxTS == nil {
		t.Fatal("watermark missing")
	}
	if got, want := UnixFloat(*source.MaxTS), 1788717665.0; got != want {
		t.Errorf("maxTs = %v, want %v", got, want)
	}
}

func TestLoadEmptyAndMissingRoot(t *testing.T) {
	for _, root := range []string{
		filepath.Join(t.TempDir(), "missing"),
		t.TempDir(),
	} {
		source := Load(Filters{Root: root, IncludeTitles: true})
		if len(source.Files) != 0 || len(source.Rows) != 0 || len(source.Ledger) != 0 {
			t.Errorf("root %s: files=%d rows=%d ledger=%d", root, len(source.Files), len(source.Rows), len(source.Ledger))
		}
		if source.MaxTS != nil || len(source.Sessions) != 0 {
			t.Errorf("root %s: watermark/sessions not empty", root)
		}
		for _, reason := range TimingReasons {
			if source.Timing[reason] != 0 {
				t.Errorf("root %s: timing[%s] = %d", root, reason, source.Timing[reason])
			}
		}
	}
}

func TestModelFilterOrdering(t *testing.T) {
	unfiltered := Load(Filters{Root: fixtureRoot(t), IncludeTitles: true})
	filtered := Load(Filters{Root: fixtureRoot(t), IncludeTitles: true, ModelFilters: []string{"SLOW"}})
	// Matching is a case-insensitive substring of the model id.
	for _, row := range filtered.Rows {
		if row.Model != "OtherLab/Slow-2" {
			t.Fatalf("unexpected model %q survived the filter", row.Model)
		}
	}
	// Every assistant record that does not match is counted before dedup and
	// before the output check.
	wantFiltered := unfiltered.Counts.Scanned - filtered.Counts.Considered
	if filtered.Counts.FilteredModel != wantFiltered {
		t.Errorf("filteredModel = %d, want %d", filtered.Counts.FilteredModel, wantFiltered)
	}
	if filtered.Counts.NoOutput != 0 || filtered.Counts.Duplicates != 0 {
		t.Errorf("filter must run before dedup/output checks: noOutput=%d duplicates=%d",
			filtered.Counts.NoOutput, filtered.Counts.Duplicates)
	}
	// The watermark still sees filtered-out rows.
	if filtered.MaxTS == nil || unfiltered.MaxTS == nil || !filtered.MaxTS.Equal(*unfiltered.MaxTS) {
		t.Errorf("watermark changed by the model filter")
	}
}

func TestDateFilterOrdering(t *testing.T) {
	source := Load(Filters{Root: fixtureRoot(t), IncludeTitles: true, Since: "2026-09-02", Until: "2026-09-03"})
	for _, row := range source.Rows {
		day := LocalDate(row.TS)
		if day < "2026-09-02" || day > "2026-09-03" {
			t.Fatalf("row %s on %s survived the date filter", row.ID, day)
		}
	}
	if source.Counts.FilteredDate == 0 {
		t.Fatal("expected date-filtered rows")
	}
	// The date filter runs after dedup: filtered rows never reach the ledger.
	if source.Counts.Duplicates != 2 || source.Counts.NoOutput != 2 {
		t.Errorf("counts changed by date filter: duplicates=%d noOutput=%d",
			source.Counts.Duplicates, source.Counts.NoOutput)
	}
	for _, row := range source.Rows {
		if LocalDate(row.TS) < "2026-09-02" {
			t.Errorf("row %s outside window", row.ID)
		}
	}
}

func TestSeenBeforeTimestamp(t *testing.T) {
	root := t.TempDir()
	// Two files: the timestamp-less copy is read first and marks "seen", so the
	// dated copy in the later file is a duplicate, not a row.
	writeJSONL(t, filepath.Join(root, "a.jsonl"), []string{
		`{"type":"session","id":"s1","cwd":"/p"}`,
		`{"type":"message","id":"m1","message":{"role":"assistant","content":[]},"usage":{"outputTokens":100,"inputTokens":10,"costUsd":0.1},"model":"m","timestamp":"2026-09-01T08:00:00Z"}`,
		`{"type":"message","id":"m2","message":{"role":"assistant","content":[]},"usage":{"outputTokens":100,"inputTokens":10,"costUsd":0.1},"model":"m"}`,
	})
	writeJSONL(t, filepath.Join(root, "b.jsonl"), []string{
		`{"type":"session","id":"s1","cwd":"/p"}`,
		`{"type":"message","id":"m2","message":{"role":"assistant","content":[]},"usage":{"outputTokens":100,"inputTokens":10,"costUsd":0.1},"model":"m","timestamp":"2026-09-01T09:00:00Z"}`,
	})
	source := Load(Filters{Root: root, IncludeTitles: true})
	// The first m2 has no timestamp and is dropped; "seen" was already marked,
	// so the second m2 is a duplicate, not a row.
	if source.Counts.NoTimestamp != 1 || source.Counts.Duplicates != 1 || source.Counts.Rows != 1 {
		t.Fatalf("counts = %+v", source.Counts)
	}
	reasons := []string{}
	for _, entry := range source.Ledger {
		reasons = append(reasons, entry.ID+"="+entry.Reason)
	}
	if len(reasons) != 2 || reasons[0] != "m2=noTimestamp" || reasons[1] != "m2=duplicate" {
		t.Fatalf("ledger = %v", reasons)
	}
}

func TestNoOutputBeforeDedup(t *testing.T) {
	root := t.TempDir()
	// The empty copy comes first and is a ledger row; it does not mark "seen",
	// so the non-empty copy in the later file becomes the step.
	writeJSONL(t, filepath.Join(root, "a.jsonl"), []string{
		`{"type":"session","id":"s1","cwd":"/p"}`,
		`{"type":"message","id":"m1","message":{"role":"assistant"},"usage":{"outputTokens":0},"model":"m","timestamp":"2026-09-01T08:00:00Z"}`,
	})
	writeJSONL(t, filepath.Join(root, "b.jsonl"), []string{
		`{"type":"session","id":"s1","cwd":"/p"}`,
		`{"type":"message","id":"m1","message":{"role":"assistant"},"usage":{"outputTokens":50},"model":"m","timestamp":"2026-09-01T09:00:00Z"}`,
	})
	source := Load(Filters{Root: root, IncludeTitles: true})
	// The empty copy is a ledger row and does not mark "seen".
	if source.Counts.NoOutput != 1 || source.Counts.Rows != 1 || source.Counts.Duplicates != 0 {
		t.Fatalf("counts = %+v", source.Counts)
	}
	if source.Ledger[0].Reason != ReasonNoOutput {
		t.Fatalf("ledger = %+v", source.Ledger[0])
	}
}

func TestRepeatedMessageIDKeepsFirstOrder(t *testing.T) {
	root := t.TempDir()
	writeJSONL(t, filepath.Join(root, "one.jsonl"), []string{
		`{"type":"session","id":"s1","cwd":"/p"}`,
		`{"type":"message","id":"a","message":{"role":"assistant"},"usage":{"outputTokens":10},"model":"m","timestamp":"2026-09-01T08:00:00Z"}`,
		`{"type":"message","id":"b","message":{"role":"assistant"},"usage":{"outputTokens":20},"model":"m","timestamp":"2026-09-01T08:01:00Z"}`,
		`{"type":"message","id":"a","message":{"role":"assistant"},"usage":{"outputTokens":30},"model":"m","timestamp":"2026-09-01T08:02:00Z"}`,
	})
	source := Load(Filters{Root: root, IncludeTitles: true})
	if len(source.Rows) != 2 {
		t.Fatalf("rows = %d", len(source.Rows))
	}
	// "a" keeps its first insertion slot but carries the last record.
	if source.Rows[0].ID != "a" || source.Rows[0].Out != 30 {
		t.Errorf("row0 = %s/%d", source.Rows[0].ID, source.Rows[0].Out)
	}
	if source.Rows[1].ID != "b" {
		t.Errorf("row1 = %s", source.Rows[1].ID)
	}
}

func TestDiscoverFiles(t *testing.T) {
	root := t.TempDir()
	writeJSONL(t, filepath.Join(root, "b.jsonl"), []string{`{}`})
	writeJSONL(t, filepath.Join(root, "a.jsonl"), []string{`{}`})
	writeJSONL(t, filepath.Join(root, "nested", "c.jsonl"), []string{`{}`})
	writeJSONL(t, filepath.Join(root, ".hidden", "d.jsonl"), []string{`{}`})
	writeJSONL(t, filepath.Join(root, ".e.jsonl"), []string{`{}`})
	writeJSONL(t, filepath.Join(root, "f.JSONL"), []string{`{}`})
	writeJSONL(t, filepath.Join(root, "g.txt"), []string{`{}`})
	files := discoverFiles(root)
	want := []string{
		filepath.Join(root, "a.jsonl"),
		filepath.Join(root, "b.jsonl"),
		filepath.Join(root, "nested", "c.jsonl"),
	}
	if len(files) != len(want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	for index := range want {
		if files[index] != want[index] {
			t.Errorf("files[%d] = %q, want %q", index, files[index], want[index])
		}
	}

	if runtime.GOOS != "windows" {
		target := t.TempDir()
		writeJSONL(t, filepath.Join(target, "linked.jsonl"), []string{`{}`})
		if err := os.Symlink(target, filepath.Join(root, "linkdir")); err != nil {
			t.Skipf("symlink: %v", err)
		}
		for _, file := range discoverFiles(root) {
			if filepath.Base(file) == "linked.jsonl" {
				t.Errorf("symlinked directory was followed: %s", file)
			}
		}
	}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"\n", []string{""}},
		{"a\nb\n", []string{"a", "b"}},
		{"a\r\nb", []string{"a", "b"}},
		{"a\rb\r", []string{"a", "b"}},
		{"a", []string{"a"}},
		{"a\n\nb", []string{"a", "", "b"}},
	}
	for _, testCase := range cases {
		got := splitLines([]byte(testCase.input))
		if len(got) != len(testCase.want) {
			t.Errorf("%q -> %q, want %q", testCase.input, got, testCase.want)
			continue
		}
		for index := range testCase.want {
			if string(got[index]) != testCase.want[index] {
				t.Errorf("%q[%d] = %q, want %q", testCase.input, index, got[index], testCase.want[index])
			}
		}
	}
}

func TestReadLinesDropsInvalidUTF8(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jsonl")
	payload := append([]byte(`{"a":"`), 0xff, 0xfe)
	payload = append(payload, []byte(`b"}`+"\n")...)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := readLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || string(lines[0]) != `{"a":"b"}` {
		t.Fatalf("lines = %q", lines)
	}
}

func TestParseTimestamp(t *testing.T) {
	cases := []struct {
		value string
		want  string // RFC3339 in UTC, or "" for nil
	}{
		{"2026-09-01T08:00:00.500Z", "2026-09-01T08:00:00.5Z"},
		{"2026-09-01T08:00:00+03:00", "2026-09-01T05:00:00Z"},
		{"2026-09-01T08:00:00+0300", "2026-09-01T05:00:00Z"},
		{"2026-09-01 08:00:00.250", "2026-09-01T08:00:00.25Z"},
		{"2026-09-01T08:00", "2026-09-01T08:00:00Z"},
		{"2026-09-01", "2026-09-01T00:00:00Z"},
		{"", ""},
		{"not a timestamp", ""},
	}
	for _, testCase := range cases {
		parsed := parseTimestamp(testCase.value)
		if testCase.want == "" {
			if parsed != nil {
				t.Errorf("%q parsed to %v, want nil", testCase.value, parsed)
			}
			continue
		}
		if parsed == nil {
			t.Errorf("%q parsed to nil", testCase.value)
			continue
		}
		if got := parsed.UTC().Format(time.RFC3339Nano); got != testCase.want {
			t.Errorf("%q -> %s, want %s", testCase.value, got, testCase.want)
		}
	}
	subsecond := parseTimestamp("2026-09-01T08:00:00.123456789Z")
	if subsecond == nil || subsecond.Nanosecond() != 123456000 {
		t.Errorf("microsecond truncation: %v", subsecond)
	}
}

// TestDeltaSeconds pins the CPython timedelta.total_seconds() rounding: the
// exact integer microsecond count divided by 1e6, not a two-term sum.
func TestDeltaSeconds(t *testing.T) {
	want := float64(12351000) / 1e6
	if got := DeltaSeconds(12351 * time.Millisecond); got != want {
		t.Errorf("DeltaSeconds = %v, want %v", got, want)
	}
	if got, want := DeltaSeconds(193206*time.Millisecond), float64(193206000)/1e6; got != want {
		t.Errorf("DeltaSeconds = %v, want %v", got, want)
	}
}

func writeJSONL(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := ""
	for _, line := range lines {
		payload += line + "\n"
	}
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
