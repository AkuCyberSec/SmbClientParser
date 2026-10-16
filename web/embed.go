// Package web holds the browser files of the web mode (HTML, CSS, JS).
// They are embedded in the binary, so the tool works offline and ships as a single file.
package web

import "embed"

//go:embed index.html login.html css js
var Files embed.FS
