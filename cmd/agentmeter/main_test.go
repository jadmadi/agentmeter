// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalOut, originalErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outCh, errCh := make(chan string, 1), make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(outR)
		outCh <- string(data)
	}()
	go func() {
		data, _ := io.ReadAll(errR)
		errCh <- string(data)
	}()
	fn()
	os.Stdout, os.Stderr = originalOut, originalErr
	outW.Close()
	errW.Close()
	stdout = <-outCh
	stderr = <-errCh
	outR.Close()
	errR.Close()
	return stdout, stderr
}

func TestWithHelp(t *testing.T) {
	if got := withHelp([]string{"help"}); len(got) != 1 || got[0] != "--help" {
		t.Errorf("withHelp(help) = %v", got)
	}
	if got := withHelp([]string{"--json"}); len(got) != 1 || got[0] != "--json" {
		t.Errorf("withHelp(--json) = %v", got)
	}
}

func TestHelpTopicKnown(t *testing.T) {
	cases := []struct {
		topic string
		want  string
	}{
		{"cmd", "usage: agentmeter cmd"},
		{"command-code", "usage: agentmeter cmd"},
		{"oc", "OpenCode telemetry"},
		{"opencode", "OpenCode telemetry"},
		{"muse", "usage: agentmeter muse"},
		{"compare", "side-by-side two-tool analytics"},
	}
	for _, test := range cases {
		t.Run(test.topic, func(t *testing.T) {
			var code int
			stdout, stderr := captureOutput(t, func() { code = helpTopic([]string{test.topic}) })
			if code != 0 {
				t.Fatalf("helpTopic(%q) = %d, stderr = %s", test.topic, code, stderr)
			}
			if !strings.Contains(stdout, test.want) {
				t.Errorf("helpTopic(%q) stdout = %q, want %q", test.topic, stdout, test.want)
			}
			if stderr != "" {
				t.Errorf("helpTopic(%q) stderr = %q", test.topic, stderr)
			}
		})
	}
}

func TestHelpTopicUnknown(t *testing.T) {
	var code int
	stdout, stderr := captureOutput(t, func() { code = helpTopic([]string{"bogus"}) })
	if code != 2 {
		t.Fatalf("helpTopic(bogus) = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("unknown topic stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, `unknown help topic "bogus"`) || !strings.Contains(stderr, "Topics:") {
		t.Errorf("unknown topic stderr = %q", stderr)
	}
}

func TestDescribeUnknown(t *testing.T) {
	flagMessage, isFlag := describeUnknown("--nope")
	if !isFlag {
		t.Error("describeUnknown(--nope) did not report a flag")
	}
	if !strings.Contains(flagMessage, `unknown flag "--nope"`) ||
		!strings.Contains(flagMessage, "agentmeter --help") {
		t.Errorf("flag message = %q", flagMessage)
	}
	if strings.Contains(flagMessage, "Usage:") {
		t.Errorf("flag message printed the usage block: %q", flagMessage)
	}
	commandMessage, isFlag := describeUnknown("bogus")
	if isFlag {
		t.Error("describeUnknown(bogus) reported a flag")
	}
	if !strings.Contains(commandMessage, `unknown command "bogus"`) ||
		!strings.Contains(commandMessage, "Usage:") {
		t.Errorf("command message = %q", commandMessage)
	}
}

func TestHelpTopicBare(t *testing.T) {
	var code int
	stdout, _ := captureOutput(t, func() { code = helpTopic(nil) })
	if code != 0 || !strings.Contains(stdout, "agentmeter cmd") {
		t.Errorf("bare help = %d, %q", code, stdout)
	}
	if !strings.Contains(stdout, "Exit codes: 0 success, 1 data or runtime error, 2 usage error.") {
		t.Errorf("help missing exit-code footer: %q", stdout)
	}
}
