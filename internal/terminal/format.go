// SPDX-License-Identifier: AGPL-3.0-only

// Package terminal formats the human summary. It follows the repository's
// terminal guidance: header-first, computed alignment, explicit units, and a
// maximum table width under 98 columns.
package terminal

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/jadmadi/agentmeter/internal/render"
)

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
