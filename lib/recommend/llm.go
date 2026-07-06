package recommend

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/genai"
)

// Chatter is the minimal LLM surface the recommender needs: given a system and
// user prompt plus a JSON response schema, return the model's JSON text.
// Implemented by GeminiChatter; faked in tests.
type Chatter interface {
	Complete(ctx context.Context, system, user string, schema *genai.Schema) (string, error)
}

// GeminiChatter calls Gemini on Vertex AI via the unified google.golang.org/genai SDK.
type GeminiChatter struct {
	client *genai.Client
	model  string
}

// NewGeminiChatter builds a Vertex AI-backed client from ADC. Project and
// location come from GOOGLE_CLOUD_PROJECT / GOOGLE_CLOUD_LOCATION.
func NewGeminiChatter(ctx context.Context, model string) (*GeminiChatter, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  os.Getenv("GOOGLE_CLOUD_PROJECT"),
		Location: os.Getenv("GOOGLE_CLOUD_LOCATION"),
	})
	if err != nil {
		return nil, fmt.Errorf("create genai client: %w", err)
	}
	return &GeminiChatter{client: client, model: model}, nil
}

// Complete sends the prompts with JSON-constrained output and returns the raw JSON text.
func (g *GeminiChatter) Complete(ctx context.Context, system, user string, schema *genai.Schema) (string, error) {
	cfg := &genai.GenerateContentConfig{
		ResponseMIMEType:  "application/json",
		ResponseSchema:    schema,
		SystemInstruction: genai.NewContentFromText(system, genai.RoleUser),
	}
	resp, err := g.client.Models.GenerateContent(ctx, g.model, genai.Text(user), cfg)
	if err != nil {
		return "", fmt.Errorf("gemini generate: %w", err)
	}
	return resp.Text(), nil
}
