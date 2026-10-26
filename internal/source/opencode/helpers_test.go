// SPDX-License-Identifier: AGPL-3.0-only

package ocsource

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestMergeIntervals(t *testing.T) {
	cases := []struct {
		name      string
		intervals []Interval
		wantUnion float64
		wantSpan  float64
	}{
		{"empty", nil, 0, 0},
		{"single", []Interval{{1000, 2500}}, 1.5, 1.5},
		{"disjoint", []Interval{{1000, 2000}, {3000, 3500}}, 1.5, 2.5},
		{"overlap", []Interval{{1000, 2000}, {1500, 3000}}, 2.0, 2.0},
		{"touching", []Interval{{1000, 2000}, {2000, 3000}}, 2.0, 2.0},
		{"contained", []Interval{{1000, 4000}, {2000, 3000}}, 3.0, 3.0},
		{"unordered", []Interval{{3000, 4000}, {1000, 2000}, {1500, 3500}}, 3.0, 3.0},
		{"three gaps", []Interval{{1000, 1100}, {1200, 1300}, {2000, 2500}}, 0.7, 1.5},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			unionSeconds, spanSeconds := MergeIntervals(testCase.intervals)
			if math.Abs(unionSeconds-testCase.wantUnion) > 1e-12 {
				t.Fatalf("union = %v, want %v", unionSeconds, testCase.wantUnion)
			}
			if math.Abs(spanSeconds-testCase.wantSpan) > 1e-12 {
				t.Fatalf("span = %v, want %v", spanSeconds, testCase.wantSpan)
			}
		})
	}
}

func TestMergeIntervalsDoesNotMutateInput(t *testing.T) {
	intervals := []Interval{{3000, 4000}, {1000, 2000}}
	MergeIntervals(intervals)
	if intervals[0].Start != 3000 || intervals[1].Start != 1000 {
		t.Fatalf("input was reordered: %v", intervals)
	}
}

func TestAllocateToolTime(t *testing.T) {
	t.Run("no intervals", func(t *testing.T) {
		calls := []ToolCall{{Name: "read"}, {Name: "bash"}}
		allocations := AllocateToolTime(calls)
		if len(allocations) != 2 || allocations[0] != 0 || allocations[1] != 0 {
			t.Fatalf("allocations: %v", allocations)
		}
	})

	t.Run("non-overlapping keeps runtime", func(t *testing.T) {
		calls := []ToolCall{
			{Name: "a", Ran: 1000, Completed: 2000},
			{Name: "b", Ran: 3000, Completed: 3500},
		}
		allocations := AllocateToolTime(calls)
		if math.Abs(allocations[0]-1.0) > 1e-12 || math.Abs(allocations[1]-0.5) > 1e-12 {
			t.Fatalf("allocations: %v", allocations)
		}
	})

	t.Run("overlap scales to the union", func(t *testing.T) {
		calls := []ToolCall{
			{Name: "a", Ran: 1000, Completed: 2000},
			{Name: "b", Ran: 1500, Completed: 2500},
		}
		allocations := AllocateToolTime(calls)
		// union 1.5s, sum 2.0s, scale 0.75.
		if math.Abs(allocations[0]-0.75) > 1e-12 || math.Abs(allocations[1]-0.75) > 1e-12 {
			t.Fatalf("allocations: %v", allocations)
		}
		var sum float64
		for _, value := range allocations {
			sum += value
		}
		if math.Abs(sum-1.5) > 1e-12 {
			t.Fatalf("allocations must sum to the union, got %v", sum)
		}
	})

	t.Run("invalid calls are skipped", func(t *testing.T) {
		calls := []ToolCall{
			// ran == 0 is falsy in Python's `if ran and done and done > ran`,
			// so a call without a start timestamp never allocates.
			{Name: "no-ran", Ran: 0, Completed: 2000},
			{Name: "inverted", Ran: 2000, Completed: 1000},
			{Name: "ok", Ran: 1000, Completed: 2000},
		}
		allocations := AllocateToolTime(calls)
		if allocations[0] != 0 || allocations[1] != 0 {
			t.Fatalf("invalid calls must not allocate: %v", allocations)
		}
		if math.Abs(allocations[2]-1.0) > 1e-12 {
			t.Fatalf("valid call allocation: %v", allocations[2])
		}
	})
}

