// SPDX-License-Identifier: AGPL-3.0-only

package ocsource

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// payload is the subset of an assistant session_message payload that the
// builder reads. Nothing else in the payload is inspected or emitted, so
// prompts, assistant text, tool arguments and file contents never leave the
// process.
type payload struct {
	Provider string
	ModelID  string
	Variant  string

	Output     int64
	Reasoning  int64
	CacheRead  int64
	CacheWrite int64
	FreshInput int64
	Generated  int64

	Created   int64
	Streamed  int64
	Completed int64

	Cost   float64
	Agent  string
	Finish string

	ToolNames   []string
	ToolDetails []ToolCall
	ToolWindows []Interval

	FirstBlockCreated *int64
	NReasoning        int
	NText             int

	// raw is the decoded payload, used for the fork/dedup signature only.
	raw map[string]any
}

// decodePayload parses one session_message.data blob with json.Number so
// integers stay integers (Python's json.loads yields int for integer literals).
func decodePayload(data string) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader([]byte(data)))
	decoder.UseNumber()
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	object, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload is %T, not an object", doc)
	}
	return object, nil
}

// parsePayload extracts the fields read by telemetry.py:load_rows.
func parsePayload(doc map[string]any, msgID string) (*payload, error) {
	p := &payload{raw: doc}

	model, err := getObject(doc, "model")
	if err != nil {
		return nil, wrapField(msgID, "model", err)
	}
	if p.Provider, err = getString(model, "providerID"); err != nil {
		return nil, wrapField(msgID, "model.providerID", err)
	}
	if p.ModelID, err = getString(model, "id"); err != nil {
		return nil, wrapField(msgID, "model.id", err)
	}
	if p.Variant, err = getString(model, "variant"); err != nil {
		return nil, wrapField(msgID, "model.variant", err)
	}

	tokens, err := getObject(doc, "tokens")
	if err != nil {
		return nil, wrapField(msgID, "tokens", err)
	}
	if p.Output, err = getInt(tokens, "output"); err != nil {
		return nil, wrapField(msgID, "tokens.output", err)
	}
	if p.Reasoning, err = getInt(tokens, "reasoning"); err != nil {
		return nil, wrapField(msgID, "tokens.reasoning", err)
	}
	if p.FreshInput, err = getInt(tokens, "input"); err != nil {
		return nil, wrapField(msgID, "tokens.input", err)
	}
	cache, err := getObject(tokens, "cache")
	if err != nil {
		return nil, wrapField(msgID, "tokens.cache", err)
	}
	if p.CacheRead, err = getInt(cache, "read"); err != nil {
		return nil, wrapField(msgID, "tokens.cache.read", err)
	}
	if p.CacheWrite, err = getInt(cache, "write"); err != nil {
		return nil, wrapField(msgID, "tokens.cache.write", err)
	}
	p.Generated = p.Output + p.Reasoning

	clock, err := getObject(doc, "time")
	if err != nil {
		return nil, wrapField(msgID, "time", err)
	}
	if p.Created, err = getInt(clock, "created"); err != nil {
		return nil, wrapField(msgID, "time.created", err)
	}
	if p.Streamed, err = getInt(clock, "streamed"); err != nil {
		return nil, wrapField(msgID, "time.streamed", err)
	}
	if p.Completed, err = getInt(clock, "completed"); err != nil {
		return nil, wrapField(msgID, "time.completed", err)
	}

	if p.Cost, err = getFloat(doc, "cost"); err != nil {
		return nil, wrapField(msgID, "cost", err)
	}
	if p.Agent, err = getString(doc, "agent"); err != nil {
		return nil, wrapField(msgID, "agent", err)
	}
	if p.Agent == "" {
		p.Agent = "unknown"
	}
	if p.Finish, err = getString(doc, "finish"); err != nil {
		return nil, wrapField(msgID, "finish", err)
	}
	if p.Finish == "" {
		p.Finish = "unknown"
	}

	blocks, err := getBlocks(doc)
	if err != nil {
		return nil, wrapField(msgID, "content", err)
	}
	for _, block := range blocks {
		kind, err := getString(block, "type")
		if err != nil {
			return nil, wrapField(msgID, "content.type", err)
		}
		switch kind {
		case "tool":
			name, err := getString(block, "name")
			if err != nil {
				return nil, wrapField(msgID, "content.name", err)
			}
			if name == "" {
				name = "(unnamed)"
			}
			blockTime, err := getObject(block, "time")
			if err != nil {
				return nil, wrapField(msgID, "content.time", err)
			}
			ran, err := getInt(blockTime, "ran")
			if err != nil {
				return nil, wrapField(msgID, "content.time.ran", err)
			}
			completed, err := getInt(blockTime, "completed")
			if err != nil {
				return nil, wrapField(msgID, "content.time.completed", err)
			}
			created, err := getInt(blockTime, "created")
			if err != nil {
				return nil, wrapField(msgID, "content.time.created", err)
			}
			var runtime *float64
			if ran != 0 && completed != 0 && completed > ran {
				value := float64(completed-ran) / 1000.0
				runtime = &value
				p.ToolWindows = append(p.ToolWindows, Interval{Start: ran, End: completed})
			}
			state, err := getObject(block, "state")
			if err != nil {
				return nil, wrapField(msgID, "content.state", err)
			}
			status, err := getString(state, "status")
			if err != nil {
				return nil, wrapField(msgID, "content.state.status", err)
			}
			p.ToolNames = append(p.ToolNames, name)
			p.ToolDetails = append(p.ToolDetails, ToolCall{
				Name:      name,
				Ran:       ran,
				Completed: completed,
				Runtime:   runtime,
				Error:     status == "error",
			})
			if created != 0 {
				p.FirstBlockCreated = minInt64Ptr(p.FirstBlockCreated, created)
			}
		case "reasoning":
			p.NReasoning++
			blockTime, err := getObject(block, "time")
			if err != nil {
				return nil, wrapField(msgID, "content.time", err)
			}
			created, err := getInt(blockTime, "created")
			if err != nil {
				return nil, wrapField(msgID, "content.time.created", err)
			}
			if created != 0 {
				p.FirstBlockCreated = minInt64Ptr(p.FirstBlockCreated, created)
			}
		case "text":
			p.NText++
		}
	}
	return p, nil
}

