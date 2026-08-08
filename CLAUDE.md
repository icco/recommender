# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a personalized content recommendation service that uses Gemini (on Vertex AI) to generate daily recommendations for movies, TV shows, and books. The service integrates with Plex (for library data + GUIDs) and generates recommendations based on watch history, ratings, and a Plex-derived taste profile. Movie and TV recommendations are titles already in the Plex library; book recommendations come from the user's Goodreads want-to-read shelf.

## Architecture

**Core Components:**
- `main.go`: Entry point with HTTP server setup using Chi router
- `handlers/`: HTTP request handlers and HTML templates
- `lib/`: Business logic libraries organized by domain
- `models/`: GORM database models for movies, TV shows, and recommendations

**Key Libraries:**
- `lib/recommend/`: Gemini-powered recommendation generation — candidate scoring/shortlisting (`candidates.go`), ID-based slotting (`slotting.go`), the Gemini client (`llm.go`), the taste profile (`profile.go`), and the pipeline (`generate.go`)
- `lib/plex/`: Plex API client for fetching library data
- `lib/omdb/`: OMDb API client (Metacritic Metascores) — sliding-window rate limit, circuit breaker, and a key-safe URL
- `lib/metacritic/`: builds metacritic.com links from title slugs (Metacritic exposes no joinable ID)
- `lib/db/`: Database utilities, migrations, and custom GORM JSON logger
- `lib/lock/`: File-based locking system for concurrency control
- `lib/validation/`: JSON validation for external API responses

**External API clients (extracted to their own repos):** `lib/goodreads`, `lib/omdb`, `lib/trakt`, and `lib/anilist` used to live here. They are now standalone MIT modules — `github.com/icco/{goodreads,omdb,trakt,anilist}` — because none of them were recommender-specific and several fill real gaps in the Go ecosystem. Fix bugs upstream and bump the dependency; do not re-add a local copy. Two behaviors worth knowing without reading their source:

- **goodreads**: the user id is from `/user/show/<id>`, a *different namespace* from `/author/show/<id>`. Passing an author id returns a different person's shelves with no error. `Shelf` returns `ErrTruncated` alongside partial results past `MaxPages`.
- **omdb**: score fields are `*int`/`*float64` because OMDb reports a missing score as the string `"N/A"`; nil keeps "unscored" distinguishable from "scored zero".

**Data Flow:**
1. Cron endpoints (`/cron/recommend`, `/cron/cache`) trigger data collection from Plex
2. Recommendation engine scores cached titles, shortlists them (date-seeded), and uses Gemini to pick 4 movies + 3 TV shows + 3 books daily by ID
3. Web interface serves recommendations with posters, ratings, and metadata

## Development Commands

