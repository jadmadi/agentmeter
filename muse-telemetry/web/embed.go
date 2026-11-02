// SPDX-License-Identifier: AGPL-3.0-only
package museweb

import _ "embed"

// IndexHTML is the shared Muse report shell.
//
//go:embed index.html
var IndexHTML []byte
