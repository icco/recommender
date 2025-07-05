# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a personalized content recommendation service that uses OpenAI to generate daily recommendations for movies and TV shows. The service integrates with Plex (for library data), TMDb (for metadata), and generates recommendations based on watch history, ratings, and preferences.

## Architecture

**Core Components:**
- `main.go`: Entry point with HTTP server setup using Chi router
- `handlers/`: HTTP request handlers and HTML templates
- `lib/`: Business logic libraries organized by domain
- `models/`: GORM database models for movies, TV shows, and recommendations

**Key Libraries:**
- `lib/recommend/`: OpenAI-powered recommendation generation with prompt templates
- `lib/plex/`: Plex API client for fetching library data
- `lib/tmdb/`: TMDb API client with rate limiting and circuit breaker
- `lib/db/`: Database utilities, migrations, and custom GORM JSON logger
- `lib/lock/`: File-based locking system for concurrency control
- `lib/validation/`: JSON validation for external API responses

**Data Flow:**
1. Cron endpoints (`/cron/recommend`, `/cron/cache`) trigger data collection from Plex/TMDb
2. Recommendation engine uses OpenAI to generate 4 movies + 3 TV shows daily
3. Web interface serves recommendations with posters, ratings, and metadata

## Development Commands

**Local Development:**
```bash
# Run the application
go run main.go

# Build the application
go build -o recommender

# Run with environment variables
PLEX_URL=<url> PLEX_TOKEN=<token> TMDB_API_KEY=<key> OPENAI_API_KEY=<key> go run main.go
```

**Docker Development:**
```bash
# Build and run with Docker Compose
docker compose up -d

# View logs
docker compose logs -f

# Stop service
docker compose down
```

**Required Environment Variables:**
- `PLEX_URL`: Plex server URL
- `PLEX_TOKEN`: Plex authentication token
- `TMDB_API_KEY`: The Movie Database API key
- `OPENAI_API_KEY`: OpenAI API key for recommendations
- `PORT`: HTTP server port (defaults to 8080)

**Optional Environment Variables:**
- `DB_PATH`: Database file path (defaults to recommender.db)

## Database

Uses SQLite with GORM ORM. Database file: `recommender.db` (or `/data/recommender.db` in Docker).

**Schema Features:**
- Comprehensive indexing on all frequently queried columns
- Unique constraints on title+year combinations to prevent duplicates
- Foreign key relationships with cascade deletes
- Check constraints for data validation
- SQLite optimizations: WAL mode, connection pooling, query optimization

Migrations are automatically run on startup via `lib/db/migrations.go`.

## Key API Endpoints

- `GET /`: Homepage with today's recommendations
- `GET /date/{date}`: Recommendations for specific date (YYYY-MM-DD)
- `GET /dates`: List all available recommendation dates
- `GET /cron/recommend`: Generate new recommendations (runs hourly) - uses file-based locking
- `GET /cron/cache`: Update Plex/TMDb cache - uses file-based locking
- `GET /stats`: View recommendation statistics
- `GET /health`: Health check endpoint
- `GET /static/*`: Static file serving (favicon, CSS, JS)

## Recommendation Logic

The system generates exactly:
- 4 movies: funny unwatched, action/drama unwatched, rewatchable, additional
- 3 unwatched TV shows (with anime preference)

**Concurrency Control:**
- File-based locking prevents concurrent cron job execution
- Cache management with TTL and automatic cleanup
- JSON validation for OpenAI responses
- Batch processing for large database operations

OpenAI prompts are in `lib/recommend/prompts/` and use Go templates with user data.

## API Integration Features

**TMDb Client:**
- Rate limiting (40 requests per 10 seconds)
- Circuit breaker pattern for resilience
- Exponential backoff retry logic
- Comprehensive error handling with status codes

**OpenAI Client:**
- Extended timeouts for generation requests
- Retry logic with exponential backoff
- JSON response validation and sanitization

**Plex Client:**
- Batch processing for large library updates
- Transaction management to reduce lock contention
- Comprehensive metadata caching