**Local Development:**
```bash
# Run the application
go run main.go

# Build the application
go build -o recommender

# Run with environment variables
PLEX_URL=<url> PLEX_TOKEN=<token> \
  GOOGLE_GENAI_USE_VERTEXAI=true GOOGLE_CLOUD_PROJECT=<proj> GOOGLE_CLOUD_LOCATION=us-central1 \
  go run main.go   # requires ADC: `gcloud auth application-default login`
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
- `DATABASE_URL`: Postgres connection string
- `PLEX_URL`: Plex server URL
- `PLEX_TOKEN`: Plex authentication token
- `GOOGLE_CLOUD_PROJECT`: GCP project ID (Vertex AI API enabled)
- `GOOGLE_CLOUD_LOCATION`: Vertex AI region (e.g. `us-central1`)

**Optional Environment Variables:**
- `GOOGLE_GENAI_USE_VERTEXAI`: `true` to use Vertex AI (recommended)
- `GEMINI_MODEL`: model ID (defaults to `gemini-2.5-flash`)
- `GOOGLE_APPLICATION_CREDENTIALS`: service-account key path for local dev (prod uses ambient ADC)
- `TRAKT_CLIENT_ID` / `TRAKT_CLIENT_SECRET`: enable Trakt signals
- `TRAKT_CONNECT_TOKEN`: shared secret required to call `GET /trakt/connect` (disabled when unset)
- `ANILIST_USERNAME`: enable AniList (public list) signals
- `OMDB_API_KEY`: enable Metacritic Metascores (fetched via OMDb, joined on IMDb ID)
- `OMDB_BATCH_SIZE`: OMDb lookups per cache run (defaults to 40, sized for OMDb's free 1000/day quota)
- `GOODREADS_USER_ID`: Goodreads numeric user id; enables the books tier
- `PORT`: HTTP server port (defaults to 8080)
- `POSTER_DIR`: Directory for locally cached Plex posters (defaults to `posters`)

External signals (Trakt watched/ratings/watchlist, AniList scores) are synced during `/cron/cache` into `ExternalSignal` and only re-rank owned Plex titles: they feed genre affinity, a watchlist score boost, watched-elsewhere handling, and prompt context. Sources are optional and skipped when their env vars are unset. Metacritic enrichment (`lib/recommend/metascore.go`) rides the same `SignalSource` interface but writes `Metascore`/`MetascoreAt` onto `Movie`/`TVShow` rather than `ExternalSignal`, because a critic score is title metadata, not a per-user signal. It is incremental, batch-capped, and time-boxed: a run takes the oldest never-checked rows first, stamps `MetascoreAt` even on a miss so uncovered titles aren't retried every hour, and never re-fetches a title that already has a score. The time box matters — `/cron/cache` and `/cron/recommend` share `cronBackgroundLockKey` and `cron.sh` only sleeps 120s between them, so enrichment that overran its budget would starve the day's recommendation run of the lock and silently skip the day. Trakt OAuth (device flow) tokens live in `OAuthToken`; authorize via `GET /trakt/connect?token=…`.

Auth to Vertex AI uses Application Default Credentials — no API key.

## Database

Two schema notes that bite silently:

- **`recommendations.type` has a CHECK constraint.** GORM emits it only when it *creates* the column and never reconciles a changed one, so widening the allowed values requires an explicit `ALTER TABLE` **before** `AutoMigrate` (see `widenRecommendationTypeCheck`). Otherwise a pre-existing database rejects every row of a newly added type. The constraint is found by scanning `pg_constraint`, since GORM's generated name isn't guaranteed.
- **Recommendation uniqueness is `(date, type, title)`, not `(date, title)`.** A book and a film routinely share a title (Dune), and the narrower key silently dropped one of the two.

Uses Postgres with GORM ORM. Connection string comes from `DATABASE_URL`. Docker Compose runs a bundled `postgres:17` service; tests use an isolated schema per test on the Postgres named by `DATABASE_URL` (see `lib/dbtest`).

**Schema Features:**
- Comprehensive indexing on all frequently queried columns
- Unique constraints on title+year combinations to prevent duplicates
- Foreign key relationships with cascade deletes
- Check constraints for data validation
- Connection pooling tuned in `main.go` (`SetMaxOpenConns`/`SetConnMaxLifetime`)

Migrations are automatically run on startup via `lib/db/migrations.go`.

Any raw SQL must be Postgres dialect (e.g. `to_char()` for date formatting, not SQLite's `strftime()`).

## Key API Endpoints

- `GET /`: Homepage with today's recommendations
- `GET /date/{date}`: Recommendations for specific date (YYYY-MM-DD)
- `GET /dates`: List all available recommendation dates
- `GET /cron/recommend`: Generate new recommendations (runs hourly) - uses file-based locking
- `GET /cron/cache`: Update the Plex cache - uses file-based locking
- `GET /stats`: View recommendation statistics
- `GET /health`: Health check endpoint
- `GET /static/*`: Static file serving (favicon, CSS, JS)

## Recommendation Logic

The system generates exactly:
- 4 movies: funny unwatched, action/drama unwatched, rewatchable, additional
- 3 unwatched TV shows (with anime preference)
- 3 books from the Goodreads want-to-read shelf (no role slots)

**Concurrency Control:**
- File-based locking prevents concurrent cron job execution
- ID-based matching + deterministic slotting of the model's picks
- Batch processing for large database operations

Gemini prompts are in `lib/recommend/prompts/` and use Go templates with the scored shortlist.

## API Integration Features

**OMDb Client (`github.com/icco/omdb`):**
- Metacritic has no public API; OMDb exposes the Metascore keyed by IMDb ID, which Plex GUIDs already give us
- **Movies only.** OMDb has no Metacritic data for TV — verified against a real key: 0 of 12 series returned a Metascore by title or by IMDb id, and the season/episode endpoints have no score field. Do not re-add TV lookups; they spend quota to store nothing. TV ratings come from Plex `audienceRating`
- `Metascore` arrives as a *string* and can be `"N/A"`. Parse defensively into `*int` so missing stays distinguishable from zero
- Free tier is 1000 requests/day, hence the per-run batch cap rather than a full-library sweep
- Sliding-window rate limiting, a circuit breaker, bounded retries, and a URL that never carries the api key

**Gemini Client (`lib/recommend/llm.go`):**
- Vertex AI backend via `github.com/icco/gutil/vertex`, auth by ADC
- JSON-constrained output via `ResponseMIMEType` + `ResponseSchema`
- Isolated behind the `Chatter` interface so tests use a fake

**Books (`lib/recommend/books.go`, `github.com/icco/goodreads`):**
- Books are the one source that owns its candidate pool. Trakt/AniList/OMDb only re-rank owned Plex titles via `ExternalSignal`, which can join to a Movie or TVShow but not a Book; the want-to-read shelf *is* the recommendable set, so books get their own table
- **Author affinity, not genre affinity.** The shelf feed has no genre field — only `user_shelves`, the user's own shelf names, which are near-empty on real accounts (measured on Nat's profile: 4 "fiction", 3 "digital-owned", 1 "top-ten" out of 100 read books). Read-shelf star ratings are rich by contrast (40× 5-star, 30× 4-star), so affinity keys on author
- `Book.Rating` stores the Goodreads community average **rescaled ×2 to 0-10**, so one scoring path serves all three tiers. Cards divide back for display via `Recommendation.GoodreadsRating()`
- `Shelf` is refreshed on every sync: a finished book moves from to-read to read, and a stale row would recommend it forever
- Goodreads covers are public on `i.gr-assets.com`, so `cachePoster` skips books rather than routing them through the Plex downloader
- Books never get a `MetacriticURL`: Metacritic has no book section, so the slug would resolve to an unrelated film
- Everything book-related fails soft. An unset `GOODREADS_USER_ID` or an unreachable Goodreads costs the books tier only; `TargetBooks` collapses to 0 and the prompt drops its books section
- Shelves are large — Nat's are 1749 to-read and 1131 read (1112 rated) as of Aug 2026 — so the sync batches upserts (`upsertBatch`) rather than one round trip per row. It shares `cronBackgroundLockKey` with the Plex refresh, so per-row writes to `rope.local` would eat the budget. A shelf past `maxPages` returns `goodreads.ErrTruncated` *with* the partial results, so a capped pool is logged, not silently mistaken for a complete one

**Plex Client:**
- Ratings prefer `rating`, falling back to `audienceRating`. Plex sets `rating` on movies but **never** on shows (measured: 0 of 1408 shows had `rating`, 1397 had `audienceRating`), so decoding only `rating` rendered every TV card as 0.0
- Batch processing for large library updates
- Transaction management to reduce lock contention
- Comprehensive metadata caching
