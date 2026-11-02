// SPDX-License-Identifier: AGPL-3.0-only

package terminal

import (
	"strings"
	"testing"
)

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