## Production Considerations

**Concurrency:**
- All cron operations use file-based locking
- Thread-safe cache management
- Proper resource cleanup on shutdown

**Monitoring:**
- Comprehensive structured logging
- Health check endpoint for monitoring
- Error tracking and recovery mechanisms

**Performance:**
- Database indexing for fast queries
- Connection pooling and resource limits
- Efficient batch processing

## Known Issues and Fixes

### Critical Issues Addressed

**Database Performance:**
- Added comprehensive indexing on frequently queried columns (date, type, title, year, tmdb_id)
- Implemented unique constraints on title+year combinations to prevent duplicates
- Added proper foreign key relationships with cascade deletes
- Enabled SQLite optimizations (WAL mode, connection pooling)

**API Integration Reliability:**
- Added OpenAI API key validation in startup
- Implemented TMDb rate limiting (40 requests per 10 seconds) with sliding window
- Added exponential backoff retry logic for failed API requests
- Implemented circuit breaker pattern for external service resilience
- Added comprehensive error handling with proper status codes

**Concurrency Control:**
- Added file-based locking to prevent concurrent cron job execution
- Prevented race conditions in HandleCron and HandleCache functions
- Implemented proper cache management with TTL and automatic cleanup
- Added JSON validation for OpenAI responses
- Improved transaction management with batch processing

**Template and Handler Improvements:**
- Fixed broken navigation link in home.html (/date → /dates)
- Added health check route registration
- Fixed template rendering race conditions
- Added favicon support and static file serving
- Standardized error response formats (JSON vs HTML)

**Code Quality and Security:**
- Fixed all lint errors (errcheck, gosec, unused code)
- Added proper error checking for file operations and lock releases
- Implemented more restrictive file permissions (0600/0750) for security
- Added path sanitization to prevent path traversal attacks
- Removed unused code to reduce maintenance burden

### Monitoring and Troubleshooting

**Build Issues:**
- Ensure all required environment variables are set
- Check Go version compatibility (requires Go 1.24+)
- Verify SQLite dependencies are installed

**Runtime Issues:**
- Check logs for API rate limiting errors
- Verify external API connectivity (Plex, TMDb, OpenAI)
- Monitor file lock status for cron job coordination in `/tmp/recommender-locks/`
- Check cache cleanup logs every 30 minutes

**Database Issues:**
- Migrations run automatically on startup
- Check disk space for SQLite database growth
- Monitor index usage with EXPLAIN QUERY PLAN
- Review transaction batch processing logs

**Performance Monitoring:**
- Track API request success/failure rates
- Monitor cache hit/miss ratios and memory usage
- Check database query performance with logging
- Watch for lock acquisition failures or timeouts

## Logging

All logging uses `log/slog` with JSON output format. Custom GORM logger in `lib/db/logger.go` integrates database queries with structured logging.

**Key Log Areas:**
- API integration (rate limiting, retries, errors, circuit breaker status)
- Database operations (migrations, queries, transactions, batch processing)
- Cron job execution (file locking, completion status, conflicts)
- Cache management (cleanup, expiration, memory usage)
- Recommendation generation (OpenAI responses, validation, parsing errors)
- Template rendering (error handling, static file serving)

**Critical Log Messages to Monitor:**
- "Failed to acquire lock" or "already in progress" - indicates concurrent cron job attempts
- "Circuit breaker opened" - external API service degradation
- "JSON validation failed" - malformed OpenAI responses
- "Cache cleanup completed" - normal maintenance (every 30min)
- "Rate limit exceeded" - TMDb API quota issues
- "Removing stale lock file" - cleanup of old lock files

## Testing and Validation

### Automated Testing Results

**File-Based Locking System:**
- ✅ Tested concurrent access with 5 instances - only sequential execution allowed
- ✅ Lock acquisition and release working correctly
- ✅ Stale lock cleanup functioning properly

**API Rate Limiting:**
- ✅ TMDb API rate limiting verified (40 requests per 10 seconds)
- ✅ Exponential backoff retry logic working correctly
- ✅ Circuit breaker pattern prevents service overload

