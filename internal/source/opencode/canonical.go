// SPDX-License-Identifier: AGPL-3.0-only

package ocsource

import (
	"fmt"

	"github.com/jadmadi/agentmeter/internal/pyjson"
)

// DedupDropKeys are removed recursively before hashing so a forked copy of a
// message (new ids, new path) still hashes like its original.
var DedupDropKeys = map[string]bool{
	"id":           true,
	"parentID":     true,
	"sessionID":    true,
	"sessionId":    true,
	"messageID":    true,
	"callID":       true,
	"snapshot":     true,
	"path":         true,
	"time_created": true,
	"time_updated": true,
}

// NormalizePayload returns a fork-comparable payload with ids and location
// fields removed (telemetry.py:normalize_payload).
func NormalizePayload(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if DedupDropKeys[key] {
				continue
			}
			out[key] = NormalizePayload(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = NormalizePayload(item)
		}
		return out
	default:
		return value
	}
}

// CanonicalJSON renders a normalized payload exactly like Python's
// json.dumps(sort_keys=True, separators=(",", ":"), ensure_ascii=False).
//
// The CPython-compatible encoder lives in internal/pyjson so the audit, the
// dedup signatures and the emitted JSON share one implementation.
func CanonicalJSON(value any) (string, error) {
	encoded, err := pyjson.Canonical(value)
	if err != nil {
		return "", fmt.Errorf("canonical JSON: %w", err)
	}
	return string(encoded), nil
}

// Signature is the SHA-256 hex digest of the canonical payload, matching
// hashlib.sha256(normalized.encode()).hexdigest().
func Signature(value any) (string, error) {
	signature, err := pyjson.Signature(value)
	if err != nil {
		return "", fmt.Errorf("canonical JSON: %w", err)
	}
	return signature, nil
}
