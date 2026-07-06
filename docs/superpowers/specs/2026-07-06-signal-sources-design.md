# External Signal Sources (Trakt + AniList) — Design

**Date:** 2026-07-06
**Status:** Approved (design). Ships as a **single PR** (Phases 3 + 4 of the overhaul roadmap).

## Problem

The recommender personalizes only from Plex watch counts + Plex ratings
(`genreAffinity` in `lib/recommend/profile.go`). Real taste lives in external
services — Trakt (watch history, ratings, watchlist) and AniList (anime scores).
Phase 1 added the plumbing (`ExternalSignal` table, `Movie`/`TVShow` IMDb/TMDb/
TVDb IDs) but nothing populates or consumes it yet.

## Goal

Feed Trakt and AniList signals into recommendations. **Signals only rank
Plex-owned titles** — every recommendation remains something you own. Both
sources are **optional**: unconfigured → skipped, the app runs unchanged.

## Non-goals

- No recommending titles you don't own (no discovery/where-to-watch).
- No storing signals that don't join to an owned Plex title.
- No AniList OAuth (public lists only).

## Architecture

```
/cron/cache  Plex upsert (existing)
           → SyncSignals(ctx): for each configured SignalSource, fetch + upsert
             ExternalSignal rows joined to Plex by TMDb→IMDb→TVDb (best-effort)

/cron/recommend  loadCandidates now folds in signals:
             - genre affinity blends Plex behavior + rated/score signals
             - watchlist boost on owned titles
             - Trakt-watched treated as watched (novelty/rewatch, TV exclusion)
           → prompt gets a short "recently loved" context line
```

### Components
- `lib/trakt/client.go` — Trakt HTTP client: OAuth device flow, token refresh,
  and the `sync/*` endpoints. Typed responses; no DB access.
- `lib/anilist/client.go` — AniList GraphQL client: `MediaListCollection` by
  username. Typed responses; no DB access.
- `lib/recommend/signals.go` — the `SignalSource` interface, the Trakt and
  AniList adapters (call the client, join to Plex, upsert `ExternalSignal`), and
  `SyncSignals(ctx)` which runs every configured source best-effort.
- `lib/recommend/profile.go` / `candidates.go` — extended to consume signals.

### SignalSource interface
```go
type SignalSource interface {
    Name() string
    Sync(ctx context.Context) (synced int, err error)
}
```
`SyncSignals` iterates the configured sources; a source error is logged and does
not fail the others or the cache job.

## Signal consumption

All signals reference an owned Plex row (`MovieID`/`TVShowID` set). Four effects:

1. **Genre affinity.** `genreAffinity` adds, per `rated`/`score` signal, a weight
   `Value/10` to the joined title's genres — blended with the existing Plex
   watch/rating weighting. Higher-rated titles pull their genres up.
2. **Watchlist boost.** `loadCandidates` marks candidates that have a
   `watchlist` signal; `scoreCandidate` adds a fixed boost so watchlisted owned
   titles rank higher.
3. **Trakt-watched = watched.** Titles with a `watched` signal are treated as
   watched even when Plex `ViewCount == 0`: movies lose the unwatched novelty
   boost and become rewatch-slot eligible; such TV shows are excluded from the TV
   candidate pool (which is otherwise `ViewCount == 0`).
4. **Prompt context.** A one-line "recently loved on Trakt/AniList: …" summary
   (top few highly-rated owned titles) is folded into the prompt next to the
   taste profile.

## Trakt

- **Auth:** OAuth device flow. Env `TRAKT_CLIENT_ID` / `TRAKT_CLIENT_SECRET`
  (unset → Trakt skipped). `GET /trakt/connect` starts the flow, returns the
  `user_code` + `verification_url`, and background-polls `oauth/device/token`
  until authorized. Tokens persist in a new `OAuthToken` row and refresh before
  each sync. `/trakt/connect` is unauthenticated; completing the flow still
  requires a Trakt login, so it cannot be hijacked (can be gated later if
  desired).
- **Sync:** `sync/watched/movies`, `sync/watched/shows`, `sync/ratings/movies`,
  `sync/ratings/shows`, `sync/watchlist/movies`, `sync/watchlist/shows`. Each
  item carries `ids.{trakt,imdb,tmdb,tvdb}`. Map to `ExternalSignal`:
  - watched → `Kind=watched`, `Value=1`
  - ratings → `Kind=rated`, `Value=<1..10>`
  - watchlist → `Kind=watchlist`, `Value=1`
  Join by TMDb→IMDb→TVDb; drop items not owned in Plex.

## AniList

- **Auth:** none. Env `ANILIST_USERNAME` (unset → AniList skipped).
- **Sync:** GraphQL `MediaListCollection(userName, type: ANIME)` → entries with
  `score` (normalized to 0..10) and media `{ idMal, title, seasonYear, format }`.
  Best-effort match to Plex by title + year (and IDs where available). Store
  matched entries as `ExternalSignal{Source:"anilist", Kind:score, Value:<0..10>}`;
  drop unmatched.

## Data model / config

- New `OAuthToken{ID, Source (unique), AccessToken, RefreshToken, ExpiresAt,
  UpdatedAt}` (Trakt only). Added to `AutoMigrate`.
- `ExternalSignal` reused unchanged.
- Env (all optional): `TRAKT_CLIENT_ID`, `TRAKT_CLIENT_SECRET`, `ANILIST_USERNAME`.
- `SourceTrakt = "trakt"`, `SourceAniList = "anilist"` constants added to `models`.

## Error handling

- Per-source sync isolated; failures logged, others continue, cache job succeeds.
- Trakt token refresh failure → log, skip Trakt this run (recommendations still
  generate from Plex + AniList + prior signals).
- Unmatched titles skipped silently (expected, not errors).

## Testing

- Trakt client: `httptest` for device-flow bootstrap, token refresh, and each
  `sync/*` endpoint.
- AniList client: `httptest` for the GraphQL response.
- Signal adapters: in-memory DB; feed fake client data; assert `ExternalSignal`
  rows are joined and upserted correctly, and unmatched items dropped.
- Consumption: affinity blends signal weight; watchlist boost reorders; a
  Trakt-`watched` movie loses novelty and a watched TV show leaves the pool; the
  prompt-context string includes loved titles.
- No network in the unit suite; sources gated off when env is unset.

## Risks / open questions

- **AniList matching accuracy.** Title+year matching for anime is imperfect
  (romaji vs english titles, season splits). Acceptable: unmatched entries are
  dropped, so the failure mode is "no signal," never a wrong signal. Log matched
  vs total counts.
- **Trakt device-flow bootstrap on a headless host.** `/trakt/connect` must be
  reachable once to authorize; the token then persists in the DB volume. If the
  DB is wiped, re-run `/trakt/connect`.
- **Signal weighting balance.** Blending Plex + Trakt + AniList affinity and the
  watchlist boost may need tuning; keep the boost/weights as named constants.