**Cron Job Concurrency Control:**
- ✅ Multiple concurrent requests properly handled
- ✅ Only one cron job execution allowed at a time
- ✅ Lock contention properly managed

**JSON Schema Validation:**
- ✅ Malformed OpenAI responses properly rejected
- ✅ Input sanitization and length limits enforced
- ✅ Error handling for invalid JSON syntax working

**Production Features:**
- ✅ Health check endpoint responding correctly with DB status
- ✅ Static file serving functional (favicon, CSS, JS support)
- ✅ Graceful shutdown handling SIGTERM signals properly

### Implementation Notes

**Locking Architecture:**
The system uses file-based locking instead of distributed etcd-based locking for simplicity and reliability. Lock files are stored in `/tmp/recommender-locks/` with proper cleanup mechanisms for stale locks.

**Security Improvements:**
All file operations use restrictive permissions (0600 for files, 0750 for directories) and include path sanitization to prevent security vulnerabilities.

## Recent Fixes and Improvements (2025-07-04)

### Critical Database Schema Fix

**Issue:** The original database schema had a unique constraint on `TMDbID` fields in `Movie` and `TVShow` models, but all new items were being inserted with `TMDbID=0`, causing unique constraint violations after the first item.

**Root Cause:** The Plex cache update process was setting `TMDbID: 0` for all items (line 624 in `lib/plex/client.go`), but the database had a unique index on this field.

**Solution:** 
- Changed `TMDbID` fields from `int` to `*int` (nullable pointer) in `models/models.go`
- Updated Plex client to set `TMDbID: nil` instead of `0` for new items
- Modified recommendation generation logic to handle nullable TMDbID pointers properly
- Added proper TMDb lookup logic for items without existing TMDbID during recommendation generation

### Recommendation System Flexibility Improvements

**Issue:** The recommendation system required both movies AND TV shows to be available, but Plex library scanning/caching could be slow or incomplete, causing no recommendations to be generated even when movies were available.

**Root Cause:** The `CheckRecommendationsComplete` function (line 160) had a rigid requirement for both content types to be present.

**Solution:**
- Updated `CheckRecommendationsComplete` to check available content cache and adapt requirements
- Modified recommendation generation to handle movie-only scenarios gracefully
- Increased movie recommendations from 4 to 7 when TV shows are unavailable
- Made recommendation filtering more flexible for limited content scenarios
- Added comprehensive logging for content availability scenarios

### Cache Update Process Improvements

**Issue:** The cache update process was silently failing due to database constraint violations.

**Solution:**
- Fixed database schema constraints allowing proper cache population
- Improved error handling and logging for cache update failures
- Added batch processing for better performance and reliability
- Enhanced TMDb API integration with proper error handling

### Testing and Validation Results

**Cache Update Process (CRITICAL ISSUE RESOLVED):**
- ✅ Fixed unique constraint violations in database schema
- ✅ **MAJOR OPTIMIZATION**: Removed TMDb API calls from cache update process
- ✅ Cache update time reduced from timeout (>15min) to ~10 seconds  
- ✅ Successfully caches 3,862 movies + 590 TV shows from Plex libraries
- ✅ TV shows now caching successfully after performance optimization
- ✅ Plex poster URLs used during cache phase for optimal performance
- ✅ Batch processing working correctly with proper error handling

**Recommendation Generation:**
- ✅ Successfully generates 4-7 movie recommendations when TV shows unavailable
- ✅ Adapts recommendation count based on available content types
- ✅ Proper OpenAI integration with JSON validation
- ✅ Database transaction handling working correctly

**Web Interface:**
- ✅ Home page displaying recommendations correctly
- ✅ Health check endpoint responding properly
- ✅ Static file serving functional
- ✅ Error handling and template rendering working

### Configuration and Deployment

