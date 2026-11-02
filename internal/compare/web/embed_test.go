// SPDX-License-Identifier: AGPL-3.0-only

package compareweb

import (
	"bytes"
	"strings"
	"testing"
)

func TestRewriteSidesIdentityForOCCMD(t *testing.T) {
	if got := RewriteSides(IndexHTML, "oc", "cmd"); !bytes.Equal(got, IndexHTML) {
		t.Fatal("oc-cmd rewrite is not byte-identical to the shell")
	}
}

func TestRewriteSidesMuse(t *testing.T) {
	out := string(RewriteSides(IndexHTML, "oc", "muse"))
	for _, banned := range []string{
		"Command Code", ".cmd", "'cmd'", `"cmd"`, "' cmd ",
		".cmdModels", ".sharedCMD", "\x00",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("rewritten shell still contains %q", banned)
		}
	}
	for _, want := range []string{
		"Muse", ".muse", "'muse'", "' muse models matched'", "' muse · match '",
		".museModels", ".sharedMUSE", "OpenCode", ".oc", "'oc'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rewritten shell missing %q", want)
		}
	}
}

func TestRewriteSidesCmdMuse(t *testing.T) {
	// Chained replacement must not turn .oc into .muse via .cmd.
	out := string(RewriteSides(IndexHTML, "cmd", "muse"))
	if !strings.Contains(out, ".cmdModels") {
		t.Error("side A keys lost (.cmdModels missing)")
	}
	if !strings.Contains(out, ".museModels") {
		t.Error("side B keys lost (.museModels missing)")
	}
	if strings.Contains(out, "OpenCode") || strings.Contains(out, ".oc ") || strings.Contains(out, "'oc'") {
		t.Error("side A relabel leaked OpenCode tokens")
	}
}
