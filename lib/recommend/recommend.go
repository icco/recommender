package recommend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/icco/gutil/logging"
	"github.com/icco/recommender/lib/plex"
	"github.com/icco/recommender/lib/recommend/prompts"
	"github.com/icco/recommender/lib/tmdb"
	"github.com/icco/recommender/models"
	openai "github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// StatsData represents statistics about the recommendations database.
type StatsData struct {
	TotalRecommendations        int64
	TotalMovies                 int64
	TotalTVShows                int64
	FirstDate                   time.Time
	LastDate                    time.Time
	AverageDailyRecommendations float64
	GenreDistribution           []struct {
		Genre string
		Count int64
	}
	TotalCachedMovies  int64
	TotalCachedTVShows int64
	LastCacheUpdate    time.Time
}

// Recommender produces and serves daily Plex/TMDb recommendations using
// OpenAI. Loggers are taken from per-call ctx via gutil/logging.
type Recommender struct {
	db     *gorm.DB
	plex   *plex.Client
	tmdb   *tmdb.Client
	openai *openai.Client

	cacheMu sync.Mutex
	cache   map[string]*CacheEntry
}

// CacheEntry represents a cached recommendation with metadata
type CacheEntry struct {
	Recommendation *models.Recommendation
	Timestamp      time.Time
	TTL            time.Duration
}

// RecommendationContext contains the context used for generating recommendations.
// It includes the available content, user preferences, and previous recommendations.
type RecommendationContext struct {
	Content                 string
	Preferences             string
	PreviousRecommendations string
}

// UnwatchedContent represents the unwatched content available for recommendations,
// organized by content type (movies and TV shows).
type UnwatchedContent struct {
	Movies  []models.Recommendation
	TVShows []models.Recommendation
}

// New creates a new Recommender instance with the provided dependencies.
// It initializes the OpenAI client with proper timeout and retry configuration.
// Loggers are sourced from per-call ctx via gutil/logging.
func New(db *gorm.DB, plex *plex.Client, tmdb *tmdb.Client) (*Recommender, error) {
	httpClient := &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
		},
	}

	config := openai.DefaultConfig(os.Getenv("OPENAI_API_KEY"))
	config.HTTPClient = httpClient

	openaiClient := openai.NewClientWithConfig(config)

	r := &Recommender{
		db:     db,
		plex:   plex,
		tmdb:   tmdb,
		openai: openaiClient,
		cache:  make(map[string]*CacheEntry),
	}

	// Cleanup runs in the background; we want it to inherit a logger we control,
	// so callers should provide ctx via StartCacheCleanup if structured background logging is needed.
	go r.startCacheCleanup(context.Background())

	return r, nil
}

// logTMDbErr demotes the breaker-open case to warn. When TMDb is unhealthy
// every title in the batch hits the same ErrCircuitOpen, so logging each at
// error level floods the logs with the same not-really-actionable message.
func logTMDbErr(l *zap.SugaredLogger, msg, title string, err error) {
	if errors.Is(err, tmdb.ErrCircuitOpen) {
		l.Warnw(msg, "title", title, "reason", "tmdb_circuit_open")
		return
	}
	l.Errorw(msg, "title", title, zap.Error(err))
}

// recommendationUTCDayRange returns [start, end) for the calendar day of t in UTC.
// Cron and HandleHome use UTC midnight for "today"; rows store that same instant in `date`.
func recommendationUTCDayRange(t time.Time) (start, end time.Time) {
	t = t.In(time.UTC)
	start = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	end = start.Add(24 * time.Hour)
	return start, end
}

// GetRecommendationsForDate retrieves all recommendations for a specific date
func (r *Recommender) GetRecommendationsForDate(ctx context.Context, date time.Time) ([]models.Recommendation, error) {
	var recommendations []models.Recommendation
	start, end := recommendationUTCDayRange(date)
	// Half-open range matches how GORM persists time.Time and avoids SQLite date() quirks
	// on a column named `date`.
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).
		Where(`"date" >= ? AND "date" < ?`, start, end).
		Find(&recommendations).Error; err != nil {
		return nil, fmt.Errorf("failed to get recommendations: %w", err)
	}
	return recommendations, nil
}

