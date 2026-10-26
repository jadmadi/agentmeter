// SPDX-License-Identifier: AGPL-3.0-only

// Package cmdtelemetry is the Command Code CLI surface: flags, report build,
// output writing, summary printing and the optional local server. It holds no
// schema math; the report lives in internal/report/cmd.
package cmdtelemetry

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	cmdweb "github.com/jadmadi/agentmeter/command-code-telemetry/web"
	"github.com/jadmadi/agentmeter/internal/emit"
	reportcmd "github.com/jadmadi/agentmeter/internal/report/cmd"
	sourcecmd "github.com/jadmadi/agentmeter/internal/source/cmd"
)

// Run executes the Command Code subcommand and returns its exit code.
func Run(args []string) int {
	flags := flag.NewFlagSet("cmd", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("root", "~/.commandcode/projects", "session root")
	models := flags.String("models", "", "comma-separated model id filters")
	since := flags.String("since", "", "earliest local date, YYYY-MM-DD")
	until := flags.String("until", "", "latest local date, YYYY-MM-DD")
	outDir := flags.String("out", "./data/cmd", "output directory")
	noTitles := flags.Bool("no-titles", false, "omit session titles")
	fullPaths := flags.Bool("full-paths", false, "keep full project paths")
	serveFlag := flags.Bool("serve", false, "build once, serve the report, and open it")
	watch := flags.Bool("watch", false, "watch sources, rebuild, and serve (implies --serve)")
	port := flags.Int("port", 8000, "port for --serve")
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
	if *watch {
		*serveFlag = true
	}

	modelFilters := parseModelFilters(*models)
	buildReport := func() (map[string]any, string, string, string, error) {
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
			return nil, "", "", "", err
		}
		jsonPath, jsPath, htmlPath, err := emit.WriteReport(*outDir, data, cmdweb.IndexHTML, "CMD_TELEMETRY")
		return data, jsonPath, jsPath, htmlPath, err
	}

	data, jsonPath, jsPath, htmlPath, err := buildReport()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentmeter cmd: %v\n", err)
		return 1
	}
	printSummary(data, jsonPath, jsPath)

	if *watch {
		err := emit.Watch(
			filepath.Dir(htmlPath),
			*port,
			!*noOpen,
			0,
			func() string { return sourcecmd.Fingerprint(*root) },
			func() error {
				_, _, _, _, err := buildReport()
				return err
			},
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentmeter cmd: watch: %v\n", err)
			return 1
		}
		return 0
	}
	if *serveFlag {
		if err := emit.Serve(filepath.Dir(htmlPath), *port, !*noOpen); err != nil {
			fmt.Fprintf(os.Stderr, "agentmeter cmd: %v\n", err)
			return 1
		}
		return 0
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
