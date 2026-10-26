// SPDX-License-Identifier: AGPL-3.0-only

package pyjson

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestPyFloatFuzzAgainstPython(t *testing.T) {
	path := os.Getenv("AGENTMETER_FLOAT_VECTORS")
	if path == "" {
		t.Skip("AGENTMETER_FLOAT_VECTORS not set")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	count := 0
	for scanner.Scan() {
		var record struct {
			Value float64 `json:"value"`
			Repr  string  `json:"repr"`
		}
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		if err := decoder.Decode(&record); err != nil {
			t.Fatalf("decode line: %v", err)
		}
		if got := PyFloat(record.Value); got != record.Repr {
			t.Fatalf("PyFloat(%v) = %q, Python repr = %q", record.Value, got, record.Repr)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	t.Logf("checked %d floats", count)
}
