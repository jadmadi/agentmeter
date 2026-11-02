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
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
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

const usage = `agentmeter compare: side-by-side two-tool analytics.

Inputs are pre-built schema v3 reports, report directories, or the tools'
own live sources (an OpenCode SQLite store / a Command Code or Muse
session root). --oc, --cmd and --muse accept an optional value; omit it to
use that tool's default.

The pair defaults to oc-cmd. Give a pair alias, two tool flags, or one
tool flag (the other side falls back to the oc-cmd default).

Usage:
  agentmeter compare [flags]
  agentmeter compare [<side-a-source>] [<side-b-source>]
  agentmeter compare <pair-alias> [<side-a-source>] [<side-b-source>]

Pair aliases: oc-cmd (both), oc-muse, cmd-muse, plus -vs-/_vs_/+ spellings.

Examples:
  agentmeter compare                          oc-cmd over default live sources
  agentmeter compare oc-muse                  oc-muse over default live sources
  agentmeter compare data/oc data/cmd         compare two report directories
  agentmeter compare --oc data/oc --muse data/muse
  agentmeter compare --muse                   muse vs the default oc source
  agentmeter compare --serve

Flags:
`

// pairAliases maps a positional token to the compared tools in canonical
// order. A real path with the same name wins over the alias.
var pairAliases = map[string][2]string{
	"oc-cmd": {shared.ToolOC, shared.ToolCMD}, "oc-vs-cmd": {shared.ToolOC, shared.ToolCMD},
	"oc_vs_cmd": {shared.ToolOC, shared.ToolCMD}, "oc+cmd": {shared.ToolOC, shared.ToolCMD},
	"both":    {shared.ToolOC, shared.ToolCMD},
	"oc-muse": {shared.ToolOC, shared.ToolMuse}, "muse-oc": {shared.ToolOC, shared.ToolMuse},
	"oc-vs-muse": {shared.ToolOC, shared.ToolMuse}, "muse-vs-oc": {shared.ToolOC, shared.ToolMuse},
	"oc_vs_muse": {shared.ToolOC, shared.ToolMuse}, "muse_vs_oc": {shared.ToolOC, shared.ToolMuse},
	"oc+muse": {shared.ToolOC, shared.ToolMuse}, "muse+oc": {shared.ToolOC, shared.ToolMuse},
	"cmd-muse": {shared.ToolCMD, shared.ToolMuse}, "muse-cmd": {shared.ToolCMD, shared.ToolMuse},
	"cmd-vs-muse": {shared.ToolCMD, shared.ToolMuse}, "muse-vs-cmd": {shared.ToolCMD, shared.ToolMuse},
	"cmd_vs_muse": {shared.ToolCMD, shared.ToolMuse}, "muse_vs_cmd": {shared.ToolCMD, shared.ToolMuse},
	"cmd+muse": {shared.ToolCMD, shared.ToolMuse}, "muse+cmd": {shared.ToolCMD, shared.ToolMuse},
}

