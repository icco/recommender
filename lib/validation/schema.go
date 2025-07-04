package validation

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RecommendationResponse represents the expected structure from OpenAI
type RecommendationResponse struct {
	Movies  []MovieRecommendation  `json:"movies"`
	TVShows []TVShowRecommendation `json:"tvshows"`
}

// MovieRecommendation represents a movie recommendation from OpenAI
type MovieRecommendation struct {
	Title       string `json:"title"`
	TMDbID      int    `json:"tmdb_id"`
	Explanation string `json:"explanation"`
}

// TVShowRecommendation represents a TV show recommendation from OpenAI
type TVShowRecommendation struct {
	Title       string `json:"title"`
	TMDbID      int    `json:"tmdb_id"`
	Explanation string `json:"explanation"`
}

// ValidateAndParseRecommendationResponse validates and parses the OpenAI response
func ValidateAndParseRecommendationResponse(responseBody string) (*RecommendationResponse, error) {
	// First, parse the JSON
	var response RecommendationResponse
	if err := json.Unmarshal([]byte(responseBody), &response); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}
	
	// Validate the response structure
	if err := validateRecommendationResponse(&response); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}
	
	// Sanitize the data
	sanitizeRecommendationResponse(&response)
	
	return &response, nil
}

// validateRecommendationResponse performs validation on the parsed response
func validateRecommendationResponse(response *RecommendationResponse) error {
	// Check that we have at least some recommendations
	if len(response.Movies) == 0 && len(response.TVShows) == 0 {
		return fmt.Errorf("no recommendations found in response")
	}
	
	// Validate movies
	validMovies := 0
	for i, movie := range response.Movies {
		if err := validateMovieRecommendation(movie); err != nil {
			return fmt.Errorf("invalid movie at index %d: %w", i, err)
		}
		validMovies++
	}
	
	// Validate TV shows
	validTVShows := 0
	for i, tvshow := range response.TVShows {
		if err := validateTVShowRecommendation(tvshow); err != nil {
			return fmt.Errorf("invalid tvshow at index %d: %w", i, err)
		}
		validTVShows++
	}
	
	// Check reasonable limits to prevent abuse
	if validMovies > 20 {
		return fmt.Errorf("too many movies in response: %d (max 20)", validMovies)
	}
	if validTVShows > 20 {
		return fmt.Errorf("too many TV shows in response: %d (max 20)", validTVShows)
	}
	
	return nil
}

// validateMovieRecommendation validates a single movie recommendation
func validateMovieRecommendation(movie MovieRecommendation) error {
	if strings.TrimSpace(movie.Title) == "" {
		return fmt.Errorf("movie title is required")
	}
	
	if movie.TMDbID <= 0 {
		return fmt.Errorf("movie tmdb_id must be positive")
	}
	
	if strings.TrimSpace(movie.Explanation) == "" {
		return fmt.Errorf("movie explanation is required")
	}
	
	// Check reasonable limits
	if len(movie.Title) > 500 {
		return fmt.Errorf("movie title too long: %d chars (max 500)", len(movie.Title))
	}
	
	if len(movie.Explanation) > 2000 {
		return fmt.Errorf("movie explanation too long: %d chars (max 2000)", len(movie.Explanation))
	}
	
	return nil
}

// validateTVShowRecommendation validates a single TV show recommendation
func validateTVShowRecommendation(tvshow TVShowRecommendation) error {
	if strings.TrimSpace(tvshow.Title) == "" {
		return fmt.Errorf("tvshow title is required")
	}
	
	if tvshow.TMDbID <= 0 {
		return fmt.Errorf("tvshow tmdb_id must be positive")
	}
	
	if strings.TrimSpace(tvshow.Explanation) == "" {
		return fmt.Errorf("tvshow explanation is required")
	}
	
	// Check reasonable limits
	if len(tvshow.Title) > 500 {
		return fmt.Errorf("tvshow title too long: %d chars (max 500)", len(tvshow.Title))
	}
	
	if len(tvshow.Explanation) > 2000 {
		return fmt.Errorf("tvshow explanation too long: %d chars (max 2000)", len(tvshow.Explanation))
	}
	
	return nil
}

// sanitizeRecommendationResponse cleans up the data and removes any invalid entries
func sanitizeRecommendationResponse(response *RecommendationResponse) {
	// Sanitize movies
	validMovies := make([]MovieRecommendation, 0, len(response.Movies))
	for _, movie := range response.Movies {
		sanitized := sanitizeMovieRecommendation(movie)
		if sanitized.Title != "" && sanitized.TMDbID > 0 {
			validMovies = append(validMovies, sanitized)
		}
	}
	response.Movies = validMovies
	
	// Sanitize TV shows
	validTVShows := make([]TVShowRecommendation, 0, len(response.TVShows))
	for _, tvshow := range response.TVShows {
		sanitized := sanitizeTVShowRecommendation(tvshow)
		if sanitized.Title != "" && sanitized.TMDbID > 0 {
			validTVShows = append(validTVShows, sanitized)
		}
	}
	response.TVShows = validTVShows
}

// sanitizeMovieRecommendation cleans up a movie recommendation
func sanitizeMovieRecommendation(movie MovieRecommendation) MovieRecommendation {
	return MovieRecommendation{
		Title:       sanitizeString(movie.Title, 500),
		TMDbID:      movie.TMDbID,
		Explanation: sanitizeString(movie.Explanation, 2000),
	}
}

// sanitizeTVShowRecommendation cleans up a TV show recommendation
func sanitizeTVShowRecommendation(tvshow TVShowRecommendation) TVShowRecommendation {
	return TVShowRecommendation{
		Title:       sanitizeString(tvshow.Title, 500),
		TMDbID:      tvshow.TMDbID,
		Explanation: sanitizeString(tvshow.Explanation, 2000),
	}
}

// sanitizeString trims whitespace and enforces length limits
func sanitizeString(s string, maxLength int) string {
	// Trim whitespace
	s = strings.TrimSpace(s)
	
	// Enforce length limit
	if len(s) > maxLength {
		s = s[:maxLength]
	}
	
	// Remove any control characters
	s = strings.Map(func(r rune) rune {
		if r < 32 && r != 9 && r != 10 && r != 13 { // Allow tab, newline, carriage return
			return -1
		}
		return r
	}, s)
	
	return s
}