// GetRecommendationDates retrieves a paginated list of distinct calendar dates that have recommendations.
func (r *Recommender) GetRecommendationDates(ctx context.Context, page, pageSize int) ([]time.Time, int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Raw(`
		SELECT COUNT(*) FROM (
			SELECT 1 FROM recommendations
			GROUP BY strftime('%Y-%m-%d', "date")
		)`).Scan(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to get total distinct dates: %w", err)
	}

	offset := (page - 1) * pageSize
	var dateRows []struct {
		D string `gorm:"column:d"`
	}
	if err := r.db.WithContext(ctx).Raw(`
		SELECT strftime('%Y-%m-%d', "date") AS d FROM recommendations
		GROUP BY strftime('%Y-%m-%d', "date")
		ORDER BY d DESC
		LIMIT ? OFFSET ?`, pageSize, offset).Scan(&dateRows).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to get dates: %w", err)
	}

	dateStrs := make([]string, len(dateRows))
	for i, row := range dateRows {
		dateStrs[i] = row.D
	}

	dates := make([]time.Time, 0, len(dateStrs))
	for _, s := range dateStrs {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return nil, 0, fmt.Errorf("parse date %q: %w", s, err)
		}
		dates = append(dates, t.UTC())
	}

	return dates, total, nil
}

// CheckRecommendationsComplete checks if recommendations are complete for a specific date
// Returns true if we have at least some recommendations (flexible approach)
func (r *Recommender) CheckRecommendationsComplete(ctx context.Context, date time.Time) (bool, error) {
	var movieCount, tvShowCount int64

	// Count movies for the date
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).
		Where("date = ? AND type = ?", date, "movie").
		Count(&movieCount).Error; err != nil {
		return false, fmt.Errorf("failed to count movies: %w", err)
	}

	// Count TV shows for the date
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).
		Where("date = ? AND type = ?", date, "tvshow").
		Count(&tvShowCount).Error; err != nil {
		return false, fmt.Errorf("failed to count TV shows: %w", err)
	}

	// Check what content is available in the cache to determine completeness criteria
	var cachedMoviesCount, cachedTVShowsCount int64
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Count(&cachedMoviesCount).Error; err != nil {
		return false, fmt.Errorf("failed to count cached movies: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Count(&cachedTVShowsCount).Error; err != nil {
		return false, fmt.Errorf("failed to count cached TV shows: %w", err)
	}

	// If we have both types of content cached, require both types of recommendations
	if cachedMoviesCount > 0 && cachedTVShowsCount > 0 {
		return movieCount > 0 && tvShowCount > 0, nil
	}

	// If only movies are cached, only require movie recommendations
	if cachedMoviesCount > 0 && cachedTVShowsCount == 0 {
		return movieCount > 0, nil
	}

	// If only TV shows are cached, only require TV show recommendations
	if cachedMoviesCount == 0 && cachedTVShowsCount > 0 {
		return tvShowCount > 0, nil
	}

	// If no content is cached, we can't generate recommendations
	return false, nil
}

// CheckRecommendationsExist checks if recommendations already exist for a specific date
func (r *Recommender) CheckRecommendationsExist(ctx context.Context, date time.Time) (bool, error) {
	var count int64

	// Count total recommendations for the date
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).
		Where("date = ?", date).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("failed to check existing recommendations: %w", err)
	}

	// If we have any recommendations, check if they're complete
	if count > 0 {
		complete, err := r.CheckRecommendationsComplete(ctx, date)
		if err != nil {
			return false, fmt.Errorf("failed to check recommendation completeness: %w", err)
		}
		return complete, nil
	}

	// Return false if we have no recommendations
	return false, nil
}

// loadPromptTemplate loads and parses a prompt template from the embedded filesystem.
// It returns a template that can be executed with the provided data.
func (r *Recommender) loadPromptTemplate(filename string) (*template.Template, error) {
	content, err := prompts.FS.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read prompt file %s: %w", filename, err)
	}

	tmpl, err := template.New(filename).Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse prompt template %s: %w", filename, err)
	}

	return tmpl, nil
}

