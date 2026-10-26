// SPDX-License-Identifier: AGPL-3.0-only

package terminal

import (
	"bytes"
	"strings"
	"testing"
)

func TestFit(t *testing.T) {
	if got := Fit("short", 10); got != "short" {
		t.Fatalf("Fit short = %q", got)
	}
	if got := Fit("abcdefghij", 7); got != "abcd..." {
		t.Fatalf("Fit long = %q", got)
	}
	if got := Fit("abcdef", 3); got != "abc" {
		t.Fatalf("Fit tiny = %q", got)
	}
}

func TestCountAndCompact(t *testing.T) {
	if got := Count(31201); got != "31,201" {
		t.Fatalf("Count = %q", got)
	}
	if got := Compact(23826437); got != "23.83M" {
		t.Fatalf("Compact = %q", got)
	}
	if got := Compact(142); got != "142" {
		t.Fatalf("Compact small = %q", got)
	}
}

func TestTableWidth(t *testing.T) {
	var buf bytes.Buffer
	Table(&buf,
		[]string{"MODEL", "STEPS", "GEN/S"},
		[][]string{{"a-very-long-model-name", "12,659", "211.8"}},
		[]bool{false, true, true},
		[]int{28, 7, 6},
	)
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if len([]rune(line)) > 98 {
			t.Fatalf("table line exceeds 98 columns: %q", line)
		}
	}
}
