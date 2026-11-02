// SPDX-License-Identifier: AGPL-3.0-only

// Package terminal formats the human summary. It follows the repository's
// terminal guidance: header-first, computed alignment, explicit units, and a
// maximum table width under 98 columns.
package terminal

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jadmadi/agentmeter/internal/render"
)

// Width returns the terminal width from COLUMNS, defaulting to 80.
func Width() int {
	if value, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && value > 0 {
		return value
	}
	return 80
}

// LineWriter wraps complete lines at width. It breaks on spaces and leaves
// unbreakable tokens intact.
type LineWriter struct {
	w     io.Writer
	width int
	buf   strings.Builder
}

// NewLineWriter returns a writer that wraps lines at width.
func NewLineWriter(w io.Writer, width int) *LineWriter {
	return &LineWriter{w: w, width: width}
}

// Unwrap exposes the underlying writer so colour detection still sees a TTY.
func (l *LineWriter) Unwrap() io.Writer { return l.w }

// Write buffers bytes and emits each complete line.
func (l *LineWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			l.emit(l.buf.String())
			l.buf.Reset()
			continue
		}
		l.buf.WriteByte(b)
	}
	return len(p), nil
}

// Close flushes any unterminated line.
func (l *LineWriter) Close() error {
	if l.buf.Len() > 0 {
		l.emit(l.buf.String())
		l.buf.Reset()
	}
	return nil
}

func (l *LineWriter) emit(line string) {
	for _, part := range wrapLine(line, l.width) {
		fmt.Fprintln(l.w, part)
	}
}

func wrapLine(line string, width int) []string {
	if width <= 0 || visibleLen(line) <= width {
		return []string{line}
	}
	parts := make([]string, 0, 2)
	start := 0
	for start < len(line) {
		visible := 0
		lastSpace := -1
		index := start
		for index < len(line) {
			if line[index] == 0x1b {
				index = escapeEnd(line, index)
				continue
			}
			r, size := utf8.DecodeRuneInString(line[index:])
			if r == ' ' && visible > 0 {
				lastSpace = index
			}
			if visible == width {
				break
			}
			visible++
			index += size
		}
		if index >= len(line) {
			parts = append(parts, line[start:])
			break
		}
		cut := index
		if lastSpace > start {
			cut = lastSpace
		}
		if part := strings.TrimRight(line[start:cut], " \t"); part != "" {
			parts = append(parts, part)
		}
		start = cut
		for start < len(line) && line[start] == ' ' {
			start++
		}
	}
	return parts
}

// visibleLen counts runes outside ANSI CSI escape sequences.
func visibleLen(line string) int {
	count := 0
	for index := 0; index < len(line); {
		if line[index] == 0x1b {
			index = escapeEnd(line, index)
			continue
		}
		_, size := utf8.DecodeRuneInString(line[index:])
		count++
		index += size
	}
	return count
}

// escapeEnd returns the index just past a CSI escape sequence. A bare ESC
// advances one byte.
func escapeEnd(line string, index int) int {
	next := index + 1
	if next >= len(line) || line[next] != '[' {
		return next
	}
	next++
	for next < len(line) {
		c := line[next]
		next++
		if c >= 0x40 && c <= 0x7e {
			break
		}
	}
	return next
}

// Title prints the report title.
func Title(w io.Writer, text string) {
	fmt.Fprintln(w, render.Paint(w, render.Bold, text))
}

// Line prints a labelled summary line. Labels are left-aligned in 7 columns.
func Line(w io.Writer, label, value string) {
	label = fmt.Sprintf("%-7s", label)
	fmt.Fprintf(w, "%s %s\n", render.Paint(w, render.Cyan, label), value)
}

// Section prints a blank line and an uppercase section heading.
func Section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n%s\n", render.Paint(w, render.Bold, title))
}

// Note prints an indented note under a table.
func Note(w io.Writer, text string) {
	fmt.Fprintf(w, "  %s\n", render.Paint(w, render.Dim, text))
}

// Count formats an integer with thousands separators.
func Count(value int64) string {
	raw := strconv.FormatInt(value, 10)
	negative := strings.HasPrefix(raw, "-")
	if negative {
		raw = raw[1:]
	}
	var b strings.Builder
	for i, digit := range raw {
		if i > 0 && (len(raw)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(digit)
	}
	if negative {
		return "-" + b.String()
	}
	return b.String()
}

// Compact formats a token count as 1.23K / 4.56M / 7.89B.
func Compact(value float64) string {
	absolute := math.Abs(value)
	switch {
	case absolute >= 1e9:
		return fmt.Sprintf("%.2fB", value/1e9)
	case absolute >= 1e6:
		return fmt.Sprintf("%.2fM", value/1e6)
	case absolute >= 1e3:
		return fmt.Sprintf("%.1fK", value/1e3)
	default:
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
}

// Fit truncates s to width runes and appends "..." when needed.
func Fit(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

// Table prints a header and rows with fixed column widths. right marks
// numeric columns. The widths are chosen by callers to stay under 98 columns.
func Table(w io.Writer, headers []string, rows [][]string, right []bool, widths []int) {
	colored := render.Enabled(w)
	if colored {
		fmt.Fprint(w, render.Bold)
	}
	printRow(w, headers, right, widths)
	if colored {
		fmt.Fprint(w, render.Reset)
	}
	total := 0
	for _, width := range widths {
		total += width
	}
	total += 2 * (len(widths) - 1)
	fmt.Fprintln(w, strings.Repeat("-", total))
	for _, row := range rows {
		printRow(w, row, right, widths)
	}
}

func printRow(w io.Writer, fields []string, right []bool, widths []int) {
	for i, width := range widths {
		value := ""
		if i < len(fields) {
			value = fields[i]
		}
		value = Fit(value, width)
		if i == 0 {
			fmt.Fprintf(w, "%-*s", width, value)
			continue
		}
		if right[i] {
			fmt.Fprintf(w, "  %*s", width, value)
		} else {
			fmt.Fprintf(w, "  %-*s", width, value)
		}
	}
	fmt.Fprintln(w)
}
