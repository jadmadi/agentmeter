// SPDX-License-Identifier: AGPL-3.0-only

// Package render owns terminal styling. Human-facing output must use these
// primitives so ANSI escapes never leak into feature packages.
package render

import (
	"io"
	"os"
)

const (
	Cyan  = "\033[36m"
	Bold  = "\033[1m"
	Dim   = "\033[2m"
	Reset = "\033[0m"
)

// Enabled reports whether w is an interactive terminal and no environment
// setting disables colour.
func Enabled(w io.Writer) bool {
	if disabledByEnv() {
		return false
	}
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func disabledByEnv() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	if os.Getenv("TERM") == "dumb" {
		return true
	}
	return os.Getenv("CLICOLOR") == "0"
}

// Paint wraps text in an ANSI style when the writer supports color.
func Paint(w io.Writer, code, text string) string {
	if !Enabled(w) {
		return text
	}
	return code + text + Reset
}
