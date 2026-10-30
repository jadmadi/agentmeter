// SPDX-License-Identifier: AGPL-3.0-only
package compareweb

import _ "embed"

// IndexHTML is the cross-tool comparison shell. It reads comparison-data.js
// (written next to it by emit.WriteReportFiles) from the COMPARISON_TELEMETRY
// global.
//
//go:embed index.html
var IndexHTML []byte
