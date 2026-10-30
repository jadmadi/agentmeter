// SPDX-License-Identifier: AGPL-3.0-only

// Package emit writes a finished schema v3 report to disk and serves it.
//
// It knows nothing about either tool: the HTML shell is passed in (the tool
// packages embed their own index.html) and the report is a plain
// map[string]any, so both builders share one writer with identical behaviour.
package emit

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jadmadi/agentmeter/internal/pyjson"
)

// Output file names, matching the Python writers.
const (
	JSONName = "telemetry-data.json"
	JSName   = "telemetry-data.js"
	HTMLName = "index.html"
)

// Files names the three artifacts of one report family.
type Files struct {
	JSON string
	JS   string
	HTML string
}

// TelemetryFiles is the per-tool schema v3 report file set.
var TelemetryFiles = Files{JSON: JSONName, JS: JSName, HTML: HTMLName}

// ComparisonFiles is the cross-tool comparison artifact set.
var ComparisonFiles = Files{JSON: "comparison-data.json", JS: "comparison-data.js", HTML: HTMLName}

// htmlScriptSource is the reference the tool shells ship with; the report
// directory keeps the data next to index.html, so it is rewritten.
const (
	htmlScriptSource = `src="data/telemetry-data.js"`
	htmlScriptTarget = `src="telemetry-data.js"`
)

// WriteReport writes outDir/telemetry-data.json, outDir/telemetry-data.js and
// outDir/index.html, creating outDir when needed, and returns the three paths.
//
// The JSON uses json.dumps(indent=1, ensure_ascii=False)-style output, the JS
// file is "window.<globalName> = <compact json>;\n", and the HTML is the
// supplied shell with src="data/telemetry-data.js" rewritten to
// src="telemetry-data.js". Each file is written through a temporary file and
// renamed into place, so a reader never sees a half-written report.
//
// globalName is the bare global, e.g. "CMD_TELEMETRY" or
// "OPENCODE_TELEMETRY"; pyjson.WriteJS rejects the "window." prefix.
func WriteReport(outDir string, data map[string]any, html []byte, globalName string) (jsonPath, jsPath, htmlPath string, err error) {
	return WriteReportFiles(outDir, TelemetryFiles, data, html, globalName)
}

// WriteReportFiles is WriteReport for an arbitrary artifact name set, used by
// the comparison report (comparison-data.json / comparison-data.js).
func WriteReportFiles(outDir string, files Files, data map[string]any, html []byte, globalName string) (jsonPath, jsPath, htmlPath string, err error) {
	if outDir == "" {
		return "", "", "", errors.New("emit: empty output directory")
	}
	if len(html) == 0 {
		return "", "", "", errors.New("emit: empty HTML shell")
	}
	if files.JSON == "" || files.JS == "" || files.HTML == "" {
		return "", "", "", errors.New("emit: incomplete file set")
	}

	// Encode everything before touching the filesystem so an encoding error
	// cannot leave a partially updated report behind.
	jsonBytes, err := pyjson.JSONBytes(data)
	if err != nil {
		return "", "", "", fmt.Errorf("emit: encode %s: %w", files.JSON, err)
	}
	jsBytes, err := pyjson.JSBytes(globalName, data)
	if err != nil {
		return "", "", "", fmt.Errorf("emit: encode %s: %w", files.JS, err)
	}
	htmlBytes := RewriteHTML(html)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", "", "", fmt.Errorf("emit: create %s: %w", outDir, err)
	}

	jsonPath = filepath.Join(outDir, files.JSON)
	jsPath = filepath.Join(outDir, files.JS)
	htmlPath = filepath.Join(outDir, files.HTML)

	if err := writeAtomic(jsonPath, jsonBytes); err != nil {
		return "", "", "", err
	}
	if err := writeAtomic(jsPath, jsBytes); err != nil {
		return "", "", "", err
	}
	if err := writeAtomic(htmlPath, htmlBytes); err != nil {
		return "", "", "", err
	}
	return jsonPath, jsPath, htmlPath, nil
}

// RewriteHTML makes the report shell self-contained for an arbitrary output
// directory by pointing the data script at the sibling telemetry-data.js.
// Every other occurrence of the source path (for example the boot-error help
// text) is left untouched, exactly like the Python writer's single .replace.
func RewriteHTML(html []byte) []byte {
	return bytes.ReplaceAll(html, []byte(htmlScriptSource), []byte(htmlScriptTarget))
}

// writeAtomic writes data to a temporary file in the destination directory,
// syncs it, and renames it over path.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("emit: create temp file for %s: %w", path, err)
	}
	tempName := temp.Name()
	cleanup := func() {
		temp.Close()
		os.Remove(tempName)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("emit: write %s: %w", path, err)
	}
	// CreateTemp uses 0600; report files are meant to be readable.
	if err := temp.Chmod(0o644); err != nil {
		cleanup()
		return fmt.Errorf("emit: chmod %s: %w", path, err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("emit: sync %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return fmt.Errorf("emit: close %s: %w", path, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return fmt.Errorf("emit: rename %s: %w", path, err)
	}
	return nil
}
