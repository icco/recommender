# Recommender Overhaul — Design

**Date:** 2026-07-06
**Status:** Approved (design). Implementation plan covers Phases 0–2 in a single PR.

## Problem

`icco/recommender` produces daily movie/TV recommendations from a Plex library,
enriched with TMDb and chosen by OpenAI. In practice it does not work reliably.
The scaffolding is sound (Go, chi, GORM, SQLite, embedded templates, health,
metrics, graceful shutdown, Plex cache, TMDb client). The failure is concentrated
in `lib/recommend/recommend.go` (~880 lines). Concrete defects:

1. **TMDb is hammered at recommend time.** `GenerateRecommendations` loops over
   *every* cached movie and *every* unwatched TV show and calls
   `SearchMovie`/`SearchTVShow` for each — even when a TMDb ID is already known.
   On a real library this trips the TMDb circuit breaker and exhausts the 5-minute
   background budget. Then `formatContent` discards all but the first 50 items:
   hundreds of API calls to use 50.
2. **The model only ever sees the same 50 titles.** `formatContent` takes
   `items[:50]` in raw DB order — no ranking, no shuffle. Most of the library is
   invisible and results never vary day to day.
3. **Exact-title matching drops most results.** Output is matched via
   `contentMap[item.Title]`; any wording drift is silently discarded. The model's
   (often hallucinated) `tmdb_id` overwrites the correct stored one.
4. **Slot-filling is broken.** The "rewatch" slot never checks `ViewCount`; genre
   buckets substring-match a freeform string; it routinely yields fewer than 4
   movies, so `CheckRecommendationsComplete` never returns true — the home page
   404s and cron regenerates endlessly.
5. **Dead weight and broken promises.** The entire in-memory cache subsystem
   (`SetCache`/`GetCache`/`cleanupCache` + a background goroutine) is never called.
   Preferences are a hardcoded generic string. The prompt promises "nothing
   recommended in the last 30 days" but only yesterday's row is passed in.
6. **Plex data is under-read.** Only the first genre is stored (`item.Genre[0]`),
   and Plex GUIDs (`imdb://`, `tmdb://`, `tvdb://`) are not read at all — so every
   ID has to be re-derived by fragile title search.

## Goals

- **Reliable:** a run either succeeds and serves good recommendations, or fails
  cleanly. Never infinite-regenerate; never hard-404 the home page.
- **Better recommendations:** use the whole library, vary day to day, dedupe over
  30 days, show a reason per pick, and personalize from real viewing behavior.
- **Extensible sources:** external signals (Trakt, AniList, …) plug in behind one
  interface. **All recommendations remain titles already owned in Plex** — external
  sources are ranking/personalization signals, never catalog.

## Non-goals

- No stack change. Keep Go/chi/GORM/SQLite/embedded templates.
- No UI redesign. Minor template tweak to surface explanations only.
- No "discover new / where to watch" catalog. Recommendations are Plex-owned only.

## Architecture

The recommendation pipeline is split into phases so that **recommend time performs
no bulk external calls**. All enrichment happens at cache time.

```
/cron/cache   Plex library  → upsert rows (GUIDs, full genres, view counts)
                            → bounded, incremental TMDb enrichment (posters/missing ids)
                            → (Phase 3+) sync external signals

/cron/recommend  eligible pool = owned − last-30-days recs
              → score (rating + novelty + genre + taste affinity)
              → date-seeded shuffle → diverse shortlist (~80)
              → OpenAI (structured outputs, strict json_schema): pick IDs + reason
              → match by ID → deterministic slotting/validation in Go
              → lazy poster fill for the ~7 finalists only
              → persist recommendations + a GenerationRun row
```

### 1. Cache-time enrichment (replaces recommend-time TMDb loop)

- `/cron/cache` reads Plex GUIDs and stores `IMDbID` / `TMDbID` / `TVDbID`
  directly from Plex metadata. This removes the need for title-search enrichment
  in the common case and gives robust join keys for Trakt/AniList later.
- Store the **full** genre list, not just the first.
- TMDb becomes a **bounded, incremental fallback**: per cache run, fill missing
  posters/IDs for up to `N` un-enriched titles (oldest `EnrichedAt` first). This
  is idempotent and converges over a few runs, staying within the background
  timeout regardless of library size.
- Recommend time makes **at most ~7** TMDb calls: a lazy poster fill for the
  finalists if a poster is still missing.

### 2. Candidate selection in Go (whole library)

- Eligible pool: all owned titles of the requested type, minus any recommended in
  the last 30 days (single indexed DB query on `recommendations.date`).
- Cheap heuristic score per title: normalized rating + novelty (unwatched boost,
  or `ViewCount > 0` for the rewatch slot) + genre fit + (Phase 2) taste affinity.
- **Date-seeded shuffle** (seed derived from the UTC date) so the shortlist —
  and therefore the day's recommendations — varies deterministically per day
  without external randomness. This keeps runs reproducible and testable.
- Emit a diverse shortlist (target ~80 titles, spread across genres) to the model.

### 3. ID-based LLM contract (structured outputs)

