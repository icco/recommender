package validation

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

// RecommendationSchema defines the JSON schema for OpenAI recommendation responses
var RecommendationSchema = `{
	"type": "object",
	"properties": {
		"movies": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"title": {"type": "string", "minLength": 1},
					"type": {"type": "string", "enum": ["movie"]},
					"tmdb_id": {"type": "integer", "minimum": 0},
					"explanation": {"type": "string", "minLength": 1}
				},
				"required": ["title", "tmdb_id", "explanation"],
				"additionalProperties": false
			},
			"minItems": 0,
			"maxItems": 10
		},
		"tvshows": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"title": {"type": "string", "minLength": 1},
					"type": {"type": "string", "enum": ["tvshow"]},
					"tmdb_id": {"type": "integer", "minimum": 0},
					"explanation": {"type": "string", "minLength": 1}
				},
				"required": ["title", "tmdb_id", "explanation"],
				"additionalProperties": false
			},
			"minItems": 0,
			"maxItems": 10
		}
	},
	"required": ["movies", "tvshows"],
	"additionalProperties": false
}`

// ValidateRecommendationResponse validates a JSON response against the recommendation schema
func ValidateRecommendationResponse(jsonData []byte) error {
	schemaLoader := gojsonschema.NewStringLoader(RecommendationSchema)
	documentLoader := gojsonschema.NewBytesLoader(jsonData)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return fmt.Errorf("failed to validate JSON schema: %w", err)
	}

	if !result.Valid() {
		var errorMessages []string
		for _, desc := range result.Errors() {
			errorMessages = append(errorMessages, desc.String())
		}
		return fmt.Errorf("JSON validation failed: %s", strings.Join(errorMessages, "; "))
	}

	return nil
}

// ValidateAndParseRecommendationResponse validates and parses a JSON response
func ValidateAndParseRecommendationResponse(jsonData []byte) (*RecommendationResponse, error) {
	// First validate the schema
	if err := ValidateRecommendationResponse(jsonData); err != nil {
		return nil, err
	}

	// Then parse the JSON
	var response RecommendationResponse
	if err := json.Unmarshal(jsonData, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return &response, nil
}

// RecommendationItem represents a single recommendation item
type RecommendationItem struct {
	Title       string `json:"title"`
	Type        string `json:"type,omitempty"`
	TMDbID      int    `json:"tmdb_id"`
	Explanation string `json:"explanation"`
}

// RecommendationResponse represents the complete response from OpenAI
type RecommendationResponse struct {
	Movies  []RecommendationItem `json:"movies"`
	TVShows []RecommendationItem `json:"tvshows"`
}

// SanitizeRecommendationResponse removes potentially harmful or invalid data
func SanitizeRecommendationResponse(response *RecommendationResponse) {
	// Remove any items with empty titles
	var cleanMovies []RecommendationItem
	for _, movie := range response.Movies {
		if strings.TrimSpace(movie.Title) != "" {
			movie.Title = strings.TrimSpace(movie.Title)
			movie.Explanation = strings.TrimSpace(movie.Explanation)
			cleanMovies = append(cleanMovies, movie)
		}
	}
	response.Movies = cleanMovies

	var cleanTVShows []RecommendationItem
	for _, show := range response.TVShows {
		if strings.TrimSpace(show.Title) != "" {
			show.Title = strings.TrimSpace(show.Title)
			show.Explanation = strings.TrimSpace(show.Explanation)
			cleanTVShows = append(cleanTVShows, show)
		}
	}
	response.TVShows = cleanTVShows
}