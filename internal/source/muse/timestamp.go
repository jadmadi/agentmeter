// SPDX-License-Identifier: AGPL-3.0-only

package muse

import (
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// timeLayouts is the timestamp fallback chain. Python's
// datetime.fromisoformat (after replacing "Z" with "+00:00") accepts RFC 3339
// with "Z" or numeric offsets, fractional seconds of any length, a space
// instead of "T", and omitted seconds. Naive values are interpreted in the
// local zone, exactly like a naive datetime followed by astimezone().
var timeLayouts = []string{
	time.RFC3339Nano,                     // 2006-01-02T15:04:05.999999999Z07:00
	"2006-01-02T15:04:05.999999999Z0700", // +0300 without a colon
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999Z0700",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseTimestamp reproduces parse_ts(): nil for empty or unparseable values.
func parseTimestamp(value any) *time.Time {
	text := asString(value)
	if text == "" {
		return nil
	}
	// datetime.fromisoformat is case-sensitive for "T"/"Z" but the data is
	// normalized; only "Z" needs translating because Go already accepts it.
	for _, layout := range timeLayouts {
		if parsed, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			// Python's fromisoformat keeps microseconds only; truncate so
			// nanosecond inputs cannot change durations.
			truncated := parsed.Truncate(time.Microsecond)
			return &truncated
		}
	}
	return nil
}

// LocalDate is `ts.astimezone().date().isoformat()`.
// fromMicros converts a microsecond-epoch timestamp (the recorded_at unit)
// into a UTC instant. Non-positive or non-numeric values return nil.
func fromMicros(value any) *time.Time {
	var micros int64
	switch typed := value.(type) {
	case float64:
		micros = int64(typed)
	case int64:
		micros = typed
	default:
		return nil
	}
	if micros <= 0 {
		return nil
	}
	ts := time.UnixMicro(micros).UTC()
	return &ts
}

func LocalDate(ts time.Time) string {
	return ts.In(time.Local).Format("2006-01-02")
}

// LocalHour is `ts.astimezone().hour`.
func LocalHour(ts time.Time) int {
	return ts.In(time.Local).Hour()
}

// UnixFloat reproduces datetime.timestamp(): integer microseconds divided by
// 1e6, so the double is rounded exactly like CPython.
func UnixFloat(ts time.Time) float64 {
	micros := ts.Unix()*1_000_000 + int64(ts.Nanosecond())/1_000
	return float64(micros) / 1e6
}

// UnixSeconds is `int(ts.timestamp())` for post-epoch timestamps.
func UnixSeconds(ts time.Time) int64 {
	return ts.Unix()
}

// DeltaSeconds reproduces timedelta.total_seconds(): exact integer
// microseconds divided by 1e6, which is a single correctly-rounded division.
// Go's Duration.Seconds() adds the second and nanosecond parts as two floats,
// which differs by one ulp on ties (12 + 0.351 vs 12.351).
func DeltaSeconds(delta time.Duration) float64 {
	return float64(delta.Microseconds()) / 1e6
}

// LocalTZLabel reproduces local_tz_label(): the *current* local UTC offset,
// formatted UTC±HH:MM. Python floors the offset minutes; negatives keep the
// sign on the hours component.
func LocalTZLabel() string {
	_, offsetSeconds := time.Now().Zone()
	minutes := int(math.Floor(float64(offsetSeconds) / 60.0))
	sign := "+"
	if minutes < 0 {
		sign = "-"
	}
	if minutes < 0 {
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

// expandHome replaces a leading "~" with the home directory, like
// os.path.expanduser for the "~" and "~/..." forms.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}
