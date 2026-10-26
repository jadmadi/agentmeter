// SPDX-License-Identifier: AGPL-3.0-only

package emit

import (
	"strings"
	"testing"
)

func TestWatchReportsMissingReport(t *testing.T) {
	err := Watch(t.TempDir(), 0, false, 0, func() string { return "" }, func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "report not found") {
		t.Fatalf("Watch error = %v, want missing report", err)
	}
}
