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
	allModels := fs.Bool("all-models", false, "use all models for the headline and verdicts (default: shared families)")
	jsonOut := fs.Bool("json", false, "print comparison-data.json to stdout instead of the human summary")
	noColor := fs.Bool("no-color", false, "disable ANSI color")

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
	if !*jsonOut {
		fmt.Fprintln(stdout, "Hint: re-run with --serve to open it.")
	}
	return 0
}

func likeForLikeAvailable(doc map[string]any) bool {
	likeForLike, ok := doc["likeForLike"].(map[string]any)
	if !ok {
		return false
	}
	available, _ := likeForLike["available"].(bool)
	return available
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

// printSummary renders the compact terminal comparison. The headline comes
// from the like-for-like (shared-family) block by default; --all-models shows
// the confounded aggregate instead.
func printSummary(w io.Writer, doc map[string]any, jsonPath, jsPath, htmlPath, scope string) {
	meta := object(doc, "meta")
	tools := object(meta, "tools")
	window := object(meta, "window")
	ocInfo := object(tools, "oc")
	cmdInfo := object(tools, "cmd")

	evidence := doc
	scopeLine := "all models · confounded by model and workload mix"
	scopeNote := "All models are included; model and workload mixes differ, so treat comparisons as context, not a harness verdict."
	if scope == "shared" {
		if likeForLike, ok := doc["likeForLike"].(map[string]any); ok && truthy(likeForLike["available"]) {
			evidence = likeForLike
			totals := object(likeForLike, "totals")
			scopeLine = fmt.Sprintf("shared-family pooled · %s shared families · %s shared steps",
				count(len(list(likeForLike, "families"))),
				count(int64Value(object(totals, "oc")["steps"])+int64Value(object(totals, "cmd")["steps"])))
			scopeNote = "Shared families are pooled by volume; one-sided families are excluded. Each family block shows its model match (OpenCode:Command Code model ids)."
		}
	}

	terminal.Title(w, "AgentMeter · OpenCode vs Command Code")
	fmt.Fprintln(w)
	terminal.Line(w, "SOURCE", text(ocInfo["label"]))
	terminal.Line(w, "", text(cmdInfo["label"]))
	mode := text(window["mode"])
	from := text(window["from"])
	to := text(window["to"])
	if from == "-" || to == "-" {
		terminal.Line(w, "WINDOW", fmt.Sprintf("%s · %s days", mode, count(window["days"])))
	} else {
		terminal.Line(w, "WINDOW", fmt.Sprintf("%s · %s → %s · %s days", mode, from, to, count(window["days"])))
	}
	terminal.Line(w, "SCOPE", scopeLine)
	wrapped(w, "  ", "  ", scopeNote, 78)

	totals := object(doc, "totals")
	ocTotals := object(totals, "oc")
	cmdTotals := object(totals, "cmd")
	terminal.Line(w, "SCALE", fmt.Sprintf("OpenCode: %s steps · %s sessions · %s models",
		count(ocTotals["steps"]), count(ocTotals["sessions"]), count(ocTotals["models"])))
	terminal.Line(w, "", fmt.Sprintf("Command Code: %s steps · %s sessions · %s models",
		count(cmdTotals["steps"]), count(cmdTotals["sessions"]), count(cmdTotals["models"])))

	printBottomLine(w, doc, scope)
	printMetricDetails(w, evidence, doc, scope)

	families := object(meta, "families")
	models := object(meta, "models")
	terminal.Line(w, "FAMILIES", fmt.Sprintf("%s shared · %s OpenCode-only · %s Command Code-only",
		count(families["shared"]), count(families["ocOnly"]), count(families["cmdOnly"])))
	terminal.Line(w, "", fmt.Sprintf("Shared families cover %s OpenCode / %s Command Code source models",
		count(models["sharedOC"]), count(models["sharedCMD"])))
	terminal.Line(w, "", fmt.Sprintf("Totals: %s OpenCode / %s Command Code source models",
		count(models["oc"]), count(models["cmd"])))

	printFamilyBlocks(w, doc, scope)
	printToolTable(w, evidence)
	printVerdicts(w, doc, scope)
	printHowToRead(w)

	terminal.Section(w, "REPORT")
	fmt.Fprintf(w, "  HTML: %s\n", htmlPath)
	fmt.Fprintf(w, "  JSON: %s\n", jsonPath)
	fmt.Fprintf(w, "  JS:   %s\n", jsPath)
}

func printFamilyBlocks(w io.Writer, doc map[string]any, scope string) {
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
		oc := object(item, "oc")
		cmd := object(item, "cmd")
		n := object(item, "n")
		conf := confidenceText(item["confidence"])
		fmt.Fprintf(w, "  %s", text(item["family"]))
		if conf != "" {
			fmt.Fprintf(w, "  (%s)", conf)
		}
		fmt.Fprintln(w)
		if sharedFamily {
			winner := object(item, "winner")
			delta := object(item, "delta")
			fmt.Fprintf(w, "    Speed: OpenCode %s vs Command Code %s tok/s\n",
				number(oc["weighted"]), number(cmd["weighted"]))
			fmt.Fprintf(w, "           → %s\n",
				speedAdvantageText(text(winner["weighted"]), delta["weightedRatio"]))
			fmt.Fprintf(w, "    Cost / 1k: OpenCode $%s vs Command Code $%s\n",
				money(oc["per1kGenerated"]), money(cmd["per1kGenerated"]))
			fmt.Fprintf(w, "           → %s\n",
				familyCostAdvantageText(text(winner["per1kGenerated"]), delta["per1kGeneratedRatio"]))
			fmt.Fprintf(w, "    Timed steps: %s OpenCode / %s Command Code\n",
				compactCount(n["oc"]), compactCount(n["cmd"]))
			fmt.Fprintf(w, "    Models: %d OpenCode / %d Command Code (%s)\n",
				len(list(oc, "models")), len(list(cmd, "models")), text(item["modelMatch"]))
		} else {
			side := "OpenCode only"
			steps := compactCount(n["oc"])
			models := len(list(oc, "models"))
			if int64Value(n["oc"]) == 0 {
				side = "Command Code only"
				steps = compactCount(n["cmd"])
				models = len(list(cmd, "models"))
			}
			fmt.Fprintf(w, "    %s · %s timed steps · %d model(s)\n", side, steps, models)
		}
		if flags := familyFlags(item["flags"]); flags != "" {
			fmt.Fprintf(w, "    Flags: %s\n", flags)
		}
		shown++
		if shown == 8 {
			break
		}
	}
	wrapped(w, "  ", "  ", "Model match = model ids per side (OpenCode:Command Code); only 1:1 is a true model pair, N:1 and 1:N pool provider variants. Flags: low sample = small timed sample; cost/speed outlier = outlier after trimming; one tool only = model present in only one tool.", 78)
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

func printToolTable(w io.Writer, evidence map[string]any) {
	toolSection := object(evidence, "tools")
	categories := list(toolSection, "normalized")
	rows, ocCoverage, cmdCoverage := toolShareRows(categories, 6)
	if len(rows) == 0 {
		return
	}
	terminal.Section(w, "TOOL MIX (share of tool calls)")
	terminal.Table(w,
		[]string{"CATEGORY", "OPENCODE", "COMMAND CODE", "DIFF"},
		rows,
		[]bool{false, true, true, true},
		[]int{26, 10, 12, 9})
	terminal.Note(w, fmt.Sprintf("coverage: OpenCode %.1f%% · Command Code %.1f%% (residual is other)",
		ocCoverage*100, cmdCoverage*100))
}

func toolShareRows(categories []any, limit int) ([][]string, float64, float64) {
	type entry struct {
		name     string
		ocShare  *float64
		cmdShare *float64
	}
	entries := make([]entry, 0, len(categories))
	var ocCoverage, cmdCoverage float64
	for _, raw := range categories {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		oc := object(item, "oc")
		cmd := object(item, "cmd")
		ocShare := optFloatValue(oc["shareCalls"])
		cmdShare := optFloatValue(cmd["shareCalls"])
		if ocShare != nil {
			ocCoverage += *ocShare
		}
		if cmdShare != nil {
			cmdCoverage += *cmdShare
		}
		entries = append(entries, entry{name: text(item["category"]), ocShare: ocShare, cmdShare: cmdShare})
	}
	// The normalized list is already ordered by calls, but sort defensively by
	// the larger of the two shares so the folded remainder is genuinely tail.
	sort.SliceStable(entries, func(i, j int) bool {
		return maxShare(entries[i].ocShare, entries[i].cmdShare) > maxShare(entries[j].ocShare, entries[j].cmdShare)
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
			ocText := "-"
			if item.ocShare != nil {
				ocText = fmt.Sprintf("%.1f%%", *item.ocShare*100)
			}
			cmdText := "-"
			if item.cmdShare != nil {
				cmdText = fmt.Sprintf("%.1f%%", *item.cmdShare*100)
			}
			rows = append(rows, []string{
				item.name,
				ocText,
				cmdText,
				signedPp(shareDiff(item.ocShare, item.cmdShare)) + " pp",
			})
			continue
		}
		if item.ocShare != nil {
			otherOC += *item.ocShare
		}
		if item.cmdShare != nil {
			otherCMD += *item.cmdShare
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
	return rows, ocCoverage, cmdCoverage
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
			verdictResult(item),
			confidenceText(item["confidence"]),
			verdictSample(item),
		})
	}
	terminal.Table(w,
		[]string{"METRIC", "RESULT", "CONFIDENCE", "N"},
		rows,
		[]bool{false, false, false, false},
		[]int{22, 27, 10, 15})
	terminal.Note(w, "RESULT names the winner and the advantage; no ratio inversion is needed.")
	terminal.Note(w, "N is the smaller side's sample; speed/cache counts are timed steps.")
	terminal.Note(w, "Shared-family verdicts are pooled by volume; family blocks show model match (1:1 = true model pair).")
}