// formatContent formats a slice of recommendations into a human-readable string.
// Each item is formatted with its title, year, rating, genre, runtime, and TMDb ID.
// It limits the number of items to prevent token limit issues.
func (r *Recommender) formatContent(items []models.Recommendation) string {
	var content strings.Builder
	// Limit to 50 items per type to prevent token limit issues
	limit := 50
	if len(items) > limit {
		items = items[:limit]
	}
	for _, item := range items {
		watched := ""
		if item.ViewCount > 0 {
			watched = " (watched)"
		}
		_, _ = fmt.Fprintf(&content, "- %s (%d)%s - Rating: %.1f - Genre: %s - Runtime: %d - TMDb ID: %d\n",
			item.Title, item.Year, watched, item.Rating, item.Genre, item.Runtime, item.TMDbID)
	}
	return content.String()
}

// limitPreviousRecommendations limits the number of previous recommendations to prevent token limit issues
func (r *Recommender) limitPreviousRecommendations(recs []models.Recommendation) []models.Recommendation {
	// Only keep the last 10 recommendations
	if len(recs) > 10 {
		return recs[len(recs)-10:]
	}
	return recs
}

// GenerateRecommendations generates new recommendations for the specified date.
// It uses OpenAI to analyze unwatched content and previous recommendations,
// then stores the generated recommendations in the database.
func (r *Recommender) GenerateRecommendations(ctx context.Context, date time.Time) error {
	l := logging.FromContext(ctx)
	l.Debugw("Starting recommendation generation")

	exists, err := r.CheckRecommendationsExist(ctx, date)
	if err != nil {
		return fmt.Errorf("failed to check existing recommendations: %w", err)
	}
	if exists {
		l.Infow("Recommendations already exist for date", "date", date)
		return nil
	}

	var cachedMovies []models.Movie
	if err := r.db.WithContext(ctx).Find(&cachedMovies).Error; err != nil {
		return fmt.Errorf("failed to get cached movies: %w", err)
	}
	l.Debugw("Found cached movies", "count", len(cachedMovies))

	var cachedTVShows []models.TVShow
	if err := r.db.WithContext(ctx).Find(&cachedTVShows).Error; err != nil {
		return fmt.Errorf("failed to get cached TV shows: %w", err)
	}
	l.Debugw("Found cached TV shows", "count", len(cachedTVShows))

	if len(cachedMovies) == 0 && len(cachedTVShows) == 0 {
		return fmt.Errorf("plex movie/TV cache is empty; run /cron/cache after Plex is reachable (skipping OpenAI)")
	}

	// Get previous recommendations for context
	prevDate := date.AddDate(0, 0, -1)
	prevRecs, err := r.GetRecommendationsForDate(ctx, prevDate)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to get previous recommendations: %w", err)
	}

	// Limit previous recommendations
	prevRecs = r.limitPreviousRecommendations(prevRecs)

	// Movies: include watched and unwatched so the model can pick a rewatch; TV: unwatched only.
	var allContent []models.Recommendation
	for _, movie := range cachedMovies {
		// Get TMDB poster URL if available
		posterURL := movie.PosterURL
		tmdbID := 0
		if movie.TMDbID != nil && *movie.TMDbID > 0 {
			tmdbID = *movie.TMDbID
			result, err := r.tmdb.SearchMovie(ctx, movie.Title, movie.Year)
			if err == nil && len(result.Results) > 0 {
				posterURL = r.tmdb.GetPosterURL(result.Results[0].PosterPath)
				if movie.TMDbID == nil {
					newTMDbID := result.Results[0].ID
					movie.TMDbID = &newTMDbID
					if err := r.db.WithContext(ctx).Save(&movie).Error; err != nil {
						l.Errorw("Failed to update movie TMDbID", "title", movie.Title, zap.Error(err))
					}
				}
			} else if err != nil {
				logTMDbErr(l, "Failed to search TMDb for movie", movie.Title, err)
			}
		} else {
			result, err := r.tmdb.SearchMovie(ctx, movie.Title, movie.Year)
			if err == nil && len(result.Results) > 0 {
				posterURL = r.tmdb.GetPosterURL(result.Results[0].PosterPath)
				tmdbID = result.Results[0].ID
				newTMDbID := result.Results[0].ID
				movie.TMDbID = &newTMDbID
				if err := r.db.WithContext(ctx).Save(&movie).Error; err != nil {
					l.Errorw("Failed to update movie TMDbID", "title", movie.Title, zap.Error(err))
				}
			} else if err != nil {
				logTMDbErr(l, "Failed to search TMDb for movie", movie.Title, err)
			}
		}

		allContent = append(allContent, models.Recommendation{
			Title:     movie.Title,
			Type:      "movie",
			Year:      movie.Year,
			Rating:    movie.Rating,
			Genre:     movie.Genre,
			PosterURL: posterURL,
			Runtime:   movie.Runtime,
			MovieID:   &movie.ID,
			TMDbID:    tmdbID,
			ViewCount: movie.ViewCount,
		})
	}

	for _, tvShow := range cachedTVShows {
		if tvShow.ViewCount > 0 {
			continue
		}
		// Get TMDB poster URL if available
		posterURL := tvShow.PosterURL
		tmdbID := 0
		if tvShow.TMDbID != nil && *tvShow.TMDbID > 0 {
			tmdbID = *tvShow.TMDbID
			result, err := r.tmdb.SearchTVShow(ctx, tvShow.Title, tvShow.Year)
			if err == nil && len(result.Results) > 0 {
				posterURL = r.tmdb.GetPosterURL(result.Results[0].PosterPath)
				if tvShow.TMDbID == nil {
					newTMDbID := result.Results[0].ID
					tvShow.TMDbID = &newTMDbID
					if err := r.db.WithContext(ctx).Save(&tvShow).Error; err != nil {
						l.Errorw("Failed to update TV show TMDbID", "title", tvShow.Title, zap.Error(err))
					}
				}
			} else if err != nil {
				logTMDbErr(l, "Failed to search TMDb for TV show", tvShow.Title, err)
			}
		} else {
			result, err := r.tmdb.SearchTVShow(ctx, tvShow.Title, tvShow.Year)
			if err == nil && len(result.Results) > 0 {
				posterURL = r.tmdb.GetPosterURL(result.Results[0].PosterPath)
				tmdbID = result.Results[0].ID
				newTMDbID := result.Results[0].ID
				tvShow.TMDbID = &newTMDbID
				if err := r.db.WithContext(ctx).Save(&tvShow).Error; err != nil {
					l.Errorw("Failed to update TV show TMDbID", "title", tvShow.Title, zap.Error(err))
				}
			} else if err != nil {
				logTMDbErr(l, "Failed to search TMDb for TV show", tvShow.Title, err)
			}
		}

		allContent = append(allContent, models.Recommendation{
			Title:     tvShow.Title,
			Type:      "tvshow",
			Year:      tvShow.Year,
			Rating:    tvShow.Rating,
			Genre:     tvShow.Genre,
			PosterURL: posterURL,
			Runtime:   tvShow.Seasons,
			TVShowID:  &tvShow.ID,
			TMDbID:    tmdbID,
			ViewCount: tvShow.ViewCount,
		})
	}

	// Prepare content for OpenAI
	content := RecommendationContext{
		Content: r.formatContent(allContent),
		Preferences: "User enjoys a mix of genres including action, drama, comedy, and sci-fi. " +
			"Prefers content with high ratings (above 7.5) and appreciates both popular and lesser-known titles.",
		PreviousRecommendations: r.formatContent(prevRecs),
	}

	l.Debugw("Available content for recommendations",
		"total_items", len(allContent),
		"cached_movies", len(cachedMovies),
		"cached_tv_shows", len(cachedTVShows),
		"content", content.Content)

	// Load prompt templates
	systemPrompt, err := r.loadPromptTemplate("system_openai.txt")
	if err != nil {
		return fmt.Errorf("failed to load system prompt: %w", err)
	}

	recommendationPrompt, err := r.loadPromptTemplate("recommendation_openai.txt")
	if err != nil {
		return fmt.Errorf("failed to load recommendation prompt: %w", err)
	}

	// Generate recommendations using OpenAI
	var systemMsg, userMsg strings.Builder
	if err := systemPrompt.Execute(&systemMsg, nil); err != nil {
		return fmt.Errorf("failed to execute system prompt: %w", err)
	}
	if err := recommendationPrompt.Execute(&userMsg, content); err != nil {
		return fmt.Errorf("failed to execute recommendation prompt: %w", err)
	}

	// Retry OpenAI API call with exponential backoff
	resp, err := r.retryOpenAIRequest(ctx, openai.ChatCompletionRequest{
		Model: openai.GPT5Mini,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleSystem,
				Content: systemMsg.String(),
			},
			{
				Role:    openai.ChatMessageRoleUser,
				Content: userMsg.String(),
			},
		},
		// gpt-5* models only allow temperature 0 or 1 (go-openai reasoning validator).
		Temperature: 1,
		ResponseFormat: &openai.ChatCompletionResponseFormat{
			Type: openai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to get OpenAI completion: %w", err)
	}

	// Parse OpenAI JSON response
	type RecommendationItem struct {
		Title       string `json:"title"`
		Type        string `json:"type,omitempty"`
		TMDbID      int    `json:"tmdb_id"`
		Explanation string `json:"explanation"`
	}

	type RecommendationResponse struct {
		Movies  []RecommendationItem `json:"movies"`
		TVShows []RecommendationItem `json:"tvshows"`
	}

	var recResponse RecommendationResponse
	if err := json.Unmarshal([]byte(resp.Choices[0].Message.Content), &recResponse); err != nil {
		return fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	// Create a map of all available content for quick lookup
	contentMap := make(map[string]models.Recommendation)
	for _, content := range allContent {
		contentMap[content.Title] = content
	}

	// Process recommendations
	recommendations := make([]models.Recommendation, 0)
	seenTitles := make(map[string]bool)

	// Helper function to process recommendation items
	processItems := func(items []RecommendationItem, contentType string) {
		for _, item := range items {
			if seenTitles[item.Title] {
				continue
			}

			if content, exists := contentMap[item.Title]; exists {
				content.Date = date
				content.TMDbID = item.TMDbID
				recommendations = append(recommendations, content)
				seenTitles[item.Title] = true
			}
		}
	}

	// Process each type of recommendation
	processItems(recResponse.Movies, "movie")
	processItems(recResponse.TVShows, "tvshow")

	// Enforce limits based on content type and requirements
	typeCounts := make(map[string]int)
	movieTypes := make(map[string]bool) // Track specific movie types (funny, action/drama, rewatched, additional)
	filteredRecommendations := make([]models.Recommendation, 0)

	// Check what content types are available to determine recommendation targets
	var availableMovies, availableTVShows int64
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Count(&availableMovies).Error; err != nil {
		return fmt.Errorf("failed to count available movies: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Count(&availableTVShows).Error; err != nil {
		return fmt.Errorf("failed to count available TV shows: %w", err)
	}

	// Determine target counts based on available content
	targetMovieCount := 4  // Standard target
	targetTVShowCount := 3 // Standard target

	if availableTVShows == 0 && availableMovies > 0 {
		targetMovieCount = 7
		targetTVShowCount = 0
		l.Infow("No TV shows available, generating movie-only recommendations",
			"target_movies", targetMovieCount)
	}

	// Process movies according to requirements
	for _, rec := range recommendations {
		if rec.Type == "movie" && typeCounts["movie"] < targetMovieCount {
			// Try to get diverse genres, but be flexible if content is limited
			if strings.Contains(strings.ToLower(rec.Genre), "comedy") && !movieTypes["funny"] {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
				movieTypes["funny"] = true
			} else if (strings.Contains(strings.ToLower(rec.Genre), "action") ||
				strings.Contains(strings.ToLower(rec.Genre), "drama")) &&
				!movieTypes["action_drama"] {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
				movieTypes["action_drama"] = true
			} else if !movieTypes["rewatched"] {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
				movieTypes["rewatched"] = true
			} else if typeCounts["movie"] < targetMovieCount { // Add additional movies up to target
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["movie"]++
			}
		}
	}

	// Process TV shows if available
	if availableTVShows > 0 {
		for _, rec := range recommendations {
			if rec.Type == "tvshow" && typeCounts["tvshow"] < targetTVShowCount {
				filteredRecommendations = append(filteredRecommendations, rec)
				typeCounts["tvshow"]++
			}
		}
	}

	l.Debugw("Recommendation counts",
		"movies", typeCounts["movie"],
		"tvshows", typeCounts["tvshow"],
		"funny_movie", movieTypes["funny"],
		"action_drama_movie", movieTypes["action_drama"],
		"rewatched_movie", movieTypes["rewatched"],
		"additional_movie", movieTypes["additional"])

	if len(filteredRecommendations) == 0 {
		l.Warnw("No new recommendations to store")
		return fmt.Errorf("no new recommendations to store")
	}

	// Save recommendations in a transaction. Clear any existing rows for this date first so
	// incomplete sets from failed runs can be replaced (CheckRecommendationsExist is false until complete).
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("date = ?", date).Delete(&models.Recommendation{}).Error; err != nil {
			return fmt.Errorf("failed to clear recommendations for date: %w", err)
		}

		for _, rec := range filteredRecommendations {
			var duplicate models.Recommendation
			err := tx.Where("title = ? AND date = ?", rec.Title, rec.Date).First(&duplicate).Error
			if err == nil {
				l.Warnw("Skipping duplicate recommendation",
					"title", rec.Title,
					"date", rec.Date)
				continue
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("failed to check for duplicate recommendation: %w", err)
			}

			if err := tx.Create(&rec).Error; err != nil {
				return fmt.Errorf("failed to save recommendation: %w", err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("failed to save recommendations in transaction: %w", err)
	}

	l.Debugw("Successfully stored recommendations",
		"total_count", len(filteredRecommendations),
		"movies", typeCounts["movie"],
		"tvshows", typeCounts["tvshow"])

	return nil
}

// GetStats retrieves statistics about the recommendations database.
// It returns counts of recommendations by type, date range, and genre distribution.
func (r *Recommender) GetStats(ctx context.Context) (*StatsData, error) {
	var stats StatsData

	// Get total recommendations
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Count(&stats.TotalRecommendations).Error; err != nil {
		return nil, fmt.Errorf("failed to get total recommendations: %w", err)
	}

	// Get counts by type
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Where("type = ?", "movie").Count(&stats.TotalMovies).Error; err != nil {
		return nil, fmt.Errorf("failed to get total movies: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Where("type = ?", "tvshow").Count(&stats.TotalTVShows).Error; err != nil {
		return nil, fmt.Errorf("failed to get total TV shows: %w", err)
	}

	// Get date range
	var firstDate, lastDate time.Time
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Order("date ASC").Limit(1).Pluck("date", &firstDate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get first date: %w", err)
		}
	}
	if err := r.db.WithContext(ctx).Model(&models.Recommendation{}).Order("date DESC").Limit(1).Pluck("date", &lastDate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get last date: %w", err)
		}
	}
	stats.FirstDate = firstDate
	stats.LastDate = lastDate

	// Calculate average daily recommendations
	if !firstDate.IsZero() && !lastDate.IsZero() {
		days := lastDate.Sub(firstDate).Hours() / 24
		if days > 0 {
			stats.AverageDailyRecommendations = float64(stats.TotalRecommendations) / days
		}
	}

	// Get genre distribution
	type genreCount struct {
		Genre string
		Count int64
	}
	var genreCounts []genreCount
	if err := r.db.WithContext(ctx).
		Model(&models.Recommendation{}).
		Select("genre, count(*) as count").
		Group("genre").
		Order("count DESC").
		Find(&genreCounts).Error; err != nil {
		return nil, fmt.Errorf("failed to get genre distribution: %w", err)
	}

	stats.GenreDistribution = make([]struct {
		Genre string
		Count int64
	}, len(genreCounts))
	for i, gc := range genreCounts {
		stats.GenreDistribution[i] = struct {
			Genre string
			Count int64
		}{
			Genre: gc.Genre,
			Count: gc.Count,
		}
	}

	// Get cache database statistics
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Count(&stats.TotalCachedMovies).Error; err != nil {
		return nil, fmt.Errorf("failed to get total cached movies: %w", err)
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Count(&stats.TotalCachedTVShows).Error; err != nil {
		return nil, fmt.Errorf("failed to get total cached TV shows: %w", err)
	}

	// Get last cache update time from the most recent movie or TV show update
	var lastMovieUpdate, lastTVShowUpdate time.Time
	if err := r.db.WithContext(ctx).Model(&models.Movie{}).Order("updated_at DESC").Limit(1).Pluck("updated_at", &lastMovieUpdate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get last movie update: %w", err)
		}
	}
	if err := r.db.WithContext(ctx).Model(&models.TVShow{}).Order("updated_at DESC").Limit(1).Pluck("updated_at", &lastTVShowUpdate).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("failed to get last TV show update: %w", err)
		}
	}

	// Use the most recent update time
	if lastMovieUpdate.After(lastTVShowUpdate) {
		stats.LastCacheUpdate = lastMovieUpdate
	} else {
		stats.LastCacheUpdate = lastTVShowUpdate
	}

	return &stats, nil
}