// optionalValueFlags may be given without a value and fall back to the tool
// default; requiredValueFlags must carry one.
var optionalValueFlags = map[string]struct{}{"oc": {}, "cmd": {}, "muse": {}}

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
	musePath := fs.String("muse", reportcompare.DefaultMusePath, "Muse report file/dir or session root")
	outDir := fs.String("out", DefaultOutDir, "output directory")
	serve := fs.Bool("serve", false, "build once, serve the comparison, and open it")
	watch := fs.Bool("watch", false, "watch both sources, rebuild, and serve (implies --serve)")
	port := fs.Int("port", 8000, "port for --serve")
	noOpen := fs.Bool("no-open", false, "do not open a browser with --serve")
	allModels := fs.Bool("all-models", false, "use all models for the headline and verdicts (default: shared families)")
	jsonOut := fs.Bool("json", false, "print comparison-data.json to stdout instead of the human summary")
	noColor := fs.Bool("no-color", false, "disable ANSI color")

	normalized, err := normalizeArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		fs.Usage()
		return 2
	}
	// Suppress the flag package's automatic usage dump on a parse error; the
	// error path prints one short line instead. Explicit help still prints the
	// full page once.
	usage := fs.Usage
	fs.Usage = func() {}
	fs.SetOutput(io.Discard)
	parseErr := fs.Parse(normalized)
	fs.SetOutput(stderr)
	fs.Usage = usage
	if parseErr != nil {
		if errors.Is(parseErr, flag.ErrHelp) {
			fs.SetOutput(stdout)
			usage()
			fs.SetOutput(stderr)
			return 0
		}
		if name := unknownFlagName(parseErr); name != "" {
			fmt.Fprintf(stderr, "agentmeter compare: invalid flag %q\n", name)
		} else {
			fmt.Fprintf(stderr, "agentmeter compare: %v\n", parseErr)
		}
		fmt.Fprintln(stderr, "Run \"agentmeter compare --help\" for usage.")
		return 2
	}
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	paths := map[string]*string{shared.ToolOC: ocPath, shared.ToolCMD: cmdPath, shared.ToolMuse: musePath}
	aTool, bTool, err := resolvePair(fs.Args(), paths, set)
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		fs.Usage()
		return 2
	}
	if *watch {
		*serve = true
	}

	aInput, err := reportcompare.Detect(aTool, *paths[aTool])
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		return 1
	}
	bInput, err := reportcompare.Detect(bTool, *paths[bTool])
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		return 1
	}
	if aInput.Kind == shared.KindReport && bInput.Kind == shared.KindReport && sameFile(aInput.Path, bInput.Path) {
		fmt.Fprintf(stderr, "agentmeter compare: --%s and --%s point at the same report; pass a different report to one side\n", aTool, bTool)
		return 1
	}

	buildReport := func() (map[string]any, string, string, string, error) {
		aReport, err := reportcompare.Load(aInput)
		if err != nil {
			return nil, "", "", "", err
		}
		bReport, err := reportcompare.Load(bInput)
		if err != nil {
			return nil, "", "", "", err
		}
		doc, err := reportcompare.Build(aReport, bReport, time.Now())
		if err != nil {
			return nil, "", "", "", err
		}
		if err := requireComparable(aTool, aReport, bTool, bReport, doc); err != nil {
			return nil, "", "", "", err
		}
		html := compareweb.RewriteSides(compareweb.IndexHTML, aTool, bTool)
		jsonPath, jsPath, htmlPath, err := emit.WriteReportFiles(*outDir, emit.ComparisonFiles, doc, html, GlobalName)
		return doc, jsonPath, jsPath, htmlPath, err
	}

	doc, jsonPath, jsPath, htmlPath, err := buildReport()
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		return 1
	}
	if *noColor {
		previous := os.Getenv("NO_COLOR")
		_ = os.Setenv("NO_COLOR", "1")
		defer func() {
			if previous == "" {
				_ = os.Unsetenv("NO_COLOR")
			} else {
				_ = os.Setenv("NO_COLOR", previous)
			}
		}()
	}
	if *jsonOut {
		raw, err := pyjson.JSONBytes(doc)
		if err != nil {
			fmt.Fprintf(stderr, "agentmeter compare: encode JSON: %v\n", err)
			return 1
		}
		if _, err := stdout.Write(raw); err != nil {
			return 1
		}
		if _, err := stdout.Write([]byte("\n")); err != nil {
			return 1
		}
	} else {
		scope := "shared"
		if *allModels || !likeForLikeAvailable(doc) {
			scope = "all"
		}
		printSummary(stdout, doc, jsonPath, jsPath, htmlPath, scope)
	}

	if *watch {
		err := emit.WatchFiles(
			filepath.Dir(htmlPath),
			emit.ComparisonFiles,
			*port,
			!*noOpen,
			0,
			func() string {
				return reportcompare.Fingerprint(aInput) + "\x00" + reportcompare.Fingerprint(bInput)
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
	if !*jsonOut {
		fmt.Fprintln(stdout, "Hint: re-run with --serve to open it.")
	}
	return 0
}

// unknownFlagName extracts the flag name from the flag package's
// "flag provided but not defined" error.
func unknownFlagName(err error) string {
	const prefix = "flag provided but not defined: "
	if message := err.Error(); strings.HasPrefix(message, prefix) {
		return strings.TrimPrefix(message, prefix)
	}
	return ""
}

// sameFile reports whether two paths resolve to the same file.
func sameFile(a, b string) bool {
	left, err := os.Stat(a)
	if err != nil {
		return false
	}
	right, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(left, right)
}

// requireComparable rejects a comparison with an empty side or window before
// any report file is written.
func requireComparable(aTool string, a *shared.Report, bTool string, b *shared.Report, doc map[string]any) error {
	sides := []struct {
		tool   string
		report *shared.Report
	}{{aTool, a}, {bTool, b}}
	for _, side := range sides {
		if side.report.SessionsN == 0 {
			return fmt.Errorf("no comparable data: %s has 0 sessions; pass --%s <session root or report>",
				shared.DisplayName(side.tool), side.tool)
		}
		if side.report.Totals.Steps == 0 {
			return fmt.Errorf("no comparable data: %s has 0 steps; pass --%s <session root or report>",
				shared.DisplayName(side.tool), side.tool)
		}
	}
	window := object(object(doc, "meta"), "window")
	if int64Value(window["days"]) == 0 {
		return fmt.Errorf("no overlapping steps in the source window")
	}
	return nil
}

func likeForLikeAvailable(doc map[string]any) bool {
	likeForLike, ok := doc["likeForLike"].(map[string]any)
	if !ok {
		return false
	}
	available, _ := likeForLike["available"].(bool)
	return available
}

// normalizeArgs makes --oc/--cmd/--muse values optional and rejects a missing
// value on the flags that truly require one. Go's flag package stops at the
// first positional, so flags are collected first and positionals are moved
// behind a "--" terminator. This lets both `compare data/oc data/cmd --serve`
// and `compare --oc --cmd` work.
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
		// A bare token is a positional even when it spells a tool name. Only
		// dashed tokens may select --oc, --cmd, or --muse.
		if !looksLikeFlag(arg) {
			positionals = append(positionals, arg)
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

// resolvePair determines the compared tools in canonical order and fills
// their sources from positionals:
//
//	compare                         oc-cmd over defaults
//	compare oc-muse                 named pair over defaults (a real path wins)
//	compare oc-muse <a> <b>         named pair with positional sources
//	compare <a>                     pair's side A + default side B
//	compare <a> <b>                 both positional sources
//
// Without an alias, explicitly set tool flags select the pair; a single
// flag pairs that tool with the first oc-cmd default not already present.
func resolvePair(args []string, paths map[string]*string, set map[string]bool) (string, string, error) {
	if len(args) > 0 {
		if pair, alias := pairAliases[strings.ToLower(args[0])]; alias && !pathExists(args[0]) {
			if set["oc"] || set["cmd"] || set["muse"] {
				return "", "", fmt.Errorf("pair alias %q cannot be combined with --oc/--cmd/--muse", args[0])
			}
			if len(args) > 3 {
				return "", "", fmt.Errorf("pair alias %q accepts at most two positional inputs", args[0])
			}
			return pair[0], pair[1], fillPairSources(pair[0], pair[1], args[1:], paths, set)
		}
	}
	var flagged []string
	for _, tool := range shared.CanonicalTools {
		if set[tool] {
			flagged = append(flagged, tool)
		}
	}
	if len(flagged) > 2 {
		return "", "", fmt.Errorf("compare is pairwise: select two of --oc/--cmd/--muse (or a pair alias)")
	}
	pair := append([]string{}, flagged...)
	for _, tool := range []string{shared.ToolOC, shared.ToolCMD} {
		if len(pair) >= 2 {
			break
		}
		if !slices.Contains(pair, tool) {
			pair = append(pair, tool)
		}
	}
	aTool, bTool := shared.OrderPair(pair[0], pair[1])
	if len(args) > 2 {
		return "", "", fmt.Errorf("accepts at most two positional inputs (%s, %s)",
			shared.DisplayName(aTool), shared.DisplayName(bTool))
	}
	return aTool, bTool, fillPairSources(aTool, bTool, args, paths, set)
}

// fillPairSources assigns positional inputs to the pair sides in order.
func fillPairSources(aTool, bTool string, args []string, paths map[string]*string, set map[string]bool) error {
	tools := []string{aTool, bTool}
	for index, arg := range args {
		tool := tools[index]
		if set[tool] {
			return fmt.Errorf("%s input given twice (positional %q and --%s)",
				shared.DisplayName(tool), arg, tool)
		}
		*paths[tool] = arg
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// printSummary renders the compact terminal comparison. The headline comes
// from the like-for-like (shared-family) block by default; --all-models shows
// the confounded aggregate instead.
func printSummary(w io.Writer, doc map[string]any, jsonPath, jsPath, htmlPath, scope string) {
	sides := sidesFromDoc(doc)
	meta := object(doc, "meta")
	tools := object(meta, "tools")
	window := object(meta, "window")
	aInfo := object(tools, sides.a)
	bInfo := object(tools, sides.b)

	evidence := doc
	sharedScope := false
	scopeLine := "all models · confounded by model and workload mix"
	scopeNote := "All models are included; model and workload mixes differ, so treat comparisons as context, not a harness verdict."
	if scope == "shared" {
		if likeForLike, ok := doc["likeForLike"].(map[string]any); ok && truthy(likeForLike["available"]) {
			evidence = likeForLike
			sharedScope = true
			totals := object(likeForLike, "totals")
			scopeLine = fmt.Sprintf("shared-family pooled · %s shared families · %s shared steps",
				count(len(list(likeForLike, "families"))),
				count(int64Value(object(totals, sides.a)["steps"])+int64Value(object(totals, sides.b)["steps"])))
			scopeNote = sidesFormat(sides, "Shared families are pooled by volume; one-sided families are excluded. Each family block shows its model match (OpenCode:Command Code model ids).")
		}
	}

	terminal.Title(w, "AgentMeter · "+sides.dispA+" vs "+sides.dispB)
	fmt.Fprintln(w)
	terminal.Line(w, "SOURCE", text(aInfo["label"]))
	terminal.Line(w, "", text(bInfo["label"]))
	mode := text(window["mode"])
	from := text(window["from"])
	to := text(window["to"])
	if from == "-" || to == "-" {
		terminal.Line(w, "WINDOW", fmt.Sprintf("%s · %s days", mode, count(window["days"])))
	} else {
		terminal.Line(w, "WINDOW", fmt.Sprintf("%s · %s → %s · %s days", mode, from, to, count(window["days"])))
	}
	terminal.Line(w, "SCOPE", scopeLine)
	if sharedScope {
		evTotals := object(evidence, "totals")
		aShared := object(evTotals, sides.a)
		bShared := object(evTotals, sides.b)
		speed := object(evidence, "throughput")
		terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "OpenCode: %s shared steps (%s timed) · %s gen tokens"),
			count(aShared["steps"]), count(object(speed, sides.a)["n"]), compactCount(aShared["generatedTokens"])))
		terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "Command Code: %s shared steps (%s timed) · %s gen tokens"),
			count(bShared["steps"]), count(object(speed, sides.b)["n"]), compactCount(bShared["generatedTokens"])))
	}
	wrapped(w, "  ", "  ", scopeNote, 78)

	totals := object(doc, "totals")
	aTotals := object(totals, sides.a)
	bTotals := object(totals, sides.b)
	terminal.Line(w, "SCALE", "all models in window (includes one-sided families)")
	terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "OpenCode: %s steps (%s/session) · %s sessions · %s models"),
		count(aTotals["steps"]), ratio1(aTotals["steps"], aTotals["sessions"]), count(aTotals["sessions"]), count(aTotals["models"])))
	terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "Command Code: %s steps (%s/session) · %s sessions · %s models"),
		count(bTotals["steps"]), ratio1(bTotals["steps"], bTotals["sessions"]), count(bTotals["sessions"]), count(bTotals["models"])))

	printBottomLine(w, evidence, doc, scope)
	printMetricDetails(w, evidence, doc, scope)

	families := object(meta, "families")
	models := object(meta, "models")
	aOnlyModels := int64Value(models[sides.a]) - int64Value(models[sides.sharedKeyA])
	bOnlyModels := int64Value(models[sides.b]) - int64Value(models[sides.sharedKeyB])
	terminal.Line(w, "FAMILIES", fmt.Sprintf(sidesFormat(sides, "%s shared · %s OpenCode-only · %s Command Code-only"),
		count(families["shared"]), count(families[sides.a+"Only"]), count(families[sides.b+"Only"])))
	terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "OpenCode models in window: %s total (%s shared, %s OC-only)"),
		count(models[sides.a]), count(models[sides.sharedKeyA]), count(aOnlyModels)))
	terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "Command Code models in window: %s total (%s shared, %s CMD-only)"),
		count(models[sides.b]), count(models[sides.sharedKeyB]), count(bOnlyModels)))

	printFamilyBlocks(w, doc, scope)
	printToolTable(w, evidence, sides)
	printVerdicts(w, doc, scope)
	printHowToRead(w, sides)

	terminal.Section(w, "REPORT")
	fmt.Fprintf(w, "  HTML: %s\n", htmlPath)
	fmt.Fprintf(w, "  JSON: %s\n", jsonPath)
	fmt.Fprintf(w, "  JS:   %s\n", jsPath)
}