func printBottomLine(w io.Writer, doc map[string]any, scope string) {
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

	terminal.Section(w, "BOTTOM LINE")
	if speed != nil {
		a := advantageOf(speed)
		if a.winner == "tie" {
			wrapped(w, "  • ", "    ", "Generation speed was effectively tied.", 76)
		} else {
			sentence := fmt.Sprintf("%s is %s faster at the weighted rate", toolLabel(a.winner), factorText(a.factor))
			if speedMedian != nil {
				m := advantageOf(speedMedian)
				if m.winner != "tie" {
					sentence += fmt.Sprintf(" and %s faster at the median step", factorText(m.factor))
				}
			}
			sentence += fmt.Sprintf(" (%s; %s timed steps).", confidenceText(speed["confidence"]), count(speed["n"]))
			wrapped(w, "  • ", "    ", sentence, 76)
		}
	}
	if cost1k != nil {
		a := advantageOf(cost1k)
		sentence := ""
		if a.winner == "tie" {
			sentence = "Per-1k-generated cost was tied"
		} else {
			sentence = fmt.Sprintf("%s is %s cheaper per 1k generated tokens (%s)",
				toolLabel(a.winner), factorText(a.factor), confidenceText(cost1k["confidence"]))
		}
		if costStep != nil {
			s := advantageOf(costStep)
			if s.winner != "tie" {
				sentence += fmt.Sprintf("; %s is %s cheaper per step (%s)",
					toolLabel(s.winner), factorText(s.factor), confidenceText(costStep["confidence"]))
			}
		}
		wrapped(w, "  • ", "    ", sentence+".", 76)
	}
	if spend != nil {
		a := advantageOf(spend)
		sentence := ""
		if a.winner == "tie" {
			sentence = "All-model spend per session was tied (" + confidenceText(spend["confidence"]) + ")"
		} else {
			sentence = fmt.Sprintf("All-model spend per session: %s is %s cheaper (%s)",
				toolLabel(a.winner), factorText(a.factor), confidenceText(spend["confidence"]))
		}
		sentence += "; model and workload mixes differ, so this is context, not a harness verdict."
		wrapped(w, "  • ", "    ", sentence, 76)
	}
	if cache != nil {
		a := advantageOf(cache)
		sentence := "Cache hit rates are effectively tied"
		if a.winner == "tie" {
			sentence += fmt.Sprintf(" (%+.2f pp).", a.pp)
		} else {
			sentence += fmt.Sprintf(" (%s %+.2f pp).", toolLabel(a.winner), a.pp)
		}
		wrapped(w, "  • ", "    ", sentence, 76)
	}
}