- Each shortlist item is presented with a short numeric ID (the DB row ID).
- Use OpenAI **structured outputs** (`response_format` = `json_schema`, `strict:
  true`) so the response shape is guaranteed. The model returns chosen **IDs** and
  a one-line `explanation` per pick, grouped by requested role.
- Matching is by ID only. No fuzzy title matching. The model never supplies TMDb
  IDs, so it can't overwrite correct ones.
- Prompt shrinks to "choose the best fits from this shortlist and explain briefly,"
  which plays to the model's strengths and removes any reliance on it knowing the
  full library.

### 4. Deterministic slotting + validation (Go)

- Movie slots: comedy / action-drama / rewatch (`ViewCount > 0`) / wildcard.
- TV slots: 3 unwatched shows.
- Slotting and dedupe run in Go after ID matching. If the model under-delivers a
  role, fill deterministically from the ranked shortlist. A valid partial day is
  still served — the pipeline **never infinite-regenerates**.

### 5. Explicit generation state

- New `GenerationRun` row per run: `date`, `status`, per-type counts, `model`,
  `duration`, `error`. "Complete for today" means *a run succeeded today*, read
  from this table — not inferred from recommendation counts.
- Home page renders whatever recommendations exist with a friendly empty state
  when a day has none. No 404 loop.

## Data model changes

- `Movie` / `TVShow`: add `IMDbID string`, `TVDbID string` (keep `TMDbID *int`);
  store genres as a normalized multi-value (parsed list / join) instead of
  first-only; add `EnrichedAt time.Time`.
- New `GenerationRun`: `ID`, `Date`, `Status`, `MovieCount`, `TVShowCount`,
  `Model`, `DurationMS`, `Error`, `CreatedAt`.
- New `ExternalSignal` (used from Phase 2 on): `ID`, `Source`, `ExternalRef`,
  `MovieID *uint`, `TVShowID *uint`, `Kind` (watched/rated/watchlist/score),
  `Value float64`, `UpdatedAt`. Populated by a `SignalSource` interface so future
  sources (Trakt, AniList, Letterboxd) plug in uniformly.

GORM `AutoMigrate` handles additive columns/tables; no destructive migration.

## Cleanup

- Delete the dead in-memory cache subsystem (`CacheEntry`, `SetCache`, `GetCache`,
  `ClearCache`, `cleanupCache`, `startCacheCleanup`, `cacheMu`, `cache`).
- Remove the hardcoded `Preferences` string and `limitPreviousRecommendations`.
- Replace the JSON-object-plus-prompt-discipline contract with structured outputs.

## Phasing

Phases 0–2 ship in **one PR**. Phases 3–4 are follow-ons, each with its own plan.

- **Phase 0 — Delete cruft.** Remove the dead cache subsystem, hardcoded
  preferences, and `limitPreviousRecommendations`.
- **Phase 1 — Reliable core.** Plex GUID + full-genre extraction; cache-time
  incremental enrichment; Go candidate selection + date-seeded shuffle; ID-based
  structured-output contract; deterministic slotting; `GenerationRun`; 30-day
  dedupe; surface explanations in the template; tests with mocked OpenAI/TMDb.
- **Phase 2 — Taste profile from Plex signals.** Compute affinity from Plex
  `ViewCount` + Plex ratings; write `ExternalSignal` rows with `Source = "plex"`;
  replace the hardcoded preferences with a generated taste-profile block fed into
  both scoring and the prompt. No external API yet.
- **Phase 3 — Trakt (follow-on).** OAuth device flow; sync watched/ratings/
  watchlist; ID-join to Plex by imdb/tmdb/tvdb; affinity + watchlist boost.
- **Phase 4 — AniList (follow-on).** GraphQL sync of anime list/scores;
  best-effort match to Plex; anime affinity.

## Testing

Unit tests for the new core, following existing `lib/recommend/recommend_test.go`
and `lib/plex` test patterns:

- Candidate scoring and ranking (deterministic given a fixed date seed).
- Date-seeded shuffle: same date → same order; different date → different order.
- Slotting: correct roles filled; rewatch slot requires `ViewCount > 0`;
  graceful fill when the model under-delivers.
- 30-day dedupe excludes recently recommended titles.
- ID-based matching ignores unknown/hallucinated IDs.
- Signal aggregation → taste profile (Phase 2).

External services (OpenAI, TMDb, and later Trakt/AniList) are accessed through
interfaces and mocked in tests. No network calls in the unit suite.

## Risks / open questions

- **Structured outputs + model choice.** Current code uses `openai.GPT5Mini` with a
  JSON-object response format. The plan switches to strict `json_schema`; confirm
  the chosen model + `go-openai` version support strict structured outputs, else
  fall back to json-object + validation with a repair retry.
- **Cache enrichment budget.** Bounded per-run enrichment (`N` titles) trades
  first-run completeness for reliability; a large library takes several cache runs
  to fully enrich. Acceptable — recommend time no longer depends on full
  enrichment (lazy finalist fill covers gaps).
- **Genre normalization.** Deciding between a parsed multi-value column and a join
  table; the parsed-list approach is simpler and sufficient for genre-fit scoring.
