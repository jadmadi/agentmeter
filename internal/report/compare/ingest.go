// SPDX-License-Identifier: AGPL-3.0-only

package compare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	shared "github.com/jadmadi/agentmeter/internal/compare"
	reportcmd "github.com/jadmadi/agentmeter/internal/report/cmd"
	reportmuse "github.com/jadmadi/agentmeter/internal/report/muse"
	ocreport "github.com/jadmadi/agentmeter/internal/report/opencode"
	sourcecmd "github.com/jadmadi/agentmeter/internal/source/cmd"
	sourcemuse "github.com/jadmadi/agentmeter/internal/source/muse"
	ocsource "github.com/jadmadi/agentmeter/internal/source/opencode"
)

// ReportName is the schema v3 report file the builders emit.
const ReportName = "telemetry-data.json"

// DefaultCMDPath is the Command Code session root.
const DefaultCMDPath = "~/.commandcode/projects"

// DefaultMusePath is the Muse sessions root.
const DefaultMusePath = "~/.local/share/muse/sessions"

// DefaultOCPath is the OpenCode store autodetected by the oc loader.
func DefaultOCPath() string { return ocsource.DefaultDBPath() }

// Detect resolves a user-supplied tool value into an Input. A directory
// containing telemetry-data.json (or a .json file) is a pre-built report; a
// directory for Command Code or Muse is a live session root; a non-JSON file
// for OpenCode is a live SQLite store.
func Detect(tool, raw string) (shared.Input, error) {
	switch tool {
	case shared.ToolOC:
		return detectOC(raw)
	case shared.ToolCMD:
		return detectCMD(raw)
	case shared.ToolMuse:
		return detectMuse(raw)
	default:
		return shared.Input{}, fmt.Errorf("compare: unknown tool %q", tool)
	}
}

func detectOC(raw string) (shared.Input, error) {
	path := expandUser(raw)
	if path == "" {
		path = DefaultOCPath()
	}
	info, err := os.Stat(path)
	if err != nil {
		return shared.Input{}, fmt.Errorf("compare: oc source %q: %w", raw, err)
	}
	if info.IsDir() {
		reportPath := filepath.Join(path, ReportName)
		if _, err := os.Stat(reportPath); err == nil {
			return reportInput(shared.ToolOC, "OpenCode", reportPath), nil
		}
		dbPath := filepath.Join(path, "opencode.db")
		if _, err := os.Stat(dbPath); err == nil {
			return liveInput(shared.ToolOC, "OpenCode SQLite", dbPath), nil
		}
		return shared.Input{}, fmt.Errorf("compare: oc directory %q has neither %s nor opencode.db", path, ReportName)
	}
	if strings.EqualFold(filepath.Ext(path), ".json") {
		return reportInput(shared.ToolOC, "OpenCode", path), nil
	}
	if err := checkSQLite(path); err != nil {
		return shared.Input{}, err
	}
	return liveInput(shared.ToolOC, "OpenCode SQLite", path), nil
}

func detectCMD(raw string) (shared.Input, error) {
	path := expandUser(raw)
	if path == "" {
		path = expandUser(DefaultCMDPath)
	}
	info, err := os.Stat(path)
	if err != nil {
		return shared.Input{}, fmt.Errorf("compare: cmd source %q: %w", raw, err)
	}
	if info.IsDir() {
		reportPath := filepath.Join(path, ReportName)
		if _, err := os.Stat(reportPath); err == nil {
			return reportInput(shared.ToolCMD, "Command Code", reportPath), nil
		}
		return liveInput(shared.ToolCMD, "Command Code sessions", path), nil
	}
	if strings.EqualFold(filepath.Ext(path), ".json") {
		return reportInput(shared.ToolCMD, "Command Code", path), nil
	}
	return shared.Input{}, fmt.Errorf("compare: cmd source %q is neither a report, a report directory, nor a session root", path)
}

func detectMuse(raw string) (shared.Input, error) {
	path := expandUser(raw)
	if path == "" {
		path = expandUser(DefaultMusePath)
	}
	info, err := os.Stat(path)
	if err != nil {
		return shared.Input{}, fmt.Errorf("compare: muse source %q: %w", raw, err)
	}
	if info.IsDir() {
		reportPath := filepath.Join(path, ReportName)
		if _, err := os.Stat(reportPath); err == nil {
			return reportInput(shared.ToolMuse, "Muse", reportPath), nil
		}
		return liveInput(shared.ToolMuse, "Muse sessions", path), nil
	}
	if strings.EqualFold(filepath.Ext(path), ".json") {
		return reportInput(shared.ToolMuse, "Muse", path), nil
	}
	return shared.Input{}, fmt.Errorf("compare: muse source %q is neither a report, a report directory, nor a session root", path)
}

