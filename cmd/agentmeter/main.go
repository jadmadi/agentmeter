// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"fmt"
	"os"
	"strings"

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

// withHelp maps `<command> help` to the command's own help flag.
func withHelp(args []string) []string {
	if len(args) > 0 && args[0] == "help" {
		return []string{"--help"}
	}
	return args
}

// helpTopic routes `agentmeter help <topic>` to the matching command page.
func helpTopic(args []string) int {
	if len(args) == 0 {
		fmt.Print(usage)
		return 0
	}
	switch args[0] {
	case "cmd", "command-code":
		return cmdtelemetry.Run([]string{"--help"})
	case "oc", "opencode":
		return octelemetry.Run([]string{"--help"})
	case "muse":
		return musetelemetry.Run([]string{"--help"})
	case "compare":
		return comparecli.Run([]string{"--help"})
	case "version":
		printVersion()
		return 0
	case "help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "agentmeter: unknown help topic %q\nTopics: cmd, oc, muse, compare, version\n", args[0])
		return 2
	}
}

func printVersion() {
	fmt.Printf("agentmeter %s\ncommit: %s\nbuilt: %s\nlicense: %s\nsource: %s\n",
		version, commit, date, licenseID, sourceURL)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	args := os.Args[2:]
	switch os.Args[1] {
	case "cmd", "command-code":
		os.Exit(cmdtelemetry.Run(withHelp(args)))
	case "oc", "opencode":
		os.Exit(octelemetry.Run(withHelp(args)))
	case "muse":
		os.Exit(musetelemetry.Run(withHelp(args)))
	case "compare":
		os.Exit(comparecli.Run(withHelp(args)))
	case "version", "--version", "-v":
		printVersion()
	case "help":
		os.Exit(helpTopic(args))
	case "--help", "-h":
		fmt.Print(usage)
	default:
		message, _ := describeUnknown(os.Args[1])
		fmt.Fprint(os.Stderr, message)
		os.Exit(2)
	}
}

// describeUnknown renders the top-level error for an unknown command or flag.
func describeUnknown(arg string) (message string, isFlag bool) {
	if strings.HasPrefix(arg, "-") {
		return fmt.Sprintf("agentmeter: unknown flag %q\nRun \"agentmeter --help\" for usage.\n", arg), true
	}
	return fmt.Sprintf("agentmeter: unknown command %q\n\n%s", arg, usage), false
}