func TestCounterMostCommonStableTies(t *testing.T) {
	counter := NewCounter()
	for _, key := range []string{"idle", "shell", "idle", "compaction", "shell", "compaction", "agent-switched"} {
		counter.Inc(key)
	}
	items := counter.MostCommon()
	got := make([]string, len(items))
	for i, item := range items {
		got[i] = item.Key
	}
	want := []string{"idle", "shell", "compaction", "agent-switched"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("most_common order = %v, want %v", got, want)
	}
	if counter.Get("idle") != 2 || counter.Total() != 7 || counter.Len() != 4 {
		t.Fatalf("counter stats: %+v", items)
	}
	if items[0].Count != 2 || items[3].Count != 1 {
		t.Fatalf("counts: %v", items)
	}
}

func TestNormalizePayloadDropsIdentifiers(t *testing.T) {
	payload := map[string]any{
		"id":           "msg_1",
		"sessionID":    "ses_1",
		"path":         "/tmp/x",
		"time_created": 123,
		"nested": map[string]any{
			"callID": "call_1",
			"keep":   "yes",
		},
		"list": []any{
			map[string]any{"messageID": "m", "value": 5},
			7,
		},
	}
	normalized := NormalizePayload(payload).(map[string]any)
	for _, dropped := range []string{"id", "sessionID", "path", "time_created"} {
		if _, ok := normalized[dropped]; ok {
			t.Fatalf("key %q must be dropped", dropped)
		}
	}
	nested := normalized["nested"].(map[string]any)
	if _, ok := nested["callID"]; ok {
		t.Fatal("nested callID must be dropped")
	}
	if nested["keep"] != "yes" {
		t.Fatalf("nested keep: %v", nested["keep"])
	}
	list := normalized["list"].([]any)
	if _, ok := list[0].(map[string]any)["messageID"]; ok {
		t.Fatal("list messageID must be dropped")
	}
	if list[1] != 7 {
		t.Fatalf("scalar list item: %v", list[1])
	}
}

// TestSignatureMatchesPython pins the dedup signature to Python:
//
//	hashlib.sha256(json.dumps(normalize_payload(x), sort_keys=True,
//	    separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()
//
// The expected digest was produced by the reference implementation for this
// exact payload (non-ASCII text, a float, dropped ids at two levels).
func TestSignatureMatchesPython(t *testing.T) {
	raw := `{"id":"msg_1","sessionID":"ses_1","cost":0.0125,"tokens":{"input":100,"output":50},` +
		`"content":[{"type":"text","text":"café ☕"},{"type":"tool","name":"read","time":{"ran":1}}],` +
		`"nested":{"path":"/tmp/a","keep":true,"ratio":1.5}}`
	var doc map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	canonical, err := CanonicalJSON(NormalizePayload(doc))
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	wantCanonical := `{"content":[{"text":"café ☕","type":"text"},{"name":"read","time":{"ran":1},"type":"tool"}],` +
		`"cost":0.0125,"nested":{"keep":true,"ratio":1.5},"tokens":{"input":100,"output":50}}`
	if canonical != wantCanonical {
		t.Fatalf("canonical JSON mismatch:\n got %s\nwant %s", canonical, wantCanonical)
	}
	signature, err := Signature(NormalizePayload(doc))
	if err != nil {
		t.Fatalf("signature: %v", err)
	}
	// sha256 of wantCanonical, computed by Python (hashlib.sha256(...).hexdigest()).
	const wantSignature = "01f6f38e6397ae49ab289a6a55870d7b84b467968885a7280f0f4e4594bc8d14"
	if signature != wantSignature {
		t.Fatalf("signature = %s, want %s", signature, wantSignature)
	}
	// A second call must be stable, and a one-character change must differ.
	repeated, err := Signature(NormalizePayload(doc))
	if err != nil || repeated != signature {
		t.Fatalf("signature is not stable: %v %v", repeated, err)
	}
	doc["cost"] = json.Number("0.0126")
	changed, err := Signature(NormalizePayload(doc))
	if err != nil {
		t.Fatalf("signature: %v", err)
	}
	if changed == signature {
		t.Fatal("a changed payload must change the signature")
	}
}