// minInt64Ptr keeps the running minimum of a block timestamp, mirroring
// `first_block_created if first_block_created is None else min(...)`.
func minInt64Ptr(current *int64, value int64) *int64 {
	if current == nil {
		v := value
		return &v
	}
	if value < *current {
		v := value
		return &v
	}
	return current
}

func wrapField(msgID, field string, err error) error {
	return fmt.Errorf("message %s: %s: %w", msgID, field, err)
}

// isFalsy mirrors Python truthiness for the JSON value kinds we can see.
func isFalsy(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case bool:
		return !v
	case string:
		return v == ""
	case json.Number:
		f, err := v.Float64()
		return err == nil && f == 0
	case float64:
		return v == 0
	case int64:
		return v == 0
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	default:
		return false
	}
}

// getObject returns m[key] as an object; nil and empty objects both yield an
// empty map, matching `value or {}`.
func getObject(m map[string]any, key string) (map[string]any, error) {
	if m == nil {
		return map[string]any{}, nil
	}
	value, ok := m[key]
	if !ok || value == nil {
		return map[string]any{}, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object, got %T", value)
	}
	return object, nil
}

// getString returns a string field, with "" for null/absent (Python `or ""`).
func getString(m map[string]any, key string) (string, error) {
	if m == nil {
		return "", nil
	}
	value, ok := m[key]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		if isFalsy(value) {
			return "", nil
		}
		return "", fmt.Errorf("expected string, got %T", value)
	}
	return text, nil
}

// getInt mirrors `int(value or 0)`.
func getInt(m map[string]any, key string) (int64, error) {
	if m == nil {
		return 0, nil
	}
	value, ok := m[key]
	if !ok || isFalsy(value) {
		return 0, nil
	}
	return asInt(value)
}

// getFloat mirrors `float(value or 0.0)`.
func getFloat(m map[string]any, key string) (float64, error) {
	if m == nil {
		return 0, nil
	}
	value, ok := m[key]
	if !ok || isFalsy(value) {
		return 0, nil
	}
	return asFloat(value)
}

// asInt converts a JSON scalar the way Python's int() does: floats truncate
// toward zero and numeric strings parse.
func asInt(value any) (int64, error) {
	switch v := value.(type) {
	case json.Number:
		if integer, err := v.Int64(); err == nil {
			return integer, nil
		}
		f, err := v.Float64()
		if err != nil {
			return 0, fmt.Errorf("invalid number %q", string(v))
		}
		return int64(f), nil
	case string:
		integer, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid integer %q", v)
		}
		return integer, nil
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	default:
		return 0, fmt.Errorf("expected number, got %T", value)
	}
}

// asFloat converts a JSON scalar the way Python's float() does.
func asFloat(value any) (float64, error) {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, fmt.Errorf("invalid number %q", string(v))
		}
		return f, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid float %q", v)
		}
		return f, nil
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case float64:
		return v, nil
	case int64:
		return float64(v), nil
	case int:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("expected number, got %T", value)
	}
}

// getBlocks mirrors `blocks = d.get("content") or []` then
// `for block in blocks: if not isinstance(block, dict): continue`.
func getBlocks(doc map[string]any) ([]map[string]any, error) {
	value, ok := doc["content"]
	if !ok || isFalsy(value) {
		return nil, nil
	}
	switch v := value.(type) {
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				out = append(out, block)
			}
		}
		return out, nil
	case map[string]any:
		// Iterating a dict yields keys (strings), none of which is a dict.
		return nil, nil
	default:
		return nil, fmt.Errorf("expected array, got %T", value)
	}
}
