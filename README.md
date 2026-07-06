# Recommender

Daily movie and TV recommendations from your **Plex** library, enriched with **TMDb** metadata and chosen by **Gemini** (on Vertex AI). This is an experiment in building an app with generative AI.

Stack: **Go**, **Chi** (routing), **GORM** (ORM), **Postgres**, **Gemini on Vertex AI** (`google.golang.org/genai`), **zap + gutil/logging** (JSON logs), **OpenTelemetry** (HTTP metrics on `/metrics`).

## What you get

The home page shows one set of recommendations per calendar day:

- Up to **four movies** (targets: comedy-leaning, action/drama, “rewatch” from titles marked watched in Plex, plus extras). Slot filling uses genre heuristics on the model output.
- Up to **three TV shows**, drawn only from **unwatched** shows in the Plex cache (`ViewCount == 0`).

Each card shows poster, title, year, rating, genre, and runtime (movies) or season count (TV).

Past days are listed at `/dates` (one row per distinct day, paginated).

## Data sources (implemented)

- **Plex** — library scan, watch counts, and GUIDs (imdb/tmdb/tvdb) + full genres during cache update
- **TMDb** — fallback poster fill for the day's finalists when Plex has no poster
- **Gemini (Vertex AI)** — picks recommendations by ID from a scored shortlist via JSON-constrained output

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
| GET | `/cron/cache` | Refresh Plex → Postgres cache (async; file lock) |
| GET | `/stats` | DB statistics |
| GET | `/health` | JSON health including DB ping |
| GET | `/metrics` | Prometheus exposition (otelhttp HTTP server metrics) |
| GET | `/static/*` | Embedded static files (e.g. favicon) |

## Environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `DATABASE_URL` | yes | Postgres connection string, e.g. `postgres://user:pass@host:5432/recommender?sslmode=disable` |
| `PLEX_URL` | yes | Plex server base URL |
| `PLEX_TOKEN` | yes | Plex token |
| `TMDB_API_KEY` | yes | TMDb API key |
| `GOOGLE_CLOUD_PROJECT` | yes | GCP project ID (Vertex AI API enabled) |
| `GOOGLE_CLOUD_LOCATION` | yes | Vertex AI region, e.g. `us-central1` |
| `GOOGLE_GENAI_USE_VERTEXAI` | no | `true` to use Vertex AI (recommended); the SDK also supports the Gemini Developer API |
| `GEMINI_MODEL` | no | Model ID (default `gemini-2.5-flash`) |
| `GOOGLE_APPLICATION_CREDENTIALS` | no | Path to a service-account key for local dev; production uses ambient ADC (workload identity) |
| `TRAKT_CLIENT_ID` | no | Trakt API app client id; enables Trakt signals |
| `TRAKT_CLIENT_SECRET` | no | Trakt API app client secret |
| `TRAKT_CONNECT_TOKEN` | no | Shared secret required to call `GET /trakt/connect?token=…`; the endpoint is disabled when unset |
| `ANILIST_USERNAME` | no | AniList username (public list); enables AniList signals |
| `PORT` | no | HTTP port (default `8080`) |
| `POSTER_DIR` | no | Directory for locally cached Plex posters (default `posters`; Docker Compose uses `/data/posters`) |

Authentication to Vertex AI uses [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials) — no API key. Locally, run `gcloud auth application-default login` or set `GOOGLE_APPLICATION_CREDENTIALS`.

### Signal sources (optional)

External sources only **re-rank titles you already own in Plex** — they never add new titles. Both are off unless configured, and are synced during `/cron/cache`.

- **Trakt** (watched / ratings / watchlist): register a Trakt API app, set `TRAKT_CLIENT_ID`/`TRAKT_CLIENT_SECRET` and a `TRAKT_CONNECT_TOKEN`, then authorize once — `curl "http://localhost:8080/trakt/connect?token=$TRAKT_CONNECT_TOKEN"` and enter the returned code at the Trakt URL. Tokens persist in the DB and auto-refresh.
- **AniList** (anime scores): set `ANILIST_USERNAME` (public list; no auth). Matched to owned anime by title + year.

Signals feed genre affinity, a watchlist score boost, watched-elsewhere handling, and a short "recently loved" line in the prompt.

## Repository layout

```
recommender/
├── handlers/          # HTTP handlers and HTML templates (embedded)
├── lib/
│   ├── db/           # Migrations and GORM logger
│   ├── health/       # Health check
│   ├── lock/         # File locks for cron endpoints
│   ├── plex/         # Plex client and cache update
│   ├── recommend/    # Gemini generation, candidate scoring, and queries
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
gcloud auth application-default login   # or set GOOGLE_APPLICATION_CREDENTIALS
export DATABASE_URL=postgres://recommender:recommender@localhost:5432/recommender?sslmode=disable
export PLEX_URL=... PLEX_TOKEN=... TMDB_API_KEY=...
export GOOGLE_GENAI_USE_VERTEXAI=true GOOGLE_CLOUD_PROJECT=... GOOGLE_CLOUD_LOCATION=us-central1
go run .
```

Optional: `POSTER_DIR=/path/to/posters`. Need a local Postgres? `docker compose up -d postgres`.

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

The compose file runs a bundled `postgres:17` service (data in the `pgdata` volume) and mounts `./data` at `/data` for cached posters (`POSTER_DIR=/data/posters`).

## Recommendation flow (summary)

1. **`/cron/cache`** — Reads Plex libraries and stores all movies and TV shows in Postgres, including `view_count`, GUIDs (imdb/tmdb/tvdb), and the full genre list. Poster thumbs are stored as absolute URLs when Plex returns relative paths.
2. **`/cron/recommend`** — Skips if a successful run already exists for the UTC day. Otherwise: loads cached titles (minus anything recommended in the last 30 days), scores them (rating + novelty + Plex-derived taste affinity), takes a date-seeded diverse shortlist, asks Gemini to pick the best fits **by ID** with a one-line reason, slots them deterministically (comedy / action-drama / rewatch / wildcard movies + unwatched TV), lazily fills any missing posters from TMDb, and **replaces** that day's rows in one transaction. Every attempt records a `GenerationRun`.

A day is "done" when a `GenerationRun` with status `ok` exists for it — tracked explicitly rather than inferred from row counts, so cron never re-runs a completed day.

## Security notes

- **`/cron/cache`, `/cron/recommend`, and `/metrics` are unauthenticated.** The cron endpoints trigger paid Gemini calls and full Plex/Trakt/AniList syncs, so anyone who can reach them can drive cost and load. Restrict `/cron/*` and `/metrics` to trusted callers at the ingress/reverse proxy (source-IP allow-list or an internal-only route); the app does not gate them itself.
- Cached-poster downloads only send the private `X-Plex-Token` to the configured Plex host, so an absolute thumb URL pointing off-host cannot exfiltrate the token or be used for SSRF with credentials.
- The GORM logger strips bound parameter values from query traces (via `gorm.ParamsFilter`), so SQL is logged with placeholders and secrets like Trakt tokens never land in logs.
