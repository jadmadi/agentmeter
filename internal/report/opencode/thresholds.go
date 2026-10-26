// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	ocsource "github.com/jadmadi/agentmeter/internal/source/opencode"
)

// IsTimed mirrors telemetry.py:is_timed with the configured thresholds:
//
//	dt > 0, minSeconds <= dt <= maxSeconds,
//	gen >= minTokens, minRate <= gen/dt <= maxRate.
func (t Thresholds) IsTimed(row *ocsource.Row) bool {
	if row.DT == nil || *row.DT <= 0 {
		return false
	}
	rate := float64(row.Gen) / *row.DT
	return t.MinSeconds <= *row.DT && *row.DT <= t.MaxSeconds &&
		float64(row.Gen) >= float64(t.MinTokens) &&
		t.MinRate <= rate && rate <= t.MaxRate
}

// Bucket mirrors telemetry.py:timing_bucket. The order of the checks is part of
// the semantics: the first matching reason wins.
func (t Thresholds) Bucket(row *ocsource.Row) string {
	if row.DT == nil || *row.DT <= 0 {
		return "noTiming"
	}
	if float64(row.Gen) < float64(t.MinTokens) {
		return "tooShort"
	}
	if *row.DT < t.MinSeconds {
		return "tooShortDuration"
	}
	if *row.DT > t.MaxSeconds {
		return "tooLong"
	}
	rate := float64(row.Gen) / *row.DT
	if rate < t.MinRate {
		return "slowRate"
	}
	if rate > t.MaxRate {
		return "fastRate"
	}
	return "plausible"
}