func printFamilyBlocks(w io.Writer, doc map[string]any, scope string) {
	sides := sidesFromDoc(doc)
	families := list(object(doc, "models"), "families")
	title := "SHARED MODEL FAMILIES (pooled by volume)"
	if scope == "all" {
		title = "MODEL FAMILIES (shared first; one-sided marked)"
	}
	terminal.Section(w, title)
	shown := 0
	for _, entry := range families {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		sharedFamily := truthy(item["shared"])
		if scope == "shared" && !sharedFamily {
			continue
		}
		aSide := object(item, sides.a)
		bSide := object(item, sides.b)
		n := object(item, "n")
		conf := confidenceText(item["confidence"])
		fmt.Fprintf(w, "  %s", text(item["family"]))
		if conf != "" {
			fmt.Fprintf(w, "  (%s)", conf)
		}
		fmt.Fprintln(w)
		if sharedFamily {
			winner := object(item, "winner")
			fmt.Fprintf(w, sidesFormat(sides, "    Speed (weighted): OpenCode %s vs Command Code %s tok/s\n"),
				number(aSide["weighted"]), number(bSide["weighted"]))
			fmt.Fprintf(w, "           → %s\n",
				speedAdvantageFromPair(text(winner["weighted"]), aSide["weighted"], bSide["weighted"], sides.b))
			fmt.Fprintf(w, sidesFormat(sides, "    Speed (median):   OpenCode %s vs Command Code %s tok/s\n"),
				number(aSide["median"]), number(bSide["median"]))
			fmt.Fprintf(w, "           → %s\n",
				speedAdvantageFromPair(text(winner["median"]), aSide["median"], bSide["median"], sides.b))
			fmt.Fprintf(w, sidesFormat(sides, "    Cost / 1k: OpenCode $%s vs Command Code $%s ($%s vs $%s / 1M)\n"),
				money(aSide["per1kGenerated"]), money(bSide["per1kGenerated"]),
				money2(aSide["perMillionGenerated"]), money2(bSide["perMillionGenerated"]))
			fmt.Fprintf(w, "           → %s\n",
				costAdvantageFromPair(text(winner["per1kGenerated"]), aSide["per1kGenerated"], bSide["per1kGenerated"], sides.a))
			fmt.Fprintf(w, sidesFormat(sides, "    Steps: %s OpenCode (%s timed) / %s Command Code (%s timed)\n"),
				count(aSide["steps"]), count(n[sides.a]), count(bSide["steps"]), count(n[sides.b]))
			fmt.Fprintf(w, sidesFormat(sides, "    Tokens: %s OpenCode / %s Command Code generated\n"),
				compactCount(aSide["generatedTokens"]), compactCount(bSide["generatedTokens"]))
			fmt.Fprintf(w, sidesFormat(sides, "    Models: %d OpenCode / %d Command Code (%s)\n"),
				len(list(aSide, "models")), len(list(bSide, "models")), text(item["modelMatch"]))
		} else {
			side := sides.dispA + " only"
			steps := count(aSide["steps"])
			timed := count(n[sides.a])
			models := len(list(aSide, "models"))
			if int64Value(aSide["steps"]) == 0 && int64Value(n[sides.a]) == 0 {
				side = sides.dispB + " only"
				steps = count(bSide["steps"])
				timed = count(n[sides.b])
				models = len(list(bSide, "models"))
			}
			fmt.Fprintf(w, "    %s · %s steps (%s timed) · %d model(s)\n", side, steps, timed, models)
		}
		if flags := familyFlags(item["flags"]); flags != "" {
			fmt.Fprintf(w, "    Flags: %s\n", flags)
		}
		shown++
		if shown == 8 {
			break
		}
	}
	footer := sidesFormat(sides, "Model match = model ids per side (OpenCode:Command Code); only 1:1 is a true model pair, N:1 and 1:N pool provider variants. Flags: low sample = <30 timed steps on either side; cost/speed outlier = family is >2x cost or >3x weighted speed from that tool's median across shared families.")
	if scope == "all" {
		footer += " one tool only = family present in only one tool."
	}
	wrapped(w, "  ", "  ", footer, 78)
}

