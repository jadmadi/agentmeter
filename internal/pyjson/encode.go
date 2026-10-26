// SPDX-License-Identifier: AGPL-3.0-only

package pyjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// WriteJSON writes data exactly like
// json.dumps(data, indent=1, ensure_ascii=False): one-space indentation, ","
// and ": " separators, raw non-ASCII, no HTML escaping, and no trailing
// newline.
//
// Object keys follow encoding/json's sorted order rather than Python's
// insertion order; the Python readers parse the document, so the ordering is
// semantically irrelevant.
func WriteJSON(w io.Writer, data any) error {
	payload, err := marshalReport(data, true)
	if err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

// WriteJS writes the browser payload for a report:
//
//	window.<global> = <compact json>;
//
// The payload uses compact separators (",", ":") and no HTML escaping, exactly
// like json.dumps(data, ensure_ascii=False, separators=(",", ":")) in the
// Python writers. The trailing newline is part of the format.
func WriteJS(w io.Writer, global string, data any) error {
	if err := validateGlobal(global); err != nil {
		return err
	}
	payload, err := marshalReport(data, false)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, "window."+global+" = "); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	_, err = io.WriteString(w, ";\n")
	return err
}

// JSBytes is WriteJS into a fresh buffer.
func JSBytes(global string, data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteJS(&buf, global, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// JSONBytes is WriteJSON into a fresh buffer.
func JSONBytes(data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// marshalReport encodes data the way the Python writers do, minus the trailing
// newline json.Encoder always appends.
func marshalReport(data any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if indent {
		encoder.SetIndent("", " ")
	}
	if err := encoder.Encode(data); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func validateGlobal(global string) error {
	if global == "" {
		return fmt.Errorf("pyjson: empty JS global name")
	}
	if strings.HasPrefix(global, "window.") {
		return fmt.Errorf("pyjson: JS global %q must not include the window. prefix", global)
	}
	for _, r := range global {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '$', r == '.':
		default:
			return fmt.Errorf("pyjson: JS global %q contains invalid character %q", global, r)
		}
	}
	return nil
}
