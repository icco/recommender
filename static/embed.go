// Package static provides the embedded static assets (favicon, etc.) that
// the recommender service serves under /static/.
package static

import "embed"

// Files holds embedded static assets served under /static/.
//
//go:embed favicon.svg
var Files embed.FS
