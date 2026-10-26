// SPDX-License-Identifier: AGPL-3.0-only

// Package cmdtelemetry is the Command Code CLI surface: flags, report build,
// output writing, summary printing and the optional local server. It holds no
// schema math; the report lives in internal/report/cmd.
package cmdtelemetry

import (
	"flag"
	"fmt"
	"os"

	cmdweb "github.com/jadmadi/agentmeter/command-code-telemetry/web"
	"github.com/jadmadi/agentmeter/internal/emit"
	reportcmd "github.com/jadmadi/agentmeter/internal/report/cmd"
	sourcecmd "github.com/jadmadi/agentmeter/internal/source/cmd"
)

// Run executes the Command Code subcommand and returns its exit code.
func Run(args []string) int {
	flags := flag.NewFlagSet("cmd", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("root", "~/.commandcode/projects", "session root (default: ~/.commandcode/projects)")
	models := flags.String("models", "", "comma-separated model id filters (default: all models)")
	since := flags.String("since", "", "earliest local date, YYYY-MM-DD")
	until := flags.String("until", "", "latest local date, YYYY-MM-DD")
	outDir := flags.String("out", "./data/cmd", "output directory (default: ./data/cmd)")
	noTitles := flags.Bool("no-titles", false, "omit session titles")
	fullPaths := flags.Bool("full-paths", false, "keep full project paths (default: basenames)")
	serveFlag := flags.Bool("serve", false, "serve the report and open it")
	port := flags.Int("port", 8000, "port for --serve (default: 8000)")
	noOpen := flags.Bool("no-open", false, "do not open a browser with --serve")
	flags.Usage = func() {
		fmt.Fprint(flags.Output(), "usage: agentmeter cmd [flags]\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "agentmeter cmd: unexpected argument %q\n", flags.Arg(0))
		flags.Usage()
		return 2
	}

	modelFilters := parseModelFilters(*models)
	source := sourcecmd.Load(sourcecmd.Filters{
		Root:          *root,
		ModelFilters:  modelFilters,
		Since:         *since,
		Until:         *until,
		IncludeTitles: !*noTitles,
	})
	data, err := reportcmd.Build(source, reportcmd.Options{
		Root:          *root,
		ModelFilters:  modelFilters,
		Since:         *since,
		Until:         *until,
		IncludeTitles: !*noTitles,
		FullPaths:     *fullPaths,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentmeter cmd: %v\n", err)
		return 1
	}

	jsonPath, jsPath, htmlPath, err := emit.WriteReport(*outDir, data, cmdweb.IndexHTML, "CMD_TELEMETRY")
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentmeter cmd: write report: %v\n", err)
		return 1
	}
	printSummary(data, jsonPath, jsPath)

	if *serveFlag {
		return serve(*outDir, *port, !*noOpen)
	}
	fmt.Printf("Open %s in a browser, or re-run with --serve.\n", htmlPath)
	return 0
}

// parseModelFilters mirrors `[term.strip() for term in args.models.split(",") if term.strip()]`.
func parseModelFilters(raw string) []string {
	terms := make([]string, 0, 4)
	start := 0
	for index := 0; index <= len(raw); index++ {
		if index == len(raw) || raw[index] == ',' {
			term := trimSpace(raw[start:index])
			if term != "" {
				terms = append(terms, term)
			}
			start = index + 1
		}
	}
	return terms
}

func trimSpace(value string) string {
	start := 0
	end := len(value)
	for start < end && isSpace(value[start]) {
		start++
	}
	for end > start && isSpace(value[end-1]) {
		end--
	}
	return value[start:end]
}

func isSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '\v' || value == '\f'
}
