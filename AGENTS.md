# AGENTS.md

Guidance for coding agents working on recommender.
See [CLAUDE.md](CLAUDE.md) for Claude Code entrypoint (`@AGENTS.md`).

## Project Overview

Personalized content recommendation service using Gemini (Vertex AI) to generate daily recommendations for movies, TV shows, and books from Plex library data and Goodreads shelves.

## Architecture & Layout

- `main.go` — Entry point with Chi v5 HTTP router.
- `handlers/` — HTTP request handlers and HTML templates.
- `lib/recommend/` — Candidate scoring, ID-based slotting, Gemini client, and pipeline.
- `lib/plex/`, `lib/omdb/`, `lib/metacritic/`, `lib/db/` — Integrations and DB layer.
- `models/` — GORM database models for PostgreSQL.
- External standalone API modules: `github.com/icco/{goodreads,omdb,trakt,anilist}` (fix bugs upstream, not in local forks).

## Critical Invariants & Edge Cases

- **Database Schemas**:
  - `recommendations.type` has a DB CHECK constraint. Widening allowed types requires an explicit `ALTER TABLE` **before** GORM `AutoMigrate`.
  - Recommendation uniqueness is `(date, type, title)`, NOT `(date, title)` (books and films often share titles).
- **External Data & Ratings**:
  - **Goodreads**: User ID is `/user/show/<id>` (not author ID). `Book.Rating` is stored rescaled ×2 (0–10 scale).
  - **OMDb**: For movies only (OMDb has no Metacritic data for TV). Batch-capped to protect free quota (1000/day).
  - **Plex**: TV shows must use `audienceRating` (Plex never sets `rating` on shows). Movies prefer `rating`.
- **Vertex AI**: Authenticates via Application Default Credentials (ADC). Set `GOOGLE_GENAI_USE_VERTEXAI=true`, `GOOGLE_CLOUD_PROJECT`, and `GOOGLE_CLOUD_LOCATION`. No API keys.

## Commands

```sh
go run main.go            # Run locally
go build -o recommender   # Build binary
go test ./...             # Run test suite
docker compose up -d      # Run with local Postgres
```

## Conventions

- Conventional Commits with lowercase subjects.
- Always verify tests pass before submitting changes.
