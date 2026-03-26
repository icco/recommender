package static

import "embed"

// Files holds embedded static assets served under /static/.
//
//go:embed favicon.svg
var Files embed.FS