func reportInput(tool, name, path string) shared.Input {
	return shared.Input{
		Tool:  tool,
		Kind:  shared.KindReport,
		Path:  path,
		Label: fmt.Sprintf("%s report · %s", name, displayPath(path)),
	}
}

func liveInput(tool, name, path string) shared.Input {
	return shared.Input{
		Tool:  tool,
		Kind:  shared.KindLive,
		Path:  path,
		Label: fmt.Sprintf("%s · %s", name, displayPath(path)),
	}
}

// Load reads the input into the canonical comparison view, building a live
// report when the source is a store or session root.
func Load(input shared.Input) (*shared.Report, error) {
	var data map[string]any
	var err error
	if input.Kind == shared.KindReport {
		data, err = readReportFile(input.Path)
	} else {
		data, err = buildLive(input)
	}
	if err != nil {
		return nil, err
	}
	return shared.Parse(input.Tool, input, data)
}

// LoadAgainst loads an input and, when it already is a report, verifies the
// file's schema version before the comparison parse reports a deeper error.
func readReportFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("compare: read %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("compare: parse %s: %w", path, err)
	}
	if data == nil {
		return nil, fmt.Errorf("compare: %s is not a JSON object", path)
	}
	return data, nil
}

func buildLive(input shared.Input) (map[string]any, error) {
	switch input.Tool {
	case shared.ToolOC:
		data, err := ocreport.Build(ocreport.Options{
			DBPath:        input.Path,
			IncludeTitles: true,
		})
		if err != nil {
			return nil, fmt.Errorf("compare: build oc report: %w", err)
		}
		return data, nil
	case shared.ToolCMD:
		source := sourcecmd.Load(sourcecmd.Filters{
			Root:          input.Path,
			IncludeTitles: true,
		})
		data, err := reportcmd.Build(source, reportcmd.Options{
			Root:          input.Path,
			IncludeTitles: true,
		})
		if err != nil {
			return nil, fmt.Errorf("compare: build cmd report: %w", err)
		}
		return data, nil
	case shared.ToolMuse:
		source := sourcemuse.Load(sourcemuse.Filters{
			Root:          input.Path,
			IncludeTitles: true,
		})
		data, err := reportmuse.Build(source, reportmuse.Options{
			Root:          input.Path,
			IncludeTitles: true,
		})
		if err != nil {
			return nil, fmt.Errorf("compare: build muse report: %w", err)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("compare: unknown tool %q", input.Tool)
	}
}

// Fingerprint returns a cheap change signature for --watch. Live sources use
// the tool's own store fingerprint; report inputs hash the file/directory
// metadata.
func Fingerprint(input shared.Input) string {
	if input.Kind == shared.KindLive {
		switch input.Tool {
		case shared.ToolOC:
			return ocsource.Fingerprint(input.Path)
		case shared.ToolMuse:
			return sourcemuse.Fingerprint(input.Path)
		default:
			return sourcecmd.Fingerprint(input.Path)
		}
	}
	return statFingerprint(input.Path)
}

func statFingerprint(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	if !info.IsDir() {
		return fmt.Sprintf("%s|%d|%d", path, info.Size(), info.ModTime().UnixNano())
	}
	type entry struct {
		path string
		size int64
		mod  int64
	}
	entries := make([]entry, 0, 64)
	_ = filepath.WalkDir(path, func(current string, item fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return nil
		}
		entries = append(entries, entry{path: current, size: info.Size(), mod: info.ModTime().UnixNano()})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	var builder strings.Builder
	for _, item := range entries {
		fmt.Fprintf(&builder, "%s|%d|%d;", item.path, item.size, item.mod)
	}
	return builder.String()
}

func checkSQLite(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("compare: open %s: %w", path, err)
	}
	defer file.Close()
	header := make([]byte, 16)
	if _, err := file.Read(header); err != nil {
		return fmt.Errorf("compare: read %s: %w", path, err)
	}
	if string(header) != "SQLite format 3\x00" {
		return fmt.Errorf("compare: %s is not a SQLite store", path)
	}
	return nil
}

func expandUser(path string) string {
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}

func displayPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	return strings.Replace(path, home, "~", 1)
}

// NowUTC formats a timestamp the way both builders do.
func NowUTC(now time.Time) string {
	return now.UTC().Format("2006-01-02T15:04:05") + "+00:00"
}
