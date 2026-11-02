// SPDX-License-Identifier: AGPL-3.0-only

package render

import "testing"

func TestDisabledByEnv(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"default", map[string]string{"NO_COLOR": "", "TERM": "xterm-256color", "CLICOLOR": "1"}, false},
		{"no color", map[string]string{"NO_COLOR": "1"}, true},
		{"dumb term", map[string]string{"TERM": "dumb"}, true},
		{"clicolor zero", map[string]string{"CLICOLOR": "0"}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("TERM", "")
			t.Setenv("CLICOLOR", "")
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if got := disabledByEnv(); got != test.want {
				t.Errorf("disabledByEnv() = %v, want %v", got, test.want)
			}
		})
	}
}
