// SPDX-License-Identifier: AGPL-3.0-only

// Package cli is the `agentmeter compare` front end: flag parsing, input
// resolution, the build/write cycle, the terminal summary, and the optional
// local server. All aggregation lives in internal/report/compare.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	shared "github.com/jadmadi/agentmeter/internal/compare"
	compareweb "github.com/jadmadi/agentmeter/internal/compare/web"
	"github.com/jadmadi/agentmeter/internal/emit"
	"github.com/jadmadi/agentmeter/internal/pyjson"
	reportcompare "github.com/jadmadi/agentmeter/internal/report/compare"
	"github.com/jadmadi/agentmeter/internal/terminal"
)

// DefaultOutDir is CWD-relative, like the per-tool defaults.
const DefaultOutDir = "./data/compare"

// GlobalName is the JS bundle global the comparison renderer reads.
const GlobalName = "COMPARISON_TELEMETRY"

const usage = `agentmeter compare: side-by-side OpenCode vs Command Code analytics.

Inputs are pre-built schema v3 reports, report directories, or the tools'
own live sources (an OpenCode SQLite store / a Command Code session root).
--oc and --cmd accept an optional value; omit it to use that tool's default.

Usage:
  agentmeter compare [flags]
  agentmeter compare [<oc-source>] [<cmd-source>]
  agentmeter compare oc-cmd

Examples:
  agentmeter compare                          compare default live sources
  agentmeter compare oc-cmd                   same, explicit pair alias
  agentmeter compare data/oc data/cmd         compare two report directories
  agentmeter compare --oc data/oc --cmd data/cmd
  agentmeter compare --oc --cmd               use both defaults
  agentmeter compare --serve

Flags:
`

// pairAliases are accepted as a single positional token meaning "use both
// defaults". A real path with the same name wins over the alias.
var pairAliases = map[string]struct{}{
	"oc-cmd":    {},
	"oc-vs-cmd": {},
	"oc_vs_cmd": {},
	"oc+cmd":    {},
	"both":      {},
}

// optionalValueFlags may be given without a value and fall back to the tool
// default; requiredValueFlags must carry one.
var optionalValueFlags = map[string]struct{}{"oc": {}, "cmd": {}}

var requiredValueFlags = map[string]struct{}{"out": {}, "port": {}}

// Run executes the compare subcommand and returns its exit code.
func Run(args []string) int {
	return run(args, os.Stdout, os.Stderr)
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}

	ocPath := fs.String("oc", "", "OpenCode report file/dir or SQLite store (default: autodetected store)")
	cmdPath := fs.String("cmd", reportcompare.DefaultCMDPath, "Command Code report file/dir or session root")
	outDir := fs.String("out", DefaultOutDir, "output directory")
	serve := fs.Bool("serve", false, "build once, serve the comparison, and open it")
	watch := fs.Bool("watch", false, "watch both sources, rebuild, and serve (implies --serve)")
	port := fs.Int("port", 8000, "port for --serve")
	noOpen := fs.Bool("no-open", false, "do not open a browser with --serve")

	normalized, err := normalizeArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		fs.Usage()
		return 2
	}
	if err := fs.Parse(normalized); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "agentmeter compare: invalid flags")
		fs.Usage()
		return 2
	}
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if err := applyPositionals(fs.Args(), ocPath, cmdPath, set); err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		fs.Usage()
		return 2
	}
	if *watch {
		*serve = true
	}

	ocInput, err := reportcompare.Detect(shared.ToolOC, *ocPath)
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		return 1
	}
	cmdInput, err := reportcompare.Detect(shared.ToolCMD, *cmdPath)
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		return 1
	}

	buildReport := func() (map[string]any, string, string, string, error) {
		ocReport, err := reportcompare.Load(ocInput)
		if err != nil {
			return nil, "", "", "", err
		}
		cmdReport, err := reportcompare.Load(cmdInput)
		if err != nil {
			return nil, "", "", "", err
		}
		doc, err := reportcompare.Build(ocReport, cmdReport, time.Now())
		if err != nil {
			return nil, "", "", "", err
		}
		jsonPath, jsPath, htmlPath, err := emit.WriteReportFiles(*outDir, emit.ComparisonFiles, doc, compareweb.IndexHTML, GlobalName)
		return doc, jsonPath, jsPath, htmlPath, err
	}

	doc, jsonPath, jsPath, htmlPath, err := buildReport()
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		return 1
	}
	printSummary(stdout, doc, jsonPath, jsPath, htmlPath)

	if *watch {
		err := emit.WatchFiles(
			filepath.Dir(htmlPath),
			emit.ComparisonFiles,
			*port,
			!*noOpen,
			0,
			func() string {
				return reportcompare.Fingerprint(ocInput) + "\x00" + reportcompare.Fingerprint(cmdInput)
			},
			func() error {
				_, _, _, _, err := buildReport()
				return err
			},
		)
		if err != nil {
			fmt.Fprintf(stderr, "agentmeter compare: watch: %v\n", err)
			return 1
		}
		return 0
	}
	if *serve {
		if err := emit.ServeFiles(filepath.Dir(htmlPath), emit.ComparisonFiles, *port, !*noOpen); err != nil {
			fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "Open %s in a browser, or re-run with --serve.\n", htmlPath)
	return 0
}