func compactCount(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return terminal.Compact(number)
}

func shortFlag(value any) string {
	switch text(value) {
	case "low-sample":
		return "low-n"
	case "cost-outlier":
		return "cost-out"
	case "speed-outlier":
		return "speed-out"
	case "one-sided":
		return "one-side"
	default:
		return text(value)
	}
}

func printToolTable(w io.Writer, evidence map[string]any, sides pairSides) {
	toolSection := object(evidence, "tools")
	categories := list(toolSection, "normalized")
	rows, aCoverage, bCoverage := toolShareRows(categories, 6, sides)
	if len(rows) == 0 {
		return
	}
	terminal.Section(w, "TOOL MIX (share of tool calls and final-answer steps)")
	terminal.Table(w,
		[]string{"CATEGORY", sides.upperA, sides.upperB, "DIFF"},
		rows,
		[]bool{false, true, true, true},
		[]int{26, 10, 12, 9})
	terminal.Note(w, fmt.Sprintf(sidesFormat(sides, "coverage: OpenCode %.1f%% · Command Code %.1f%% (rest is other; 0.1%% rounded)"),
		aCoverage*100, bCoverage*100))
}

func toolShareRows(categories []any, limit int, sides pairSides) ([][]string, float64, float64) {
	type entry struct {
		name   string
		aShare *float64
		bShare *float64
	}
	entries := make([]entry, 0, len(categories))
	var aCoverage, bCoverage float64
	for _, raw := range categories {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		aSide := object(item, sides.a)
		bSide := object(item, sides.b)
		aShare := optFloatValue(aSide["shareCalls"])
		bShare := optFloatValue(bSide["shareCalls"])
		if aShare != nil {
			aCoverage += *aShare
		}
		if bShare != nil {
			bCoverage += *bShare
		}
		entries = append(entries, entry{name: text(item["category"]), aShare: aShare, bShare: bShare})
	}
	// The normalized list is already ordered by calls, but sort defensively by
	// the larger of the two shares so the folded remainder is genuinely tail.
	sort.SliceStable(entries, func(i, j int) bool {
		return maxShare(entries[i].aShare, entries[i].bShare) > maxShare(entries[j].aShare, entries[j].bShare)
	})
	take := limit
	if len(entries) <= limit+1 {
		take = len(entries)
	}
	rows := make([][]string, 0, take+1)
	var otherOC, otherCMD float64
	otherCount := 0
	for index, item := range entries {
		if index < take {
			aText := "-"
			if item.aShare != nil {
				aText = fmt.Sprintf("%.1f%%", *item.aShare*100)
			}
			bText := "-"
			if item.bShare != nil {
				bText = fmt.Sprintf("%.1f%%", *item.bShare*100)
			}
			rows = append(rows, []string{
				item.name,
				aText,
				bText,
				signedPp(shareDiff(item.aShare, item.bShare)) + " pp",
			})
			continue
		}
		if item.aShare != nil {
			otherOC += *item.aShare
		}
		if item.bShare != nil {
			otherCMD += *item.bShare
		}
		otherCount++
	}
	if otherCount > 0 {
		label := "other"
		if otherCount > 1 {
			label = fmt.Sprintf("other (%d)", otherCount)
		}
		rows = append(rows, []string{label, fmt.Sprintf("%.1f%%", otherOC*100), fmt.Sprintf("%.1f%%", otherCMD*100), signedPp((otherOC-otherCMD)*100) + " pp"})
	}
	return rows, aCoverage, bCoverage
}

