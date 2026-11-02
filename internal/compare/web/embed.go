// SPDX-License-Identifier: AGPL-3.0-only
package compareweb

import (
	"bytes"
	_ "embed"
	"strings"

	shared "github.com/jadmadi/agentmeter/internal/compare"
)

// IndexHTML is the cross-tool comparison shell. It reads comparison-data.js
// (written next to it by emit.WriteReportFiles) from the COMPARISON_TELEMETRY
// global. The shell is written for the oc-cmd pair; RewriteSides relabels it
// for any other pair at emit time.
//
//go:embed index.html
var IndexHTML []byte

// RewriteSides relabels the comparison shell for a tool pair: document keys
// (.oc/.cmd, 'oc'/'cmd', shared-model keys), display ids and human labels.
// Two-phase placeholder substitution keeps chained replacements exact (for
// cmd-muse, .oc must become .cmd without also becoming .muse). For oc-cmd
// every replacement is identity.
func RewriteSides(html []byte, aTool, bTool string) []byte {
	const (
		keyA   = "\x00sideA\x00"
		keyB   = "\x00sideB\x00"
		labelA = "\x00labelA\x00"
		labelB = "\x00labelB\x00"
	)
	out := html
	replace := func(old, new string) {
		out = bytes.ReplaceAll(out, []byte(old), []byte(new))
	}
	// Document keys and element classes. Element ids (oc-tools, oc-pie) are
	// internal only and intentionally untouched.
	replace(".oc", ".\x00sideA\x00")
	replace(".cmd", ".\x00sideB\x00")
	replace("'oc'", "'"+keyA+"'")
	replace("'cmd'", "'"+keyB+"'")
	replace(`"oc"`, `"`+keyA+`"`)
	replace(`"cmd"`, `"`+keyB+`"`)
	replace("' oc ", "' "+keyA+" ")
	replace("' cmd ", "' "+keyB+" ")
	replace(".sharedOC", ".shared\x00upperA\x00")
	replace(".sharedCMD", ".shared\x00upperB\x00")
	// Human labels. "Command Code" is replaced before "OpenCode" so neither
	// substitution can match the other's output; placeholders make both exact.
	replace("Command Code", labelB)
	replace("OpenCode", labelA)

	replace(keyA, aTool)
	replace(keyB, bTool)
	replace("\x00upperA\x00", strings.ToUpper(aTool))
	replace("\x00upperB\x00", strings.ToUpper(bTool))
	replace(labelA, shared.DisplayName(aTool))
	replace(labelB, shared.DisplayName(bTool))
	return out
}
