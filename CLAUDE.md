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
- `lib/lock/`: Distributed locking system for concurrency control
- `lib/validation/`: JSON schema validation for external API responses

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
- `ETCD_ENDPOINTS`: Comma-separated etcd endpoints for distributed locking
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
- `GET /cron/recommend`: Generate new recommendations (runs hourly) - uses distributed locking
- `GET /cron/cache`: Update Plex/TMDb cache - uses distributed locking
- `GET /stats`: View recommendation statistics
- `GET /health`: Health check endpoint
- `GET /static/*`: Static file serving (favicon, CSS, JS)

## Recommendation Logic

The system generates exactly:
- 4 movies: funny unwatched, action/drama unwatched, rewatchable, additional
- 3 unwatched TV shows (with anime preference)

**Concurrency Control:**
- Distributed locking prevents concurrent cron job execution
- Cache management with TTL and automatic cleanup
- JSON schema validation for OpenAI responses
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
- All cron operations use distributed locking
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

## Common Issues and Solutions

**Build Issues:**
- Ensure all required environment variables are set
- Check Go version compatibility (requires Go 1.24+)
- Verify SQLite dependencies are installed

**Runtime Issues:**
- Check logs for API rate limiting errors
- Verify external API connectivity (Plex, TMDb, OpenAI)
- Monitor distributed lock status for cron job coordination

**Database Issues:**
- Migrations run automatically on startup
- Check disk space for SQLite database growth
- Monitor index usage with EXPLAIN QUERY PLAN

## Logging

All logging uses `log/slog` with JSON output format. Custom GORM logger in `lib/db/logger.go` integrates database queries with structured logging.

**Key Log Areas:**
- API integration (rate limiting, retries, errors)
- Database operations (migrations, queries, transactions)
- Cron job execution (locking, completion status)
- Cache management (cleanup, expiration)
- Recommendation generation (OpenAI responses, validation)