// SPDX-License-Identifier: AGPL-3.0-only

// Package datewindow validates inclusive local date windows.
package datewindow

import (
	"fmt"
	"time"
)

const layout = "2006-01-02"

// Validate checks that since and until are empty or strict YYYY-MM-DD dates
// and that the window is not inverted.
func Validate(since, until string) error {
	sinceTime, err := parse("--since", since)
	if err != nil {
		return err
	}
	untilTime, err := parse("--until", until)
	if err != nil {
		return err
	}
	if !sinceTime.IsZero() && !untilTime.IsZero() && sinceTime.After(untilTime) {
		return fmt.Errorf("--since %s is after --until %s", since, until)
	}
	return nil
}

func parse(flag, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(layout, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s %q: want YYYY-MM-DD", flag, value)
	}
	return parsed, nil
}
