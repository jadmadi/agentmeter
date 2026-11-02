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

func TestWidth(t *testing.T) {
	t.Setenv("COLUMNS", "42")
	if got := Width(); got != 42 {
		t.Errorf("Width = %d, want 42", got)
	}
	t.Setenv("COLUMNS", "nope")
	if got := Width(); got != 80 {
		t.Errorf("Width fallback = %d, want 80", got)
	}
}

func TestWrapLine(t *testing.T) {
	got := wrapLine("alpha beta gamma delta", 12)
	for _, line := range got {
		if len([]rune(line)) > 12 {
			t.Errorf("line %q exceeds 12", line)
		}
	}
	if strings.Join(got, " ") != "alpha beta gamma delta" {
		t.Errorf("wrapLine lost words: %q", got)
	}
	long := "https://example.com/a/very/long/unbreakable/path"
	got = wrapLine(long, 10)
	if joined := strings.Join(got, ""); joined != long {
		t.Errorf("wrapLine long token = %q", joined)
	}
	for _, line := range got {
		if len([]rune(line)) > 10 {
			t.Errorf("line %q exceeds 10", line)
		}
	}
	spaced := strings.Repeat(" ", 50) + strings.Repeat("x", 100)
	got = wrapLine(spaced, 10)
	if joined := strings.Join(got, ""); joined != strings.Repeat("x", 100) {
		t.Errorf("wrapLine indented long line = %q", joined)
	}
}

func TestWrapLineANSI(t *testing.T) {
	line := "\033[1m" + strings.Repeat("-", 20) + "\033[0m"
	parts := wrapLine(line, 10)
	if joined := strings.Join(parts, ""); joined != line {
		t.Errorf("ANSI wrap lost escapes: %q", joined)
	}
	for _, part := range parts {
		if visibleLen(part) > 10 {
			t.Errorf("ANSI line %q has visible width %d", part, visibleLen(part))
		}
	}
}

func TestLineWriterWraps(t *testing.T) {
	var out strings.Builder
	writer := NewLineWriter(&out, 10)
	if _, err := writer.Write([]byte("alpha beta gamma delta\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if len([]rune(line)) > 10 {
			t.Errorf("line %q exceeds 10", line)
		}
	}
}