// startCacheCleanup starts a goroutine that periodically cleans up expired cache entries.
func (r *Recommender) startCacheCleanup(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.cleanupCache(ctx)
		}
	}
}

// cleanupCache removes expired entries from the cache.
func (r *Recommender) cleanupCache(ctx context.Context) {
	l := logging.FromContext(ctx)
	now := time.Now()

	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()

	var expiredKeys []string
	for key, entry := range r.cache {
		if now.Sub(entry.Timestamp) > entry.TTL {
			expiredKeys = append(expiredKeys, key)
		}
	}

	for _, key := range expiredKeys {
		delete(r.cache, key)
	}

	if len(expiredKeys) > 0 {
		l.Debugw("Cleaned up expired cache entries",
			"expired_count", len(expiredKeys),
			"remaining_count", len(r.cache))
	}
}

// SetCache adds or updates a cache entry.
func (r *Recommender) SetCache(key string, recommendation *models.Recommendation, ttl time.Duration) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	r.cache[key] = &CacheEntry{
		Recommendation: recommendation,
		Timestamp:      time.Now(),
		TTL:            ttl,
	}
}

// GetCache retrieves a cache entry if it exists and is not expired.
func (r *Recommender) GetCache(key string) (*models.Recommendation, bool) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()

	entry, exists := r.cache[key]
	if !exists {
		return nil, false
	}

	if time.Since(entry.Timestamp) > entry.TTL {
		delete(r.cache, key)
		return nil, false
	}

	return entry.Recommendation, true
}

// ClearCache removes all cache entries.
func (r *Recommender) ClearCache(ctx context.Context) {
	r.cacheMu.Lock()
	r.cache = make(map[string]*CacheEntry)
	r.cacheMu.Unlock()

	logging.FromContext(ctx).Debugw("Cache cleared")
}

// retryOpenAIRequest implements retry logic for OpenAI API calls with exponential backoff.
func (r *Recommender) retryOpenAIRequest(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	l := logging.FromContext(ctx)
	var resp openai.ChatCompletionResponse
	maxRetries := 3
	baseDelay := 2 * time.Second
	maxDelay := 30 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		result, err := r.openai.CreateChatCompletion(ctx, req)
		if err == nil {
			return result, nil
		}

		if attempt == maxRetries {
			l.Errorw("OpenAI API max retries exceeded",
				"attempts", attempt+1,
				zap.Error(err))
			return resp, err
		}

		delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt)))
		if delay > maxDelay {
			delay = maxDelay
		}

		l.Warnw("Retrying OpenAI API request",
			"attempt", attempt+1,
			"delay", delay,
			zap.Error(err))

		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		case <-time.After(delay):
		}
	}

	return resp, fmt.Errorf("unreachable code")
}
