// Package prompts embeds the Gemini prompt templates used by the recommend
// package for generating movie and TV show recommendations.
package prompts

import "embed"

// FS holds the embedded Gemini prompt templates used by the recommend package.
//
//go:embed *.txt
var FS embed.FS
