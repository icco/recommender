package recommend

import (
	"context"
	"fmt"
	"os"

	"github.com/icco/gutil/vertex"
	"google.golang.org/genai"
)

// Chatter is the minimal LLM surface the recommender needs: given a system and
// user prompt plus a JSON response schema, return the model's JSON text.
// Implemented by GeminiChatter; faked in tests.
type Chatter interface {
	Complete(ctx context.Context, system, user string, schema *genai.Schema) (string, error)
}

// GeminiChatter calls Gemini on Vertex AI via github.com/icco/gutil/vertex.
type GeminiChatter struct {
	v *vertex.Client
}

// NewGeminiChatter builds a Vertex AI-backed client from ADC. Project and
// location come from GOOGLE_CLOUD_PROJECT / GOOGLE_CLOUD_LOCATION.
func NewGeminiChatter(ctx context.Context, model string) (*GeminiChatter, error) {
	v, err := vertex.New(ctx, vertex.Config{
		Project:  os.Getenv("GOOGLE_CLOUD_PROJECT"),
		Location: os.Getenv("GOOGLE_CLOUD_LOCATION"),
		Model:    model,
	})
	if err != nil {
		return nil, fmt.Errorf("create vertex client: %w", err)
	}
	return &GeminiChatter{v: v}, nil
}

// Complete sends the prompts with JSON-constrained output and returns the raw
// JSON text.
//
// An empty answer comes back as an error wrapping vertex.ErrEmptyResponse
// rather than an empty string. Callers unmarshal this into a slot assignment,
// so "" would surface as a confusing parse failure instead of the safety block
// or truncated generation it actually is.
func (g *GeminiChatter) Complete(ctx context.Context, system, user string, schema *genai.Schema) (string, error) {
	resp, err := g.v.Generate(ctx, vertex.Request{
		System: system,
		Parts:  vertex.Text(user),
		Schema: schema,
	})
	if err != nil {
		return "", fmt.Errorf("gemini generate: %w", err)
	}
	return resp.Text, nil
}
