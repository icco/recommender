# Recommender

Daily movie and TV recommendations from your **Plex** library, enriched with **TMDb** metadata and chosen by **OpenAI**. This is an experiment in building an app with generative AI.

Stack: **Go**, **Chi** (routing), **GORM** (ORM), **SQLite**, **log/slog** (JSON logs).

## What you get

The home page shows one set of recommendations per calendar day:

- Up to **four movies** (targets: comedy-leaning, action/drama, “rewatch” from titles marked watched in Plex, plus extras). Slot filling uses genre heuristics on the model output.
- Up to **three TV shows**, drawn only from **unwatched** shows in the Plex cache (`ViewCount == 0`).

Each card shows poster, title, year, rating, genre, and runtime (movies) or season count (TV).

Past days are listed at `/dates` (one row per distinct day, paginated).

## Data sources (implemented)

- **Plex** — library scan and watch counts during cache update
- **TMDb** — posters and IDs during recommendation generation (not during bulk cache, for speed)
- **OpenAI** — JSON recommendations from a constrained prompt

### Not implemented (possible future work)

- AniList, Letterboxd, Trakt, and other catalogs mentioned in earlier notes
- Incremental “fill missing slots only” runs (each successful run replaces the whole day’s rows when incomplete)

## API endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Today’s recommendations (UTC date) |
| GET | `/date/YYYY-MM-DD` | Recommendations for that day |
| GET | `/dates` | Paginated list of days (`?page`, `?size`) |
| GET | `/cron/recommend` | Start recommendation generation (async; file lock) |
| GET | `/cron/cache` | Refresh Plex → SQLite cache (async; file lock) |
| GET | `/stats` | DB statistics |
| GET | `/health` | JSON health including DB ping |
| GET | `/static/*` | Embedded static files (e.g. favicon) |

## Environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `PLEX_URL` | yes | Plex server base URL |
| `PLEX_TOKEN` | yes | Plex token |
| `TMDB_API_KEY` | yes | TMDb API key |
| `OPENAI_API_KEY` | yes | OpenAI API key |
| `PORT` | no | HTTP port (default `8080`) |
| `DB_PATH` | no | SQLite file path (default `recommender.db`; Docker Compose uses `/data/recommender.db`) |

## Repository layout

```
recommender/
├── handlers/          # HTTP handlers and HTML templates (embedded)
├── lib/
│   ├── db/           # Migrations and GORM logger
│   ├── health/       # Health check
│   ├── lock/         # File locks for cron endpoints
│   ├── plex/         # Plex client and cache update
│   ├── recommend/    # OpenAI generation and queries
│   ├── tmdb/         # TMDb client
│   └── validation/   # Request and response validation helpers
├── models/           # GORM models
├── static/           # Assets embedded into the binary (e.g. favicon)
└── data/             # Docker volume mount target for the DB (optional locally)
```

Package docs: [pkg.go.dev/github.com/icco/recommender](https://pkg.go.dev/github.com/icco/recommender).

## Running

### Local

```bash
export PLEX_URL=... PLEX_TOKEN=... TMDB_API_KEY=... OPENAI_API_KEY=...
go run .
```

Optional: `DB_PATH=/path/to/recommender.db`.

### Docker Compose

```bash
docker compose up -d
```

Open `http://localhost:8080`. Trigger cache then recommendations:

```bash
curl -sS "http://localhost:8080/cron/cache"
curl -sS "http://localhost:8080/cron/recommend"
```

Logs: `docker compose logs -f`. Stop: `docker compose down`.

The compose file mounts `./data` at `/data` and sets `DB_PATH=/data/recommender.db`.

## Recommendation flow (summary)

1. **`/cron/cache`** — Reads Plex libraries, stores all movies and TV shows in SQLite (including `view_count` from Plex). Poster thumbs are stored as absolute URLs when Plex returns relative paths.
2. **`/cron/recommend`** — If the day is not yet “complete,” loads cached titles, builds the prompt (movies include watched + unwatched; TV prompts only unwatched), calls OpenAI, filters results, then **replaces** any existing rows for that UTC date so partial runs can succeed on retry.

“Complete” for a day depends on what exists in the cache (e.g. both movies and TV in library ⇒ need both types in that day’s recommendations).