// normalizeArgs makes --oc/--cmd values optional and rejects a missing value
// on the flags that truly require one. Go's flag package stops at the first
// positional, so flags are collected first and positionals are moved behind a
// "--" terminator. This lets both `compare data/oc data/cmd --serve` and
// `compare --oc --cmd` work.
func normalizeArgs(args []string) ([]string, error) {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, 2)
	afterTerminator := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if afterTerminator {
			positionals = append(positionals, arg)
			continue
		}
		if arg == "--" {
			afterTerminator = true
			continue
		}
		name, _, hasValue := splitFlag(arg)
		canonical := strings.TrimLeft(name, "-")
		if _, optional := optionalValueFlags[canonical]; optional {
			if hasValue {
				flags = append(flags, arg)
				continue
			}
			if i+1 < len(args) && !looksLikeFlag(args[i+1]) {
				flags = append(flags, arg, args[i+1])
				i++
				continue
			}
			flags = append(flags, name+"=")
			continue
		}
		if _, required := requiredValueFlags[canonical]; required {
			if hasValue {
				flags = append(flags, arg)
				continue
			}
			if i+1 >= len(args) || looksLikeFlag(args[i+1]) {
				return nil, fmt.Errorf("%s requires a value (use %s=<value>)", displayFlag(name), displayFlag(name))
			}
			flags = append(flags, arg, args[i+1])
			i++
			continue
		}
		if looksLikeFlag(arg) {
			flags = append(flags, arg)
			continue
		}
		positionals = append(positionals, arg)
	}
	if len(positionals) > 0 {
		flags = append(flags, "--")
		flags = append(flags, positionals...)
	}
	return flags, nil
}

func splitFlag(arg string) (name, value string, hasValue bool) {
	if len(arg) < 2 || arg[0] != '-' {
		return arg, "", false
	}
	if index := strings.IndexByte(arg, '='); index >= 0 {
		return arg[:index], arg[index+1:], true
	}
	return arg, "", false
}

func looksLikeFlag(arg string) bool {
	return len(arg) > 1 && arg[0] == '-'
}

func displayFlag(name string) string {
	return "--" + strings.TrimLeft(name, "-")
}

