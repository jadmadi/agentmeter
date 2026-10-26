// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}
