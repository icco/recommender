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