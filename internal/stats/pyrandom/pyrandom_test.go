// SPDX-License-Identifier: AGPL-3.0-only

package pyrandom

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

type randomOp struct {
	Kind string `json:"kind"`
	K    uint   `json:"k"`
	N    string `json:"n"`
	Want string `json:"want"`
}

type randomCase struct {
	Seed int64      `json:"seed"`
	Ops  []randomOp `json:"ops"`
}

func loadRandomVectors(t *testing.T) []randomCase {
	t.Helper()
	path := filepath.Join("..", "testdata", "pyrandom.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors %s: %v", path, err)
	}
	var file struct {
		Cases []randomCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse vectors %s: %v", path, err)
	}
	if len(file.Cases) == 0 {
		t.Fatal("pyrandom vectors are empty")
	}
	return file.Cases
}

func parseWantFloat(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("invalid vector float %q: %v", s, err)
	}
	return v
}

func TestCPythonParityVectors(t *testing.T) {
	for _, testCase := range loadRandomVectors(t) {
		rng := New(testCase.Seed)
		for i, op := range testCase.Ops {
			switch op.Kind {
			case "bits":
				got := strconv.FormatUint(rng.GetRandBits(op.K), 10)
				if got != op.Want {
					t.Fatalf("seed %d op %d: getrandbits(%d) = %s, want %s",
						testCase.Seed, i, op.K, got, op.Want)
				}
			case "below":
				n, err := strconv.ParseUint(op.N, 10, 64)
				if err != nil {
					t.Fatalf("seed %d op %d: bad n %q: %v", testCase.Seed, i, op.N, err)
				}
				got := strconv.FormatUint(rng.RandBelow(n), 10)
				if got != op.Want {
					t.Fatalf("seed %d op %d: randrange(%s) = %s, want %s",
						testCase.Seed, i, op.N, got, op.Want)
				}
			case "float":
				got := rng.Float64()
				want := parseWantFloat(t, op.Want)
				if math.Float64bits(got) != math.Float64bits(want) {
					t.Fatalf("seed %d op %d: random() = %v (%016x), want %v (%016x)",
						testCase.Seed, i, got, math.Float64bits(got),
						want, math.Float64bits(want))
				}
			default:
				t.Fatalf("seed %d op %d: unknown kind %q", testCase.Seed, i, op.Kind)
			}
		}
	}
}

func TestKnownCPythonStreams(t *testing.T) {
	// Values produced by CPython 3.15: random.Random(seed).getrandbits(32).
	cases := []struct {
		seed int64
		want uint32
	}{
		{0, 3626764237},
		{1, 577090037},
		{42, 2746317213},
		{-42, 2746317213}, // CPython seeds on abs(seed)
	}
	for _, c := range cases {
		if got := New(c.seed).Uint32(); got != c.want {
			t.Errorf("first getrandbits(32) for seed %d = %d, want %d", c.seed, got, c.want)
		}
		if got := New(-c.seed).Uint32(); got != c.want {
			t.Errorf("negative seed %d stream differs from its absolute value", c.seed)
		}
	}
}

func TestSeedRangeAndReseed(t *testing.T) {
	// int64 minimum must seed from abs(seed) == 2**63 without overflow.
	a := New(math.MinInt64)
	b := New(math.MaxInt64)
	if a.Uint32() == b.Uint32() {
		t.Fatal("distinct extreme seeds produced the same first draw")
	}

	rng := New(42)
	first := rng.Uint32()
	rng.Seed(42)
	if got := rng.Uint32(); got != first {
		t.Fatalf("Seed did not reset the stream: %d vs %d", got, first)
	}
}

func TestGetRandBitsZeroDoesNotConsume(t *testing.T) {
	a := New(123)
	if got := a.GetRandBits(0); got != 0 {
		t.Fatalf("GetRandBits(0) = %d, want 0", got)
	}
	b := New(123)
	if x, y := a.Uint32(), b.Uint32(); x != y {
		t.Fatalf("GetRandBits(0) consumed MT state: %d vs %d", x, y)
	}
}

func TestRandBelowBounds(t *testing.T) {
	rng := New(7)
	for _, n := range []uint64{1, 2, 5, 100, 1 << 20, math.MaxUint64} {
		for i := 0; i < 100; i++ {
			if got := rng.RandBelow(n); got >= n {
				t.Fatalf("RandBelow(%d) returned %d", n, got)
			}
		}
	}
}

func TestFloat64Range(t *testing.T) {
	rng := New(99)
	for i := 0; i < 1000; i++ {
		v := rng.Float64()
		if v < 0 || v >= 1 {
			t.Fatalf("Float64 out of range: %v", v)
		}
	}
}

func TestRandRangePanicsOnEmptyRange(t *testing.T) {
	rng := New(1)
	defer func() {
		if recover() == nil {
			t.Fatal("RandRange(0) did not panic")
		}
	}()
	rng.RandRange(0)
}

func TestGetRandBitsPanicsAbove64(t *testing.T) {
	rng := New(1)
	defer func() {
		if recover() == nil {
			t.Fatal("GetRandBits(65) did not panic")
		}
	}()
	_ = rng.GetRandBits(65)
}

func BenchmarkUint32(b *testing.B) {
	rng := New(42)
	for i := 0; i < b.N; i++ {
		_ = rng.Uint32()
	}
}

func BenchmarkRandBelow(b *testing.B) {
	rng := New(42)
	for i := 0; i < b.N; i++ {
		_ = rng.RandBelow(1000)
	}
}
