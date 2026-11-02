// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"fmt"
	"os"

	cmdtelemetry "github.com/jadmadi/agentmeter/command-code-telemetry/go"
	comparecli "github.com/jadmadi/agentmeter/internal/compare/cli"
	musetelemetry "github.com/jadmadi/agentmeter/muse-telemetry/go"
	octelemetry "github.com/jadmadi/agentmeter/opencode-telemetry/go"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

const (
	licenseID = "AGPL-3.0-only"
	sourceURL = "https://github.com/jadmadi/agentmeter"
)

const usage = `agentmeter - local-first analytics for AI coding sessions

Usage:
  agentmeter cmd [flags]       Command Code telemetry (alias: command-code)
  agentmeter oc [flags]        OpenCode telemetry (alias: opencode)
  agentmeter muse [flags]      Muse session telemetry
  agentmeter compare [flags]   side-by-side two-tool comparison
  agentmeter version
  agentmeter help

Run "agentmeter cmd --help", "agentmeter oc --help",
"agentmeter muse --help" or "agentmeter compare --help" for flags.

AgentMeter is licensed AGPL-3.0-only. Source: https://github.com/jadmadi/agentmeter
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
	case "muse":
		os.Exit(musetelemetry.Run(args))
	case "compare":
		os.Exit(comparecli.Run(args))
	case "version", "--version", "-v":
		fmt.Printf("agentmeter %s\ncommit: %s\nbuilt: %s\nlicense: %s\nsource: %s\n",
			version, commit, date, licenseID, sourceURL)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "agentmeter: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