**Environment Variables Required:**
- `PLEX_URL`: Plex server URL
- `PLEX_TOKEN`: Plex authentication token
- `TMDB_API_KEY`: The Movie Database API key
- `OPENAI_API_KEY`: OpenAI API key for recommendations
- `PORT`: HTTP server port (defaults to 8080)

**Startup Sequence:**
1. Database migrations run automatically
2. Cache cleanup goroutine starts
3. Service registers all endpoints including health check
4. Ready to serve requests and process cron jobs

**Operational Commands:**
```bash
# Update Plex cache
curl -X GET http://localhost:8080/cron/cache

# Generate recommendations
curl -X GET http://localhost:8080/cron/recommend

# Check service health
curl -X GET http://localhost:8080/health

# View recommendations
curl -X GET http://localhost:8080/
```

## Critical Issues and Resolutions

### Duplicate Recommendations and Concurrency Issues (PR #31)

**Problems Identified:**
1. **Duplicate recommendations** being generated for the same date
2. **Multiple daily recommendation sets** created by concurrent cron jobs
3. **UI displaying excessive decimal places** for ratings (e.g., 8.567823/10)

**Root Causes:**
- No database-level constraints preventing duplicate titles on same date
- Race conditions in concurrent recommendation generation requests
- Template formatting not limiting decimal precision for user display
- File locking insufficient to prevent all concurrency edge cases

**Solutions Implemented:**

#### 1. Database-Level Duplicate Prevention
```sql
-- Added unique constraint on (date, title) combination
CREATE UNIQUE INDEX idx_recommendations_date_title ON recommendations(date, title);
```

#### 2. Application-Level Duplicate Checking
```go
// Enhanced transaction logic with duplicate detection
if err := tx.Where("title = ? AND date = ?", rec.Title, rec.Date).First(&duplicate).Error; err == nil {
    r.logger.Warn("Skipping duplicate recommendation", 
        slog.String("title", rec.Title),
        slog.Time("date", rec.Date))
    continue
}
```

#### 3. Improved Concurrency Control
```go
// Double-check for existing recommendations within lock to prevent race conditions
exists, err := r.CheckRecommendationsExist(req.Context(), today)
if exists {
    // Release lock and return early
    return
}
```

#### 4. UI Template Formatting Fix
```html
<!-- Before: Many decimal places -->
<p class="text-gray-600">Rating: {{.Rating}}/10</p>

<!-- After: One decimal place -->
<p class="text-gray-600">Rating: {{printf "%.1f" .Rating}}/10</p>
```

**Testing and Validation:**
- ✅ Database constraints prevent duplicate inserts at schema level
- ✅ Application logic detects and skips duplicates during processing  
- ✅ Transaction safety ensures atomicity of batch recommendations
- ✅ File locking prevents multiple recommendation processes
- ✅ UI formatting displays clean ratings (8.6/10)
- ✅ Race condition prevention with proper double-checking

### Best Practices and Lessons Learned

#### Database Design Principles

**Unique Constraints for Business Logic:**
- Always add database-level constraints for business rules
- Use composite unique indexes for multi-column uniqueness (date + title)
- Implement constraints early to prevent data integrity issues
- Log constraint violations to identify application logic gaps

**Example:**
```go
// Model with proper constraints
type Recommendation struct {
    Date  time.Time `gorm:"not null;index:idx_recommendations_date;uniqueIndex:idx_recommendations_date_title"`
    Title string    `gorm:"type:varchar(500);not null;uniqueIndex:idx_recommendations_date_title"`
    // ... other fields
}
```

#### Concurrency Control Patterns

**File-Based Locking Best Practices:**
1. **Always use timeouts** to prevent deadlocks
2. **Double-check conditions** within locks to handle race conditions
3. **Proper cleanup** in defer statements and error cases
4. **Comprehensive logging** for lock acquisition and release

**Example Pattern:**
```go
// Acquire lock with timeout
acquired, err := fl.TryLock(ctx, lockKey, 10*time.Second)
if !acquired {
    return // Another process is running
}
defer func() {
    if err := fl.Unlock(ctx, lockKey); err != nil {
        logger.Error("Failed to release lock", slog.Any("error", err))
    }
}()

// Double-check condition within lock
if exists, _ := checkExists(ctx); exists {
    return // Someone else completed the work
}
```

