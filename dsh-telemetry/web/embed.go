// SPDX-License-Identifier: AGPL-3.0-only

package web

import _ "embed"

// IndexHTML is the single-page DSH telemetry report shell.
//
//go:embed index.html
var IndexHTML []byte