func maxShare(a, b *float64) float64 {
	value := 0.0
	if a != nil {
		value = *a
	}
	if b != nil && *b > value {
		value = *b
	}
	return value
}

func shareDiff(a, b *float64) any {
	if a == nil || b == nil {
		return nil
	}
	return (*a - *b) * 100
}

func printVerdicts(w io.Writer, doc map[string]any, scope string) {
	sides := sidesFromDoc(doc)
	verdictsByScope, _ := doc["verdicts"].(map[string]any)
	verdicts, _ := verdictsByScope[scope].([]any)
	if len(verdicts) == 0 {
		return
	}
	title := "VERDICTS (shared-family pooled)"
	if scope == "all" {
		title = "VERDICTS (all models, confounded)"
	}
	terminal.Section(w, title)
	rows := make([][]string, 0, len(verdicts))
	for _, entry := range verdicts {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			metricLabel(item["metric"]),
			verdictResult(item, sides.a, sides.b),
			confidenceText(item["confidence"]),
			verdictSample(item),
		})
	}
	terminal.Table(w,
		[]string{"METRIC", "RESULT", "CONFIDENCE", "N"},
		rows,
		[]bool{false, false, false, false},
		[]int{22, 26, 10, 14})
	terminal.Note(w, "RESULT names the winner and the advantage; no ratio inversion is needed.")
	terminal.Note(w, "N is the smaller side's sample: timed steps (speed), steps (cost/cache),")
	terminal.Note(w, "or sessions (spend). Spend/session (all) is all-model context, not shared.")
}

func printBottomLine(w io.Writer, evidence, doc map[string]any, scope string) {
	sides := sidesFromDoc(doc)
	verdicts := list(object(doc, "verdicts"), scope)
	if len(verdicts) == 0 {
		return
	}
	speed := findVerdict(verdicts, "generation speed (weighted)")
	speedMedian := findVerdict(verdicts, "generation speed (median step)")
	cost1k := findVerdict(verdicts, "cost per 1k generated")
	costStep := findVerdict(verdicts, "cost per step")
	spend := findVerdict(verdicts, "spend per session")
	cache := findVerdict(verdicts, "cache hit rate")
	evTotals := object(evidence, "totals")
	aEvTotals := object(evTotals, sides.a)
	bEvTotals := object(evTotals, sides.b)

	terminal.Section(w, "BOTTOM LINE")
	if speed != nil {
		a := advantageOf(speed, sides.a, sides.b)
		if a.winner == "tie" {
			wrapped(w, "  • ", "    ", "Generation speed was effectively tied.", 76)
		} else {
			sentence := fmt.Sprintf("%s is %s faster at the weighted rate", toolLabel(a.winner), factorText(a.factor))
			if speedMedian != nil {
				m := advantageOf(speedMedian, sides.a, sides.b)
				if m.winner != "tie" {
					sentence += fmt.Sprintf(" and %s faster at the median step", factorText(m.factor))
				}
			}
			sentence += fmt.Sprintf(" (%s; min-side N = %s timed steps).", confidenceText(speed["confidence"]), count(speed["n"]))
			wrapped(w, "  • ", "    ", sentence, 76)
		}
	}
	if cost1k != nil {
		a := advantageOf(cost1k, sides.a, sides.b)
		sentence := ""
		if a.winner == "tie" {
			sentence = "Per-1k-generated cost was tied"
		} else {
			sentence = fmt.Sprintf("%s is %s cheaper per 1k generated tokens (%s)",
				toolLabel(a.winner), factorText(a.factor), confidenceText(cost1k["confidence"]))
		}
		if costStep != nil {
			s := advantageOf(costStep, sides.a, sides.b)
			if s.winner != "tie" {
				sentence += fmt.Sprintf("; %s is %s cheaper per step (%s; %s vs %s gen tok/step)",
					toolLabel(s.winner), factorText(s.factor), confidenceText(costStep["confidence"]),
					ratio0(aEvTotals["generatedTokens"], aEvTotals["steps"]),
					ratio0(bEvTotals["generatedTokens"], bEvTotals["steps"]))
			}
		}
		if scope == "shared" {
			if note := pooledCostReversalNote(doc, a.winner); note != "" {
				sentence += ". " + note
			}
		}
		wrapped(w, "  • ", "    ", sentence+".", 76)
	}
	if spend != nil {
		a := advantageOf(spend, sides.a, sides.b)
		sentence := ""
		if a.winner == "tie" {
			sentence = "All-model spend per session was tied (" + confidenceText(spend["confidence"]) + ")"
		} else {
			sentence = fmt.Sprintf("All-model spend per session: %s is %s cheaper (%s)",
				toolLabel(a.winner), factorText(a.factor), confidenceText(spend["confidence"]))
		}
		allTotals := object(doc, "totals")
		aAll := object(allTotals, sides.a)
		bAll := object(allTotals, sides.b)
		allCost := object(doc, "cost")
		aAllCost := object(allCost, sides.a)
		bAllCost := object(allCost, sides.b)
		sentence += fmt.Sprintf(sidesFormat(sides, " (%s vs %s steps/session; total spend OpenCode $%s at $%s/step vs Command Code $%s at $%s/step); model and workload mixes differ, so this is context, not a harness verdict."),
			ratio1(aAll["steps"], aAll["sessions"]),
			ratio1(bAll["steps"], bAll["sessions"]),
			money2(aAllCost["totalSpendUsd"]),
			money(aAllCost["perStep"]),
			money2(bAllCost["totalSpendUsd"]),
			money(bAllCost["perStep"]))
		wrapped(w, "  • ", "    ", sentence, 76)
	}
	if cache != nil {
		a := advantageOf(cache, sides.a, sides.b)
		sentence := "Cache hit rates are effectively tied"
		if a.winner == "tie" {
			sentence += fmt.Sprintf(" (%+.2f pp).", a.pp)
		} else {
			sentence += fmt.Sprintf(" (%s %+.2f pp).", toolLabel(a.winner), a.pp)
		}
		wrapped(w, "  • ", "    ", sentence, 76)
	}
}

