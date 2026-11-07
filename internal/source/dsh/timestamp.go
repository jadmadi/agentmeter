// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// timeLayouts is the ISO-8601 fallback chain for string timestamps.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999Z0700",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseTimestamp converts a millisecond-epoch number, second-epoch number, or
// ISO-8601 string into a UTC instant. Non-positive or unparseable values
// return nil.
func parseTimestamp(value any) *time.Time {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed <= 0 {
			return nil
		}
		return fromEpochFloat(typed)
	case int64:
		if typed <= 0 {
			return nil
		}
		return fromEpochFloat(float64(typed))
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil
		}
		if num, err := strconv.ParseFloat(text, 64); err == nil && !math.IsNaN(num) && !math.IsInf(num, 0) && num > 0 {
			return fromEpochFloat(num)
		}
		for _, layout := range timeLayouts {
			if parsed, err := time.ParseInLocation(layout, text, time.Local); err == nil {
				rounded := time.UnixMicro(parsed.UnixMicro()).In(parsed.Location())
				return &rounded
			}
		}
		return nil
	default:
		return nil
	}
}

func fromEpochFloat(raw float64) *time.Time {
	// DSH records millisecond epoch timestamps (e.g. 1762000000000).
	var micros int64
	if raw > 1e11 {
		micros = int64(math.Round(raw * 1000))
	} else {
		micros = int64(math.Round(raw * 1e6))
	}
	if micros <= 0 {
		return nil
	}
	ts := time.UnixMicro(micros).UTC()
	return &ts
}

// LocalDate returns YYYY-MM-DD in local time.
func LocalDate(ts time.Time) string {
	return ts.In(time.Local).Format("2006-01-02")
}

// LocalHour returns the hour (0..23) in local time.
func LocalHour(ts time.Time) int {
	return ts.In(time.Local).Hour()
}

// UnixFloat reproduces datetime.timestamp(): integer microseconds divided by
// 1e6.
func UnixFloat(ts time.Time) float64 {
	micros := ts.Unix()*1_000_000 + int64(ts.Nanosecond())/1_000
	return float64(micros) / 1e6
}

// UnixSeconds returns integer epoch seconds.
func UnixSeconds(ts time.Time) int64 {
	return ts.Unix()
}

// DeltaSeconds reproduces timedelta.total_seconds().
func DeltaSeconds(delta time.Duration) float64 {
	return float64(delta.Microseconds()) / 1e6
}

// LocalTZLabel formats the current local UTC offset as UTC±HH:MM.
func LocalTZLabel() string {
	_, offsetSeconds := time.Now().Zone()
	minutes := int(math.Floor(float64(offsetSeconds) / 60.0))
	sign := "+"
	if minutes < 0 {
		sign = "-"
		minutes = -minutes
	}
	hours := minutes / 60
	rest := minutes % 60
	return "UTC" + sign + twoDigits(hours) + ":" + twoDigits(rest)
}

func twoDigits(value int) string {
	if value < 10 {
		return "0" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}

// FormatMinute reproduces `%Y-%m-%d %H:%M` in local time.
func FormatMinute(ts time.Time) string {
	return ts.In(time.Local).Format("2006-01-02 15:04")
}

// expandHome replaces a leading "~" with the home directory.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}
