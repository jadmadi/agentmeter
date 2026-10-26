// SPDX-License-Identifier: AGPL-3.0-only

// Package pyjson reproduces the JSON behaviour the Python telemetry builders
// rely on.
//
// Two different encodings live here and they are deliberately not the same:
//
//   - Canonical/Signature mirror
//     json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).
//     They are used for OpenCode fork/duplicate signatures, so the byte output
//     must match CPython exactly: sorted object keys, no HTML escaping, raw
//     non-ASCII, CPython's shortest-round-trip float formatting (including the
//     trailing ".0" on integral floats) and CPython's control-character
//     escapes.
//   - WriteJSON/WriteJS mirror the report writers:
//     json.dumps(data, indent=1, ensure_ascii=False) and the compact
//     json.dumps(..., separators=(",", ":")) payload wrapped in the
//     "window.<GLOBAL> = ...;\n" script.
//
// Everything here is standard library only.
package pyjson