func pooledCostReversalNote(doc map[string]any, pooledWinner string) string {
	sides := sidesFromDoc(doc)
	if pooledWinner == "" || pooledWinner == "tie" || pooledWinner == "n/a" {
		return ""
	}
	families := list(object(doc, "models"), "families")
	var top map[string]any
	var topTokens float64
	for _, entry := range families {
		item, ok := entry.(map[string]any)
		if !ok || !truthy(item["shared"]) {
			continue
		}
		aTokens, _ := pyjson.Float(object(item, sides.a)["generatedTokens"])
		bTokens, _ := pyjson.Float(object(item, sides.b)["generatedTokens"])
		if top == nil || aTokens+bTokens > topTokens {
			top = item
			topTokens = aTokens + bTokens
		}
	}
	if top == nil || confidenceText(top["confidence"]) != "clear" {
		return ""
	}
	topWinner := text(object(top, "winner")["per1kGenerated"])
	if topWinner == "" || topWinner == "tie" || topWinner == "n/a" || topWinner == pooledWinner {
		return ""
	}
	aCost := object(top, sides.a)["per1kGenerated"]
	bCost := object(top, sides.b)["per1kGenerated"]
	return fmt.Sprintf("Pooled direction reverses %s (%s: %s at $%s vs $%s) due to shared-family volume mix",
		text(top["family"]),
		text(top["modelMatch"]),
		costAdvantageFromPair(topWinner, aCost, bCost, sides.a),
		money(aCost),
		money(bCost))
}

