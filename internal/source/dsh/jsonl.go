// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/jadmadi/agentmeter/internal/zstd"
)

// readJSONL reads a plain .jsonl or Zstandard-compressed .jsonl.zstd file and
// returns every JSON object line in order. Unreadable files, corrupt zstd
// streams, malformed lines, non-object JSON values, and lines containing
// NaN/Infinity tokens are skipped without aborting the session load.
func readJSONL(path string) []map[string]any {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	var stream io.Reader = file
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".zstd") || strings.HasSuffix(lower, ".zst") {
		stream = zstd.NewReader(file)
	}

	var records []map[string]any
	reader := bufio.NewReaderSize(stream, 64*1024)
	for {
		line, readErr := readLine(reader)
		if len(line) > 0 {
			if obj := parseJSONObjectLine(line); obj != nil {
				records = append(records, obj)
			}
		}
		if readErr != nil {
			break
		}
	}
	return records
}

func readLine(reader *bufio.Reader) ([]byte, error) {
	var full []byte
	for {
		fragment, isPrefix, err := reader.ReadLine()
		if len(fragment) > 0 {
			if !isPrefix && len(full) == 0 {
				return fragment, err
			}
			full = append(full, fragment...)
		}
		if !isPrefix || err != nil {
			return full, err
		}
	}
}

func parseJSONObjectLine(line []byte) map[string]any {
	trimmed := bytes.TrimFunc(line, unicode.IsSpace)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil
	}
	return object
}
