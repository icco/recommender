// Package prompts embeds the OpenAI prompt templates used by the recommend
// package for generating movie and TV show recommendations.
package prompts

import "embed"

// FS holds the embedded OpenAI prompt templates used by the recommend package.
//
//go:embed *.txt
var FS embed.FS