func printMetricDetails(w io.Writer, evidence, doc map[string]any, scope string) {
	sides := sidesFromDoc(doc)
	throughput := object(evidence, "throughput")
	aSpeed := object(throughput, sides.a)
	bSpeed := object(throughput, sides.b)
	speedWinner := object(throughput, "winner")
	terminal.Line(w, "SPEED", fmt.Sprintf(sidesFormat(sides, "weighted: OpenCode %s vs Command Code %s tok/s"),
		number(aSpeed["weighted"]), number(bSpeed["weighted"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s · confidence: %s",
		speedAdvantageFromPair(text(speedWinner["weighted"]), aSpeed["weighted"], bSpeed["weighted"], sides.b),
		verdictConfidence(doc, scope, "generation speed (weighted)")))
	terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "median:   OpenCode %s vs Command Code %s tok/s"),
		number(aSpeed["median"]), number(bSpeed["median"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s · confidence: %s",
		speedAdvantageFromPair(text(speedWinner["median"]), aSpeed["median"], bSpeed["median"], sides.b),
		verdictConfidence(doc, scope, "generation speed (median step)")))

	evTotals := object(evidence, "totals")
	aEvTotals := object(evTotals, sides.a)
	bEvTotals := object(evTotals, sides.b)
	cost := object(evidence, "cost")
	aCost := object(cost, sides.a)
	bCost := object(cost, sides.b)
	costWinner := object(cost, "winner")
	terminal.Line(w, "COST", fmt.Sprintf(sidesFormat(sides, "per 1k generated: OpenCode $%s vs Command Code $%s"),
		money(aCost["per1kGenerated"]), money(bCost["per1kGenerated"])))
	terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "per 1M generated: OpenCode $%s vs Command Code $%s"),
		money2(aCost["perMillionGenerated"]), money2(bCost["perMillionGenerated"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s · confidence: %s",
		costAdvantageFromPair(text(costWinner["per1kGenerated"]), aCost["per1kGenerated"], bCost["per1kGenerated"], sides.a),
		verdictConfidence(doc, scope, "cost per 1k generated")))
	terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "per step: OpenCode $%s vs Command Code $%s"),
		money(aCost["perStep"]), money(bCost["perStep"])))
	terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "gen/step: OpenCode %s vs Command Code %s tok/step"),
		ratio0(aEvTotals["generatedTokens"], aEvTotals["steps"]),
		ratio0(bEvTotals["generatedTokens"], bEvTotals["steps"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s · confidence: %s",
		costAdvantageFromPair(text(costWinner["perStep"]), aCost["perStep"], bCost["perStep"], sides.a),
		verdictConfidence(doc, scope, "cost per step")))
	input := fmt.Sprintf("input: both $%s per 1M tokens", money2(aCost["perMillionInput"]))
	if money2(aCost["perMillionInput"]) != money2(bCost["perMillionInput"]) {
		input = fmt.Sprintf(sidesFormat(sides, "input: OpenCode $%s vs Command Code $%s per 1M tokens"),
			money2(aCost["perMillionInput"]), money2(bCost["perMillionInput"]))
	}
	terminal.Line(w, "", input)
	terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "scope total: OpenCode $%s vs Command Code $%s"),
		money2(aCost["totalSpendUsd"]), money2(bCost["totalSpendUsd"])))

	spendCost := object(doc, "cost")
	aSpend := object(spendCost, sides.a)
	bSpend := object(spendCost, sides.b)
	spendWinner := object(spendCost, "winner")
	terminal.Line(w, "SPEND", fmt.Sprintf(sidesFormat(sides, "all models, per session: OpenCode $%s vs Command Code $%s"),
		money(aSpend["totalSpendPerSession"]), money(bSpend["totalSpendPerSession"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s · confidence: %s",
		costAdvantageFromPair(text(spendWinner["totalSpendPerSession"]), aSpend["totalSpendPerSession"], bSpend["totalSpendPerSession"], sides.a),
		verdictConfidence(doc, scope, "spend per session")))
	terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "all models, total: OpenCode $%s vs Command Code $%s"),
		money2(aSpend["totalSpendUsd"]), money2(bSpend["totalSpendUsd"])))
	terminal.Line(w, "", fmt.Sprintf(sidesFormat(sides, "all models, per step: OpenCode $%s vs Command Code $%s"),
		money(aSpend["perStep"]), money(bSpend["perStep"])))

	cache := object(evidence, "cache")
	aCache := object(cache, sides.a)
	bCache := object(cache, sides.b)
	terminal.Line(w, "CACHE", fmt.Sprintf(sidesFormat(sides, "hit rate: OpenCode %s%% vs Command Code %s%%"),
		percent2(aCache["hitRate"]), percent2(bCache["hitRate"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s",
		cacheAdvantageText(winner(cache, "hitRate"), object(cache, "delta")["hitRatePp"])))
}

func printHowToRead(w io.Writer, sides pairSides) {
	terminal.Section(w, "HOW TO READ THIS")
	bullets := []string{
		sidesFormat(sides, `"OpenCode 3.48× faster" means OpenCode produced 3.48× more generated tokens per second; "Command Code 1.24× cheaper" means Command Code spent 1.24× less per unit. The winner is named, so no ratio inversion is needed.`),
		"Weighted speed = total generated tokens ÷ total generation time. Median speed = the typical step; it resists a few very long steps.",
		"N is the smaller side's sample: timed steps for speed, all steps for cost and cache, sessions for spend.",
		"clear = >=20% relative difference with >=30 samples per side; weak = 5% to 20% relative difference; low-sample = <30 samples on either side; confounded = model, family-volume, or workload mixes differ; tie = <5% relative (cache: <1 pp or <5%).",
		"Family flags: low sample = <30 timed steps on either side; cost/speed outlier = family is >2x cost or >3x weighted speed from that tool's median across shared families; one tool only = family present in only one tool (shown with --all-models).",
		sidesFormat(sides, "Like-for-like pools the shared families by volume. Family blocks show model match (OpenCode:Command Code model ids); only 1:1 rows are a true model-to-model pair. N:1 and 1:N rows pool provider variants."),
		"SPEND is all-model and is not a per-task cost; use the shared-family cost lines for a like-for-like view.",
	}
	for _, text := range bullets {
		wrapped(w, "  • ", "    ", text, 76)
	}
}

func wrapped(w io.Writer, firstPrefix, nextPrefix, text string, width int) {
	words := strings.Fields(text)
	if len(words) == 0 {
		return
	}
	line := words[0]
	prefix := firstPrefix
	for _, word := range words[1:] {
		if len([]rune(prefix))+len([]rune(line))+1+len([]rune(word)) > width {
			fmt.Fprintf(w, "%s%s\n", prefix, line)
			prefix = nextPrefix
			line = word
		} else {
			line += " " + word
		}
	}
	fmt.Fprintf(w, "%s%s\n", prefix, line)
}

func toolLabel(tool string) string {
	if tool == "tie" {
		return "tie"
	}
	return shared.DisplayName(tool)
}

// pairSides carries the resolved pair for summary rendering.
type pairSides struct {
	a          string
	b          string
	dispA      string
	dispB      string
	upperA     string
	upperB     string
	sharedKeyA string
	sharedKeyB string
}

// sidesFromDoc reads the compared pair from the document. Reports written
// before meta.pair existed are treated as the oc-cmd pair.
func sidesFromDoc(doc map[string]any) pairSides {
	aKey, bKey := shared.ToolOC, shared.ToolCMD
	if meta, ok := doc["meta"].(map[string]any); ok {
		if pair, ok := meta["pair"].([]any); ok && len(pair) == 2 {
			if first, ok := pair[0].(string); ok && first != "" {
				aKey = first
			}
			if second, ok := pair[1].(string); ok && second != "" {
				bKey = second
			}
		}
	}
	return pairSides{
		a:          aKey,
		b:          bKey,
		dispA:      shared.DisplayName(aKey),
		dispB:      shared.DisplayName(bKey),
		upperA:     strings.ToUpper(shared.DisplayName(aKey)),
		upperB:     strings.ToUpper(shared.DisplayName(bKey)),
		sharedKeyA: "shared" + strings.ToUpper(aKey),
		sharedKeyB: "shared" + strings.ToUpper(bKey),
	}
}

// sidesFormat relabels a summary format string for the compared pair. The
// templates name OpenCode and Command Code; for that pair this is identity.
func sidesFormat(sides pairSides, format string) string {
	replacer := strings.NewReplacer(
		"Command Code", sides.dispB,
		"OpenCode", sides.dispA,
		"CMD-only", strings.ToUpper(sides.b)+"-only",
		"OC-only", strings.ToUpper(sides.a)+"-only",
	)
	return replacer.Replace(format)
}

func factorText(factor float64) string {
	return fmt.Sprintf("%.2f×", factor)
}

func pairRatio(oc, cmd any) (float64, bool) {
	ocVal, okOC := pyjson.Float(oc)
	cmdVal, okCMD := pyjson.Float(cmd)
	if !okOC || !okCMD || ocVal <= 0 || cmdVal <= 0 {
		return 0, false
	}
	return ocVal / cmdVal, true
}

func advantageOf(item map[string]any, aKey, bKey string) struct {
	winner       string
	factor       float64
	pct          float64
	pp           float64
	higherBetter bool
	ok           bool
} {
	result := struct {
		winner       string
		factor       float64
		pct          float64
		pp           float64
		higherBetter bool
		ok           bool
	}{}
	result.winner, _ = item["winner"].(string)
	if result.winner == "" {
		result.winner = "tie"
	}
	unit := text(item["unit"])
	if unit == "% of input" {
		result.pp, result.ok = pyjson.Float(item["deltaPp"])
		return result
	}
	ratio, ok := pairRatio(item[aKey], item[bKey])
	if !ok {
		ratio, ok = pyjson.Float(item["ratio"])
	}
	if !ok || ratio <= 0 {
		return result
	}
	result.higherBetter, _ = item["higherBetter"].(bool)
	if result.winner == "tie" {
		result.factor = 1
		result.ok = true
		return result
	}
	result.factor = ratio
	if (result.higherBetter && result.winner == bKey) || (!result.higherBetter && result.winner == aKey) {
		result.factor = 1 / ratio
	}
	if result.factor < 1 {
		result.factor = 1 / result.factor
	}
	result.pct = (result.factor - 1) * 100
	result.ok = true
	return result
}

func speedAdvantageFromPair(winner string, aVal, bVal any, bKey string) string {
	ratio, ok := pairRatio(aVal, bVal)
	if !ok {
		return "-"
	}
	return speedAdvantageText(winner, ratio, bKey)
}

func costAdvantageFromPair(winner string, aVal, bVal any, aKey string) string {
	ratio, ok := pairRatio(aVal, bVal)
	if !ok {
		return "-"
	}
	return costAdvantageText(winner, ratio, aKey)
}

func speedAdvantageText(winner string, ratio any, bKey string) string {
	if winner == "tie" {
		return "tie"
	}
	value, ok := pyjson.Float(ratio)
	if !ok || value <= 0 {
		return "-"
	}
	factor := value
	if winner == bKey {
		factor = 1 / value
	}
	if factor < 1 {
		factor = 1 / factor
	}
	return fmt.Sprintf("%s %s faster", toolLabel(winner), factorText(factor))
}

func costAdvantageText(winner string, ratio any, aKey string) string {
	if winner == "tie" {
		return "tie"
	}
	value, ok := pyjson.Float(ratio)
	if !ok || value <= 0 {
		return "-"
	}
	factor := value
	if winner == aKey {
		factor = 1 / value
	}
	if factor < 1 {
		factor = 1 / factor
	}
	return fmt.Sprintf("%s %s cheaper", toolLabel(winner), factorText(factor))
}

func cacheAdvantageText(winner string, pp any) string {
	value, _ := pyjson.Float(pp)
	if winner == "tie" {
		return fmt.Sprintf("tie (%+.2f pp)", value)
	}
	return fmt.Sprintf("%s %+.2f pp higher", toolLabel(winner), value)
}

func verdictResult(item map[string]any, aKey, bKey string) string {
	a := advantageOf(item, aKey, bKey)
	if !a.ok {
		return "-"
	}
	if a.winner == "tie" {
		if text(item["unit"]) == "% of input" {
			return fmt.Sprintf("tie (%+.2f pp)", a.pp)
		}
		return "tie"
	}
	if text(item["unit"]) == "% of input" {
		return fmt.Sprintf("%s %+.2f pp higher", toolLabel(a.winner), a.pp)
	}
	verb := "faster"
	if !a.higherBetter {
		verb = "cheaper"
	}
	return fmt.Sprintf("%s %s %s", toolLabel(a.winner), factorText(a.factor), verb)
}

func verdictSample(item map[string]any) string {
	metric := strings.ToLower(text(item["metric"]))
	if strings.Contains(metric, "spend") {
		return count(item["n"]) + " sessions"
	}
	return count(item["n"]) + " steps"
}

func metricLabel(value any) string {
	switch text(value) {
	case "generation speed (weighted)":
		return "Speed, weighted"
	case "generation speed (median step)":
		return "Speed, median step"
	case "cost per 1k generated":
		return "Cost per 1k generated"
	case "cost per step":
		return "Cost per step"
	case "spend per session":
		return "Spend/session (all)"
	case "cache hit rate":
		return "Cache hit rate"
	default:
		return text(value)
	}
}

func findVerdict(verdicts []any, metric string) map[string]any {
	for _, entry := range verdicts {
		item, ok := entry.(map[string]any)
		if ok && text(item["metric"]) == metric {
			return item
		}
	}
	return nil
}

func verdictConfidence(doc map[string]any, scope, metric string) string {
	item := findVerdict(list(object(doc, "verdicts"), scope), metric)
	if item == nil {
		return "n/a"
	}
	return confidenceText(item["confidence"])
}

func familyFlags(value any) string {
	flags, ok := value.([]any)
	if !ok || len(flags) == 0 {
		return ""
	}
	parts := make([]string, 0, len(flags))
	for _, flag := range flags {
		switch text(flag) {
		case "low-sample":
			parts = append(parts, "low sample")
		case "cost-outlier":
			parts = append(parts, "cost outlier")
		case "speed-outlier":
			parts = append(parts, "speed outlier")
		case "one-sided":
			parts = append(parts, "one tool only")
		default:
			parts = append(parts, text(flag))
		}
	}
	return strings.Join(parts, ", ")
}

func verdictValue(item map[string]any) string {
	if item["unit"] == "% of input" {
		return signedPp(item["deltaPp"]) + " pp"
	}
	if item["ratio"] != nil {
		return ratioText(item["ratio"])
	}
	return "-"
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

func money2(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.2f", number)
}

func ratio0(numerator, denominator any) string {
	num, okN := pyjson.Float(numerator)
	den, okD := pyjson.Float(denominator)
	if !okN || !okD || den <= 0 {
		return "-"
	}
	return terminal.Count(int64(math.Round(num / den)))
}

func ratio1(numerator, denominator any) string {
	num, okN := pyjson.Float(numerator)
	den, okD := pyjson.Float(denominator)
	if !okN || !okD || den <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", num/den)
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

func percent2(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.2f", number*100)
}

func int64Value(value any) int64 {
	number, ok := pyjson.Int(value)
	if !ok {
		return 0
	}
	return number
}

func optFloatValue(value any) *float64 {
	number, ok := pyjson.Float(value)
	if !ok {
		return nil
	}
	return &number
}

func ratioText(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.2f×", number)
}

func winnerText(value any) string {
	switch value {
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

func confidenceText(value any) string {
	text, _ := value.(string)
	if text == "" {
		return "-"
	}
	return text
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
