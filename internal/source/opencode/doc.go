// SPDX-License-Identifier: AGPL-3.0-only

// Package ocsource loads OpenCode's v2 SQLite store (session_v2 /
// session_message) read-only and reconstructs per-step rows, the error/empty
// ledger, session metadata and store-wide event counters.
//
// It performs no aggregation: every metric, fit and distribution belongs to
// internal/report/opencode. The package mirrors opencode-telemetry/py/
// telemetry.py's load_rows() exactly, including the SQL, the ordering, the
// fork/dedup fingerprint, interval merging and proportional tool-time
// allocation.
package ocsource
