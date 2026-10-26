// SPDX-License-Identifier: AGPL-3.0-only
// Package testutil contains fixture and normalization helpers shared by the
// Go parity tests. It is compiled into test binaries, not the CLI.
package testutil

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// NormalizeReport returns a deep copy of a report with the volatile
// generatedAt field removed. All other values are preserved exactly so the
// Python-vs-Go comparison remains strict.
func NormalizeReport(data map[string]any) map[string]any {
	clone, ok := deepCopy(data).(map[string]any)
	if !ok {
		return nil
	}
	if meta, ok := clone["meta"].(map[string]any); ok {
		delete(meta, "generatedAt")
	}
	return clone
}

// ReadReport loads a telemetry-data.json report.
func ReadReport(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return data, nil
}

// WriteReport writes an indented JSON report for diagnostics.
func WriteReport(path string, data map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(data, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func deepCopy(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = deepCopy(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = deepCopy(item)
		}
		return out
	default:
		return v
	}
}