func printMetricDetails(w io.Writer, evidence, doc map[string]any, scope string) {
	throughput := object(evidence, "throughput")
	ocSpeed := object(throughput, "oc")
	cmdSpeed := object(throughput, "cmd")
	speedDelta := object(throughput, "delta")
	speedWinner := object(throughput, "winner")
	terminal.Line(w, "SPEED", fmt.Sprintf("weighted: OpenCode %s vs Command Code %s tok/s",
		number(ocSpeed["weighted"]), number(cmdSpeed["weighted"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s · confidence: %s",
		speedAdvantageText(text(speedWinner["weighted"]), speedDelta["weightedRatio"]),
		verdictConfidence(doc, scope, "generation speed (weighted)")))
	terminal.Line(w, "", fmt.Sprintf("median:   OpenCode %s vs Command Code %s tok/s",
		number(ocSpeed["median"]), number(cmdSpeed["median"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s · confidence: %s",
		speedAdvantageText(text(speedWinner["median"]), speedDelta["medianRatio"]),
		verdictConfidence(doc, scope, "generation speed (median step)")))

	cost := object(evidence, "cost")
	ocCost := object(cost, "oc")
	cmdCost := object(cost, "cmd")
	costDelta := object(cost, "delta")
	costWinner := object(cost, "winner")
	terminal.Line(w, "COST", fmt.Sprintf("per 1k generated: OpenCode $%s vs Command Code $%s",
		money(ocCost["per1kGenerated"]), money(cmdCost["per1kGenerated"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s · confidence: %s",
		costAdvantageText(text(costWinner["per1kGenerated"]), costDelta["per1kGeneratedRatio"]),
		verdictConfidence(doc, scope, "cost per 1k generated")))
	terminal.Line(w, "", fmt.Sprintf("per step: OpenCode $%s vs Command Code $%s",
		money(ocCost["perStep"]), money(cmdCost["perStep"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s · confidence: %s",
		costAdvantageText(text(costWinner["perStep"]), costDelta["perStepRatio"]),
		verdictConfidence(doc, scope, "cost per step")))
	input := fmt.Sprintf("input: both $%s per 1M tokens", money(ocCost["perMillionInput"]))
	if int64Value(costDelta["perMillionInputRatio"]) != 1 {
		input = fmt.Sprintf("input: OpenCode $%s vs Command Code $%s per 1M tokens",
			money(ocCost["perMillionInput"]), money(cmdCost["perMillionInput"]))
	}
	terminal.Line(w, "", input)

	spendCost := object(doc, "cost")
	ocSpend := object(spendCost, "oc")
	cmdSpend := object(spendCost, "cmd")
	spendDelta := object(spendCost, "delta")
	spendWinner := object(spendCost, "winner")
	terminal.Line(w, "SPEND", fmt.Sprintf("all models, per session: OpenCode $%s vs Command Code $%s",
		money(ocSpend["totalSpendPerSession"]), money(cmdSpend["totalSpendPerSession"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s · confidence: %s",
		costAdvantageText(text(spendWinner["totalSpendPerSession"]), spendDelta["totalSpendPerSessionRatio"]),
		verdictConfidence(doc, scope, "spend per session")))

	cache := object(evidence, "cache")
	ocCache := object(cache, "oc")
	cmdCache := object(cache, "cmd")
	terminal.Line(w, "CACHE", fmt.Sprintf("hit rate: OpenCode %s%% vs Command Code %s%%",
		percent2(ocCache["hitRate"]), percent2(cmdCache["hitRate"])))
	terminal.Line(w, "", fmt.Sprintf("→ %s",
		cacheAdvantageText(winner(cache, "hitRate"), object(cache, "delta")["hitRatePp"])))
}

func printHowToRead(w io.Writer) {
	terminal.Section(w, "HOW TO READ THIS")
	bullets := []string{
		`"OpenCode 3.48× faster" means OpenCode produced 3.48× more generated tokens per second; "Command Code 1.24× cheaper" means Command Code spent 1.24× less per unit. The winner is named, so no ratio inversion is needed.`,
		"Weighted speed = total generated tokens ÷ total generation time. Median speed = the typical step; it resists a few very long steps.",
		"N is the smaller side's sample: timed steps for speed and cache, steps for cost, sessions for spend.",
		"clear = the bootstrap interval excludes a tie; weak = the direction is suggestive but overlaps a tie; confounded = model or workload mixes differ; tie = within 5% relative (cache: under 1 pp or 5%).",
		"Family flags: low sample = small timed sample; cost/speed outlier = an outlier after trimming; one tool only = the model is present in only one tool.",
		"Like-for-like pools the shared families by volume. Family blocks show model match (OpenCode:Command Code model ids); only 1:1 rows are a true model-to-model pair. N:1 and 1:N rows pool provider variants.",
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
	switch tool {
	case "oc":
		return "OpenCode"
	case "cmd":
		return "Command Code"
	case "tie":
		return "tie"
	default:
		return tool
	}
}

func factorText(factor float64) string {
	return fmt.Sprintf("%.2f×", factor)
}

func advantageOf(item map[string]any) struct {
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
	ratio, ok := pyjson.Float(item["ratio"])
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
	if (result.higherBetter && result.winner == "cmd") || (!result.higherBetter && result.winner == "oc") {
		result.factor = 1 / ratio
	}
	if result.factor < 1 {
		result.factor = 1 / result.factor
	}
	result.pct = (result.factor - 1) * 100
	result.ok = true
	return result
}

func speedAdvantageText(winner string, ratio any) string {
	if winner == "tie" {
		return "tie"
	}
	value, ok := pyjson.Float(ratio)
	if !ok || value <= 0 {
		return "-"
	}
	factor := value
	if winner == "cmd" {
		factor = 1 / value
	}
	if factor < 1 {
		factor = 1 / factor
	}
	return fmt.Sprintf("%s %s faster", toolLabel(winner), factorText(factor))
}

func costAdvantageText(winner string, ratio any) string {
	if winner == "tie" {
		return "tie"
	}
	value, ok := pyjson.Float(ratio)
	if !ok || value <= 0 {
		return "-"
	}
	factor := value
	if winner == "oc" {
		factor = 1 / value
	}
	if factor < 1 {
		factor = 1 / factor
	}
	return fmt.Sprintf("%s %s cheaper", toolLabel(winner), factorText(factor))
}

func familyCostAdvantageText(winner string, ratio any) string {
	if winner == "tie" {
		return "tie"
	}
	value, ok := pyjson.Float(ratio)
	if !ok || value <= 0 {
		return "-"
	}
	pct := (value - 1) * 100
	if pct < 0 {
		pct = -pct
	}
	if pct < 100 {
		return fmt.Sprintf("%s %.0f%% cheaper", toolLabel(winner), pct)
	}
	factor := value
	if winner == "oc" {
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

func verdictResult(item map[string]any) string {
	a := advantageOf(item)
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
		return "Spend per session"
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
