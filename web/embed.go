// Package web embeds the browser UI.
package web

import "embed"

// FS holds the static UI files under static/.
//
//go:embed static
var FS embed.FS
