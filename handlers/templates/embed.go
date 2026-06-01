// Package templates embeds the HTML templates rendered by the handlers package.
package templates

import "embed"

// FS holds the embedded HTML templates served by the handlers package.
//
//go:embed *.html
var FS embed.FS