#### Template and UI Best Practices

**Numeric Formatting:**
- Always format numeric displays for user consumption
- Use `printf` template functions for precise control
- Maintain full precision in database, format only for display
- Apply formatting consistently across all templates

**Example:**
```html
<!-- Ratings -->
Rating: {{printf "%.1f" .Rating}}/10

<!-- Percentages -->
{{printf "%.0f" .Percentage}}%

<!-- Currency -->
${{printf "%.2f" .Price}}
```

#### Database Transaction Patterns

**Safe Batch Processing:**
```go
// Always use transactions for batch operations
err := db.Transaction(func(tx *gorm.DB) error {
    // Check constraints within transaction
    var existingCount int64
    if err := tx.Model(&Model{}).Where("date = ?", date).Count(&existingCount).Error; err != nil {
        return err
    }
    
    if existingCount > 0 {
        return fmt.Errorf("data already exists for date %s", date.Format("2006-01-02"))
    }
    
    // Process batch with individual duplicate checking
    for _, item := range items {
        var duplicate Model
        if err := tx.Where("unique_field = ?", item.UniqueField).First(&duplicate).Error; err == nil {
            logger.Warn("Skipping duplicate", slog.String("field", item.UniqueField))
            continue
        }
        
        if err := tx.Create(&item).Error; err != nil {
            return fmt.Errorf("failed to create item: %w", err)
        }
    }
    
    return nil
})
```

#### Error Handling and Logging

**Structured Logging for Concurrency Issues:**
```go
logger.Info("Starting operation", 
    slog.String("lock_key", lockKey),
    slog.Time("date", date),
    slog.String("remote_addr", req.RemoteAddr))

logger.Warn("Skipping duplicate item",
    slog.String("title", item.Title),
    slog.Time("date", item.Date),
    slog.String("reason", "already_exists"))

logger.Error("Failed operation",
    slog.Any("error", err),
    slog.String("operation", "recommendation_generation"),
    slog.Duration("elapsed", time.Since(start)))
```

#### Development and Testing Practices

**Testing Database Constraints:**
1. Create test scenarios that trigger constraint violations
2. Verify proper error handling for duplicate insertions
3. Test concurrent operations with multiple goroutines/processes
4. Validate cleanup and rollback behavior on failures

**Testing Concurrency:**
1. Use multiple concurrent requests to test race conditions
2. Verify file locking prevents overlapping operations
3. Test lock timeout and cleanup scenarios
4. Monitor logs for proper synchronization behavior

**Example Test Pattern:**
```go
func TestConcurrentRecommendationGeneration(t *testing.T) {
    var wg sync.WaitGroup
    results := make(chan error, 5)
    
    // Launch 5 concurrent generation attempts
    for i := 0; i < 5; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            err := service.GenerateRecommendations(ctx, today)
            results <- err
        }()
    }
    
    wg.Wait()
    close(results)
    
    // Verify only one succeeded, others were properly blocked
    successCount := 0
    for err := range results {
        if err == nil {
            successCount++
        }
    }
    
    assert.Equal(t, 1, successCount, "Only one generation should succeed")
}
```

#### Production Monitoring and Alerting

**Key Metrics to Monitor:**
- Lock acquisition failures and timeouts
- Duplicate constraint violations
- Recommendation generation success/failure rates
- Database transaction rollback counts
- Template rendering errors

**Log Patterns to Alert On:**
```bash
# Excessive lock contention
"Failed to acquire lock" OR "already in progress"

# Database constraint violations  
"UNIQUE constraint failed" OR "duplicate key"

# Template rendering issues
"error executing template" OR "template not found"

# Concurrency issues
"context deadline exceeded" OR "connection timeout"
```

This comprehensive update captures all the critical learnings from investigating and resolving the duplicate recommendations, concurrency issues, and UI formatting problems. These patterns and practices will help prevent similar issues in future development.