// applyPositionals resolves the optional positional inputs:
//
//	compare                         both defaults
//	compare oc-cmd                  both defaults (pair alias; a real path wins)
//	compare <oc>                    OC source + default cmd
//	compare <oc> <cmd>              both positional sources
func applyPositionals(args []string, ocPath, cmdPath *string, set map[string]bool) error {
	switch len(args) {
	case 0:
		return nil
	case 1:
		arg := args[0]
		if _, alias := pairAliases[strings.ToLower(arg)]; alias && !pathExists(arg) {
			if set["oc"] || set["cmd"] {
				return fmt.Errorf("pair alias %q cannot be combined with --oc/--cmd", arg)
			}
			return nil
		}
		if set["oc"] {
			return fmt.Errorf("OpenCode input given twice (positional %q and --oc)", arg)
		}
		*ocPath = arg
		return nil
	case 2:
		if set["oc"] || set["cmd"] {
			return fmt.Errorf("positional inputs cannot be combined with --oc/--cmd")
		}
		*ocPath, *cmdPath = args[0], args[1]
		return nil
	default:
		return fmt.Errorf("accepts at most two positional inputs (OpenCode, Command Code)")
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// printSummary renders the compact terminal comparison.
func printSummary(w io.Writer, doc map[string]any, jsonPath, jsPath, htmlPath string) {
	meta := object(doc, "meta")
	tools := object(meta, "tools")
	window := object(meta, "window")
	ocInfo := object(tools, "oc")
	cmdInfo := object(tools, "cmd")

	terminal.Title(w, "AgentMeter · OpenCode vs Command Code")
	fmt.Fprintln(w)
	terminal.Line(w, "SOURCE", "oc  "+text(ocInfo["label"]))
	terminal.Line(w, "", "cmd "+text(cmdInfo["label"]))
	mode := text(window["mode"])
	from := text(window["from"])
	to := text(window["to"])
	if from == "-" || to == "-" {
		terminal.Line(w, "WINDOW", fmt.Sprintf("%s · %s days", mode, count(window["days"])))
	} else {
		terminal.Line(w, "WINDOW", fmt.Sprintf("%s · %s → %s · %s days", mode, from, to, count(window["days"])))
	}
	totals := object(doc, "totals")
	ocTotals := object(totals, "oc")
	cmdTotals := object(totals, "cmd")
	terminal.Line(w, "SCALE", fmt.Sprintf("oc %s steps · %s sessions · %s models",
		count(ocTotals["steps"]), count(ocTotals["sessions"]), count(ocTotals["models"])))
	terminal.Line(w, "", fmt.Sprintf("cmd %s steps · %s sessions · %s models",
		count(cmdTotals["steps"]), count(cmdTotals["sessions"]), count(cmdTotals["models"])))

	throughput := object(doc, "throughput")
	ocSpeed := object(throughput, "oc")
	cmdSpeed := object(throughput, "cmd")
	terminal.Line(w, "SPEED", fmt.Sprintf("weighted oc %s · cmd %s tok/s · Δ %s",
		number(ocSpeed["weighted"]), number(cmdSpeed["weighted"]), delta(object(throughput, "delta")["weightedPct"], "oc faster", "cmd faster", winner(throughput, "weighted"))))
	terminal.Line(w, "", fmt.Sprintf("median   oc %s · cmd %s tok/s · Δ %s",
		number(ocSpeed["median"]), number(cmdSpeed["median"]), delta(object(throughput, "delta")["medianPct"], "oc faster", "cmd faster", winner(throughput, "median"))))

	cost := object(doc, "cost")
	ocCost := object(cost, "oc")
	cmdCost := object(cost, "cmd")
	terminal.Line(w, "COST", fmt.Sprintf("$/1k generated oc $%s · cmd $%s · Δ %s",
		money(ocCost["per1kGenerated"]), money(cmdCost["per1kGenerated"]),
		delta(object(cost, "delta")["per1kGeneratedPct"], "oc cheaper", "cmd cheaper", winner(cost, "per1kGenerated"))))
	terminal.Line(w, "", fmt.Sprintf("spend oc $%s · cmd $%s · Δ %s",
		money(ocCost["totalSpendUsd"]), money(cmdCost["totalSpendUsd"]),
		delta(object(cost, "delta")["totalSpendUsdPct"], "oc lower", "cmd lower", winner(cost, "totalSpendUsd"))))

	cache := object(doc, "cache")
	ocCache := object(cache, "oc")
	cmdCache := object(cache, "cmd")
	terminal.Line(w, "CACHE", fmt.Sprintf("hit oc %s%% · cmd %s%% · Δ %s pp",
		percent(ocCache["hitRate"]), percent(cmdCache["hitRate"]), signedPp(object(cache, "delta")["hitRatePp"])))

	families := object(meta, "families")
	terminal.Line(w, "MODELS", fmt.Sprintf("%s shared · %s oc-only · %s cmd-only",
		count(families["shared"]), count(families["ocOnly"]), count(families["cmdOnly"])))

	printFamilyTable(w, doc)
	printToolTable(w, doc)
	printVerdicts(w, doc)

	terminal.Section(w, "REPORT")
	fmt.Fprintf(w, "  %s\n", htmlPath)
	fmt.Fprintf(w, "  %s\n", jsonPath)
	fmt.Fprintf(w, "  %s\n", jsPath)
}

func printFamilyTable(w io.Writer, doc map[string]any) {
	families := list(object(doc, "models"), "families")
	rows := make([][]string, 0, 6)
	for _, entry := range families {
		item, ok := entry.(map[string]any)
		if !ok || !truthy(item["shared"]) {
			continue
		}
		oc := object(item, "oc")
		cmd := object(item, "cmd")
		rows = append(rows, []string{
			text(item["family"]),
			number(oc["weighted"]),
			number(cmd["weighted"]),
			signed(item["delta"]),
			money(oc["per1kGenerated"]),
			money(cmd["per1kGenerated"]),
		})
		if len(rows) == 6 {
			break
		}
	}
	if len(rows) == 0 {
		return
	}
	terminal.Section(w, "SHARED MODEL FAMILIES (top by volume)")
	terminal.Table(w,
		[]string{"FAMILY", "OC TOK/S", "CMD TOK/S", "Δ%", "OC $/1K", "CMD $/1K"},
		rows,
		[]bool{false, true, true, true, true, true},
		[]int{28, 9, 10, 9, 9, 9})
}

func printToolTable(w io.Writer, doc map[string]any) {
	categories := list(object(doc, "tools"), "normalized")
	rows := make([][]string, 0, 6)
	for _, entry := range categories {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		oc := object(item, "oc")
		cmd := object(item, "cmd")
		delta := object(item, "delta")
		rows = append(rows, []string{
			text(item["category"]),
			percent(oc["shareCalls"]),
			percent(cmd["shareCalls"]),
			signedPp(delta["sharePp"]),
		})
		if len(rows) == 6 {
			break
		}
	}
	if len(rows) == 0 {
		return
	}
	terminal.Section(w, "TOOL MIX (share of calls)")
	terminal.Table(w,
		[]string{"CATEGORY", "OC %", "CMD %", "Δ pp"},
		rows,
		[]bool{false, true, true, true},
		[]int{28, 8, 8, 8})
}

func printVerdicts(w io.Writer, doc map[string]any) {
	verdicts, _ := doc["verdicts"].([]any)
	if len(verdicts) == 0 {
		return
	}
	terminal.Section(w, "VERDICTS")
	for _, entry := range verdicts {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		side := winnerOf(item)
		deltaText := "-"
		if value := item["deltaPct"]; value != nil {
			deltaText = signedPct(value)
		} else if value := item["deltaPp"]; value != nil {
			deltaText = signedPp(value) + " pp"
		}
		fmt.Fprintf(w, "  %-38s oc %-8s cmd %-8s Δ %-9s %s\n",
			text(item["metric"]), numberWithUnit(item["oc"], item["unit"]),
			numberWithUnit(item["cmd"], item["unit"]), deltaText, side)
	}
}

// ---- document readers

func object(data map[string]any, key string) map[string]any {
	if value, ok := data[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

func list(data map[string]any, key string) []any {
	if value, ok := data[key].([]any); ok {
		return value
	}
	return nil
}

func winner(container map[string]any, key string) string {
	winners := object(container, "winner")
	value, _ := winners[key].(string)
	return value
}

func winnerOf(item map[string]any) string {
	switch value, _ := item["winner"].(string); value {
	case shared.ToolOC:
		return "oc wins"
	case shared.ToolCMD:
		return "cmd wins"
	case "tie":
		return "tie"
	default:
		return "n/a"
	}
}

func truthy(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func text(value any) string {
	if value == nil {
		return "-"
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return "-"
		}
		return text
	}
	return fmt.Sprintf("%v", value)
}

func count(value any) string {
	if value == nil {
		return "-"
	}
	number, ok := pyjson.Int(value)
	if !ok {
		return text(value)
	}
	return terminal.Count(number)
}

func number(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return trimFloat(number)
}

func numberWithUnit(value any, unit any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	unitText := text(unit)
	switch {
	case unitText == "USD":
		return fmt.Sprintf("$%.2f", number)
	case strings.HasPrefix(unitText, "$/"):
		return fmt.Sprintf("$%.4f", number)
	case unitText == "% of input":
		return trimFloat(number*100) + "%"
	case unitText == "tokens":
		return terminal.Compact(number)
	}
	return trimFloat(number)
}

func money(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.4f", number)
}

func percent(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.1f", number*100)
}

func signed(value any) string {
	entry, ok := value.(map[string]any)
	if !ok {
		return "-"
	}
	return signedPct(entry["weightedPct"])
}

func signedPct(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%+.1f%%", number)
}

func signedPp(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%+.2f", number)
}

func delta(value any, ocBetter, cmdBetter, side string) string {
	formatted := signedPct(value)
	if formatted == "-" {
		return "n/a"
	}
	switch side {
	case shared.ToolOC:
		return formatted + " (" + ocBetter + ")"
	case shared.ToolCMD:
		return formatted + " (" + cmdBetter + ")"
	case "tie":
		return formatted + " (tie)"
	default:
		return formatted
	}
}

func trimFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
