// SPDX-License-Identifier: AGPL-3.0-only

// Package ocreport builds the OpenCode schema v3 report.
//
// It is a line-by-line port of opencode-telemetry/py/telemetry.py:build(): the
// same sections, field names, ordering, nulls, sentinels, rounding and
// accumulation order. Source reading lives in internal/source/opencode; the
// Python-compatible numerics live in internal/stats.
package ocreport

import (
	"time"
)

// Thresholds are the timed-row censoring bounds (telemetry.py DEFAULT_THRESHOLDS).
type Thresholds struct {
	MinSeconds float64
	MaxSeconds float64
	MinTokens  int64
	MinRate    float64
	MaxRate    float64
}

// DefaultThresholds matches the Python defaults:
//
//	0.5s <= LLM seconds <= 300s, generated >= 20 tokens, 3..400 tokens/s.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MinSeconds: 0.5,
		MaxSeconds: 300.0,
		MinTokens:  20,
		MinRate:    3.0,
		MaxRate:    400.0,
	}
}

// ThresholdMap renders the thresholds in the schema's fixed key order.
func (t Thresholds) Map() map[string]any {
	return map[string]any{
		"minSeconds": t.MinSeconds,
		"maxSeconds": t.MaxSeconds,
		"minTokens":  t.MinTokens,
		"minRate":    t.MinRate,
		"maxRate":    t.MaxRate,
	}
}

// Options controls one build.
type Options struct {
	DBPath        string
	ModelFilters  []string
	Since         string
	Until         string
	IncludeTitles bool
	FullPaths     bool
	// Thresholds overrides the defaults when non-nil.
	Thresholds *Thresholds
	// Baseline is "YYYY-MM-DD..YYYY-MM-DD" (empty means the selected rows).
	Baseline string
	// Now pins generatedAt and the timezone label; zero means time.Now().
	Now time.Time
}

// effectiveThresholds returns the configured thresholds or the defaults.
func (o Options) effectiveThresholds() Thresholds {
	if o.Thresholds == nil {
		return DefaultThresholds()
	}
	return *o.Thresholds
}
