// SPDX-License-Identifier: AGPL-3.0-only

package datewindow

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name          string
		since, until  string
		wantErrSubstr string
	}{
		{"empty", "", "", ""},
		{"single day", "2026-09-02", "2026-09-02", ""},
		{"ordered range", "2026-01-01", "2026-12-31", ""},
		{"leap day", "2024-02-29", "2024-03-01", ""},
		{"bad since", "2026-13-45", "", `invalid --since "2026-13-45": want YYYY-MM-DD`},
		{"bad until", "", "2026-02-30", `invalid --until "2026-02-30": want YYYY-MM-DD`},
		{"non padded", "2026-9-2", "", `invalid --since "2026-9-2": want YYYY-MM-DD`},
		{"inverted", "2026-05-01", "2026-01-01", "--since 2026-05-01 is after --until 2026-01-01"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := Validate(test.since, test.until)
			if test.wantErrSubstr == "" {
				if err != nil {
					t.Fatalf("Validate(%q, %q) = %v", test.since, test.until, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErrSubstr) {
				t.Fatalf("Validate(%q, %q) = %v, want %q", test.since, test.until, err, test.wantErrSubstr)
			}
		})
	}
}
