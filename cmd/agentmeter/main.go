// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"fmt"
	"os"

	cmdtelemetry "github.com/jadmadi/agentmeter/command-code-telemetry/go"
	octelemetry "github.com/jadmadi/agentmeter/opencode-telemetry/go"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

const usage = `agentmeter - local-first analytics for AI coding sessions

Usage:
  agentmeter cmd [flags]       Command Code telemetry (alias: command-code)
  agentmeter oc [flags]        OpenCode telemetry (alias: opencode)
  agentmeter version
  agentmeter help

Run "agentmeter cmd --help" or "agentmeter oc --help" for flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	args := os.Args[2:]
	switch os.Args[1] {
	case "cmd", "command-code":
		os.Exit(cmdtelemetry.Run(args))
	case "oc", "opencode":
		os.Exit(octelemetry.Run(args))
	case "version", "--version", "-v":
		fmt.Printf("agentmeter %s\ncommit: %s\nbuilt: %s\n", version, commit, date)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "agentmeter: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
