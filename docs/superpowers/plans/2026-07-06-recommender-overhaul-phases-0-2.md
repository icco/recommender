# Recommender Overhaul (Phases 0–2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the daily Plex recommender actually work — reliable generation, whole-library candidate selection with daily variety, an ID-based Gemini contract, deterministic slotting, explicit run state, and a Plex-derived taste profile — all in one PR.

**Architecture:** Keep the Go/chi/GORM/SQLite/templates scaffolding. Rewrite `lib/recommend` into focused files. Recommend-time performs no bulk external calls: Plex GUIDs + genres are captured at cache time; candidates are scored/shuffled in Go; Gemini (on Vertex AI) only picks IDs from a shortlist and explains them; slotting/validation happen deterministically in Go; a `GenerationRun` row records success.

**Tech Stack:** Go 1.26, chi v5, GORM + SQLite, `google.golang.org/genai` (Gemini/Vertex AI), TMDb (fallback only), zap + gutil/logging, OpenTelemetry.

## Global Constraints

- Go module: `github.com/icco/recommender`; Go version `go 1.26.2` (from `go.mod`).
- **Recommendations are Plex-owned titles only.** External sources are signals, never catalog.
- LLM provider is **Gemini on Vertex AI** via `google.golang.org/genai`, `Backend: genai.BackendVertexAI`. Remove all `github.com/sashabaranov/go-openai` usage.
- Env vars: add `GOOGLE_GENAI_USE_VERTEXAI`, `GOOGLE_CLOUD_PROJECT` (required), `GOOGLE_CLOUD_LOCATION` (required), optional `GEMINI_MODEL` (default `gemini-2.5-flash`); remove `OPENAI_API_KEY`. Keep `PLEX_URL`, `PLEX_TOKEN`, `TMDB_API_KEY`, `PORT`, `DB_PATH`.
- Recommendation record type constants: `models.TypeMovie = "movie"`, `models.TypeTVShow = "tvshow"`.
- GORM SQLite column naming: `TMDbID`→`tm_db_id`, so new `IMDbID`→`im_db_id`, `TVDbID`→`tv_db_id`, `EnrichedAt`→`enriched_at`, `Explanation`→`explanation`.
- Loggers come from `logging.FromContext(ctx)` (gutil). No `log/slog`.
- Dates are UTC midnight: `time.Now().UTC().Truncate(24 * time.Hour)`; rows store that instant in `date`.
- Branch: `overhaul-recommend-core`. Never commit to `main`. Never force-push.
- Every task ends green: `go build ./... && go test ./... && gofmt -l .` (expect no output from gofmt).
- Commit messages end with: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`. Do **not** add a "Generated with Claude Code" footer.

---

## File Structure

**New files:**
- `lib/recommend/generate.go` — `GenerateRecommendations` orchestration + run recording.
- `lib/recommend/candidates.go` — candidate loading, scoring, date-seeded shuffle, shortlist formatting.
- `lib/recommend/slotting.go` — Gemini response parsing, ID matching, deterministic slot selection.
- `lib/recommend/llm.go` — `Chatter` interface + Gemini/Vertex concrete client.
- `lib/recommend/profile.go` — Plex-signal taste profile (Phase 2).
- `lib/recommend/candidates_test.go`, `slotting_test.go`, `generate_test.go`, `profile_test.go`.
- `lib/plex/guid.go` + `lib/plex/guid_test.go` — GUID parsing helpers.

**Modified files:**
- `lib/recommend/recommend.go` — strip dead cache + hardcoded prefs; keep queries/stats.
- `lib/recommend/recommend_test.go` — drop `cache` field from `testRecommender`.
- `models/models.go` — new fields + `GenerationRun`, `ExternalSignal`.
- `lib/db/migrations.go` — AutoMigrate new models.
- `lib/plex/plexgo_convert.go` — decode `Guid`; request `includeGuids=1`.
- `lib/plex/client.go` — store GUIDs, joined genres, `EnrichedAt` in upserts.
- `handlers/handlers.go` — completeness via `GenerationRun`.
- `handlers/templates/home.html` — render explanations.
- `main.go` — Gemini env validation + client wiring.
- `go.mod` / `go.sum` — add `google.golang.org/genai`, remove `go-openai`.
- `README.md`, `template.env`, `docker-compose.yml`, `CLAUDE.md` — env var changes.

---

## Task 0: Delete dead code (Phase 0)

Removes the never-called in-memory cache subsystem, the hardcoded preferences string, and `limitPreviousRecommendations`. Pure deletion; nothing depends on these.

**Files:**
- Modify: `lib/recommend/recommend.go`
- Modify: `lib/recommend/recommend_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `Recommender` struct with fields `db *gorm.DB`, `plex *plex.Client`, `tmdb *tmdb.Client` (LLM field added in Task 3). No `cache`/`cacheMu`.

- [ ] **Step 1: Delete cache members from the struct**

In `lib/recommend/recommend.go`, edit the `Recommender` struct to exactly:

```go
type Recommender struct {
	db   *gorm.DB
	plex *plex.Client
	tmdb *tmdb.Client
}
```

Delete the `CacheEntry` type, and the methods `startCacheCleanup`, `cleanupCache`, `SetCache`, `GetCache`, `ClearCache`. Delete the `go r.startCacheCleanup(context.Background())` line and the `cache: make(...)` initializer in `New`. Delete now-unused imports (`sync`; keep `context` — still used elsewhere).

- [ ] **Step 2: Delete hardcoded preferences and `limitPreviousRecommendations`**

In `GenerateRecommendations`, delete the `Preferences: "User enjoys a mix..."` literal (leave `Preferences` empty for now — Task 8 fills it). Delete the `limitPreviousRecommendations` method and its call site (Task 6 replaces prev-rec handling with 30-day dedupe; for now delete the `prevRecs = r.limitPreviousRecommendations(prevRecs)` line only if it blocks compilation — otherwise leave the surrounding block, Task 6 rewrites this function wholesale).

- [ ] **Step 3: Fix the test helper**

In `lib/recommend/recommend_test.go`, change `testRecommender` to:

```go
func testRecommender(db *gorm.DB) *Recommender {
	return &Recommender{db: db}
}
```

- [ ] **Step 4: Build and test**

Run: `go build ./... && go test ./lib/recommend/... -v`
Expected: PASS (existing date/pagination/exist tests still pass).

- [ ] **Step 5: Commit**

```bash
git add lib/recommend/recommend.go lib/recommend/recommend_test.go
git commit -m "refactor: remove dead in-memory cache and hardcoded preferences

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 1: Data model + migrations

Adds enrichment fields, an explanation column, and two new tables. GORM `AutoMigrate` adds columns/tables non-destructively.

**Files:**
- Modify: `models/models.go`
- Modify: `lib/db/migrations.go`
- Test: `lib/db/migrations_test.go` (create)

**Interfaces:**
- Produces:
  - `models.Movie`/`models.TVShow` gain `IMDbID string`, `TVDbID string`, `EnrichedAt *time.Time`.
  - `models.Recommendation` gains `Explanation string`.
  - `models.GenerationRun{ID uint; Date time.Time; Status string; MovieCount int; TVShowCount int; Model string; DurationMS int64; Error string; CreatedAt time.Time}` with `Status` constants `models.RunStatusOK = "ok"`, `models.RunStatusError = "error"`.
  - `models.ExternalSignal{ID uint; Source string; ExternalRef string; MovieID *uint; TVShowID *uint; Kind string; Value float64; UpdatedAt time.Time}` with `models.SignalKindWatched/Rated/Watchlist/Score` and `models.SourcePlex = "plex"`.

- [ ] **Step 1: Add fields to Movie and TVShow**

In `models/models.go`, add to the `Movie` struct (after `TMDbID`):

```go
	IMDbID     string     `gorm:"type:varchar(32);index:idx_movies_imdb_id"` // Plex GUID imdb://
	TVDbID     string     `gorm:"type:varchar(32)"`                          // Plex GUID tvdb://
	EnrichedAt *time.Time `gorm:"index:idx_movies_enriched_at"`              // last TMDb enrichment; nil = never
```

Add the identical three fields to `TVShow` (indexes `idx_tvshows_imdb_id`, `idx_tvshows_enriched_at`).

- [ ] **Step 2: Add Explanation to Recommendation**

In the `Recommendation` struct add (after `PosterURL`):

```go
	Explanation string `gorm:"type:varchar(1000)"` // model's one-line reason for this pick
```

- [ ] **Step 3: Add the new models and constants**

Append to `models/models.go`:

```go
// Run status values for GenerationRun.Status.
const (
	RunStatusOK    = "ok"
	RunStatusError = "error"
)

// Signal source + kind values for ExternalSignal.
const (
	SourcePlex        = "plex"
	SignalKindWatched = "watched"
	SignalKindRated   = "rated"
	SignalKindScore   = "score"
	SignalKindWatchlist = "watchlist"
)

// GenerationRun records one recommendation-generation attempt for a day.
type GenerationRun struct {
	ID          uint      `gorm:"primarykey"`
	Date        time.Time `gorm:"not null;index:idx_generation_runs_date"` // UTC midnight of the target day
	Status      string    `gorm:"type:varchar(20);not null"`               // "ok" or "error"
	MovieCount  int       `gorm:"default:0"`
	TVShowCount int       `gorm:"default:0"`
	Model       string    `gorm:"type:varchar(64)"`
	DurationMS  int64     `gorm:"default:0"`
	Error       string    `gorm:"type:varchar(1000)"`
	CreatedAt   time.Time
}

// ExternalSignal is a per-title or per-user signal from a source (Plex, Trakt, …)
// used to personalize scoring. Recommendations remain Plex-owned; signals only rank.
type ExternalSignal struct {
	ID          uint    `gorm:"primarykey"`
	Source      string  `gorm:"type:varchar(32);not null;uniqueIndex:idx_signal_unique"`
	ExternalRef string  `gorm:"type:varchar(128);uniqueIndex:idx_signal_unique"` // e.g. imdb id or "genre:Comedy"
	Kind        string  `gorm:"type:varchar(20);not null;uniqueIndex:idx_signal_unique"`
	MovieID     *uint   `gorm:"index"`
	TVShowID    *uint   `gorm:"index"`
	Value       float64 `gorm:"default:0"`
	UpdatedAt   time.Time
}
```

- [ ] **Step 4: Register the new models in AutoMigrate**

In `lib/db/migrations.go`, change the `AutoMigrate` call in `RunMigrations` to:

```go
	if err := db.WithContext(ctx).AutoMigrate(
		&models.Movie{}, &models.TVShow{}, &models.Recommendation{},
		&models.GenerationRun{}, &models.ExternalSignal{},
	); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}
```

- [ ] **Step 5: Write the migration test**

Create `lib/db/migrations_test.go`:

```go
package db

import (
	"testing"
	"time"

	"github.com/icco/recommender/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRunMigrations_createsNewTables(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(t.Context(), gdb); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	if !gdb.Migrator().HasTable(&models.GenerationRun{}) {
		t.Fatal("generation_runs table missing")
	}
	if !gdb.Migrator().HasTable(&models.ExternalSignal{}) {
		t.Fatal("external_signals table missing")
	}
	run := models.GenerationRun{Date: time.Now().UTC().Truncate(24 * time.Hour), Status: models.RunStatusOK, MovieCount: 4}
	if err := gdb.Create(&run).Error; err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.ID == 0 {
		t.Fatal("expected assigned ID")
	}
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./lib/db/... -run TestRunMigrations_createsNewTables -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add models/models.go lib/db/migrations.go lib/db/migrations_test.go
git commit -m "feat: add enrichment fields, GenerationRun and ExternalSignal models

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Capture Plex GUIDs + full genres at cache time

Reads `Guid[]` from Plex, parses `imdb://`/`tmdb://`/`tvdb://`, stores them plus the full genre list and `EnrichedAt` on cache upsert. This gives robust IDs and removes the recommend-time TMDb loop's reason to exist.

**Files:**
- Create: `lib/plex/guid.go`, `lib/plex/guid_test.go`
- Modify: `lib/plex/plexgo_convert.go`
- Modify: `lib/plex/client.go`

**Interfaces:**
- Produces:
  - `plex.parseGUIDs(guids []string) (imdb string, tmdb *int, tvdb string)` — parses Plex GUID URIs.
  - `plex.joinGenres(tags []components.Tag) string` — comma-joined, de-duplicated genre string.
  - `Item` gains `Guids []string`.

- [ ] **Step 1: Write the GUID parser test**

Create `lib/plex/guid_test.go`:

```go
package plex

import "testing"

func TestParseGUIDs(t *testing.T) {
	imdb, tmdb, tvdb := parseGUIDs([]string{
		"imdb://tt0133093",
		"tmdb://603",
		"tvdb://12345",
	})
	if imdb != "tt0133093" {
		t.Errorf("imdb = %q, want tt0133093", imdb)
	}
	if tmdb == nil || *tmdb != 603 {
		t.Errorf("tmdb = %v, want 603", tmdb)
	}
	if tvdb != "12345" {
		t.Errorf("tvdb = %q, want 12345", tvdb)
	}
}

func TestParseGUIDs_empty(t *testing.T) {
	imdb, tmdb, tvdb := parseGUIDs(nil)
	if imdb != "" || tmdb != nil || tvdb != "" {
		t.Errorf("expected zero values, got %q %v %q", imdb, tmdb, tvdb)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/plex/... -run TestParseGUIDs -v`
Expected: FAIL (`undefined: parseGUIDs`).

- [ ] **Step 3: Implement the parser**

Create `lib/plex/guid.go`:

```go
package plex

import (
	"strconv"
	"strings"

	"github.com/LukeHagar/plexgo/models/components"
)

// parseGUIDs extracts imdb/tmdb/tvdb identifiers from Plex GUID URIs like
// "imdb://tt0133093", "tmdb://603", "tvdb://12345". Unknown schemes are ignored.
func parseGUIDs(guids []string) (imdb string, tmdb *int, tvdb string) {
	for _, g := range guids {
		switch {
		case strings.HasPrefix(g, "imdb://"):
			imdb = strings.TrimPrefix(g, "imdb://")
		case strings.HasPrefix(g, "tmdb://"):
			if n, err := strconv.Atoi(strings.TrimPrefix(g, "tmdb://")); err == nil {
				tmdb = &n
			}
		case strings.HasPrefix(g, "tvdb://"):
			tvdb = strings.TrimPrefix(g, "tvdb://")
		}
	}
	return imdb, tmdb, tvdb
}

// joinGenres returns a comma-separated, order-preserving, de-duplicated list of
// genre tags. Empty when there are none.
func joinGenres(tags []components.Tag) string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if t.Tag == "" {
			continue
		}
		if _, ok := seen[t.Tag]; ok {
			continue
		}
		seen[t.Tag] = struct{}{}
		out = append(out, t.Tag)
	}
	return strings.Join(out, ", ")
}
```

- [ ] **Step 4: Add a joinGenres test**

Append to `lib/plex/guid_test.go`:

```go
import ... // add "github.com/LukeHagar/plexgo/models/components" to the import block

func TestJoinGenres(t *testing.T) {
	got := joinGenres([]components.Tag{{Tag: "Comedy"}, {Tag: "Drama"}, {Tag: "Comedy"}})
	if got != "Comedy, Drama" {
		t.Errorf("joinGenres = %q, want %q", got, "Comedy, Drama")
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./lib/plex/... -run 'TestParseGUIDs|TestJoinGenres' -v`
Expected: PASS.

- [ ] **Step 6: Decode Guid from Plex and request it**

In `lib/plex/plexgo_convert.go`, add to `sectionListMetadata`:

```go
	Guid []struct {
		ID string `json:"id"`
	} `json:"Guid,omitempty"`
```

Add `Guids []string` to the `Item` struct in `lib/plex/client.go` (after `Genre`). In `sectionMetadataToPlexItem`, before the `return Item{...}`, build the slice and set it:

```go
	var guids []string
	for _, g := range md.Guid {
		if g.ID != "" {
			guids = append(guids, g.ID)
		}
	}
```

Add `Guids: guids,` to the returned `Item` literal. In `listSectionContentAll`, add `q.Set("includeGuids", "1")` next to the other `q.Set(...)` calls so Plex returns the `Guid` array.

- [ ] **Step 7: Persist GUIDs, joined genres, and EnrichedAt in upserts**

In `lib/plex/client.go`, update `movieUpsertColumns` and `tvUpsertColumns` to include the new columns:

```go
var movieUpsertColumns = []string{
	titleKey, "year", "rating", "genre", "poster_url", "runtime",
	"tm_db_id", "im_db_id", "tv_db_id", "enriched_at", "view_count", "updated_at",
}

var tvUpsertColumns = []string{
	titleKey, "year", "rating", "genre", "poster_url", "seasons",
	"tm_db_id", "im_db_id", "tv_db_id", "enriched_at", "view_count", "updated_at",
}
```

In `upsertMovieBatch`, replace the `genre := ...` block with `genre := joinGenres(item.Genre)`, and before building the `models.Movie` literal add:

```go
			imdb, tmdbID, tvdb := parseGUIDs(item.Guids)
			var enrichedAt *time.Time
			if tmdbID != nil || imdb != "" {
				enrichedAt = &now
			}
```

Set these on the literal: `TMDbID: tmdbID, IMDbID: imdb, TVDbID: tvdb, EnrichedAt: enrichedAt,` (replace the existing `TMDbID: nil,`). Apply the identical changes in `upsertTVShowBatch` (using `joinGenres`, `parseGUIDs`, and the same fields).

- [ ] **Step 8: Build and run the full plex suite**

Run: `go build ./... && go test ./lib/plex/... -v`
Expected: PASS (existing `client_test.go`/`client_cache_test.go` still pass; new GUID tests pass).

- [ ] **Step 9: Commit**

```bash
git add lib/plex/guid.go lib/plex/guid_test.go lib/plex/plexgo_convert.go lib/plex/client.go
git commit -m "feat(plex): capture GUIDs and full genre list at cache time

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Gemini/Vertex LLM client behind a Chatter interface

Introduces the `Chatter` interface (mockable) and the Gemini concrete client, swaps the dependency, and wires `New` + `main.go` env validation.

**Files:**
- Create: `lib/recommend/llm.go`
- Modify: `lib/recommend/recommend.go` (New signature + `chat` field)
- Modify: `main.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces:
  - `type Chatter interface { Complete(ctx context.Context, system, user string, schema *genai.Schema) (string, error) }`
  - `NewGeminiChatter(ctx context.Context, model string) (*GeminiChatter, error)`
  - `Recommender` gains field `chat Chatter`; `New(db, plex, tmdb, chat)` signature.

- [ ] **Step 1: Add the genai dependency, remove go-openai**

Run:

```bash
go get google.golang.org/genai@latest
go mod tidy
```

Expected: `google.golang.org/genai` appears in `go.mod`; `github.com/sashabaranov/go-openai` is removed once its last import is gone (Task 6 removes the final import — until then `go mod tidy` may keep it; that's fine).

- [ ] **Step 2: Implement the Chatter interface + Gemini client**

Create `lib/recommend/llm.go`:

```go
package recommend

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/genai"
)

// Chatter is the minimal LLM surface the recommender needs: given a system and
// user prompt plus a JSON response schema, return the model's JSON text.
// Implemented by GeminiChatter; faked in tests.
type Chatter interface {
	Complete(ctx context.Context, system, user string, schema *genai.Schema) (string, error)
}

// GeminiChatter calls Gemini on Vertex AI via the unified google.golang.org/genai SDK.
type GeminiChatter struct {
	client *genai.Client
	model  string
}

// NewGeminiChatter builds a Vertex AI-backed client from ADC. Project and
// location come from GOOGLE_CLOUD_PROJECT / GOOGLE_CLOUD_LOCATION.
func NewGeminiChatter(ctx context.Context, model string) (*GeminiChatter, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  os.Getenv("GOOGLE_CLOUD_PROJECT"),
		Location: os.Getenv("GOOGLE_CLOUD_LOCATION"),
	})
	if err != nil {
		return nil, fmt.Errorf("create genai client: %w", err)
	}
	return &GeminiChatter{client: client, model: model}, nil
}

// Complete sends the prompts with JSON-constrained output and returns the raw JSON text.
func (g *GeminiChatter) Complete(ctx context.Context, system, user string, schema *genai.Schema) (string, error) {
	cfg := &genai.GenerateContentConfig{
		ResponseMIMEType:  "application/json",
		ResponseSchema:    schema,
		SystemInstruction: genai.NewContentFromText(system, genai.RoleUser),
	}
	resp, err := g.client.Models.GenerateContent(ctx, g.model, genai.Text(user), cfg)
	if err != nil {
		return "", fmt.Errorf("gemini generate: %w", err)
	}
	return resp.Text(), nil
}
```

> If the installed SDK version rejects `ResponseSchema` for a field, drop `ResponseSchema` from `cfg` and rely on `ResponseMIMEType` + the Go-side validation added in Task 5 (the parser already ignores unknown fields and bad IDs). Note this in the commit if you hit it.

- [ ] **Step 3: Update the Recommender struct and New**

In `lib/recommend/recommend.go` set the struct and constructor:

```go
type Recommender struct {
	db   *gorm.DB
	plex *plex.Client
	tmdb *tmdb.Client
	chat Chatter
}

// New creates a Recommender. chat is the LLM backend (Gemini on Vertex AI in prod).
func New(db *gorm.DB, plexClient *plex.Client, tmdbClient *tmdb.Client, chat Chatter) (*Recommender, error) {
	return &Recommender{db: db, plex: plexClient, tmdb: tmdbClient, chat: chat}, nil
}
```

Remove the OpenAI client construction and the `openai`/`net/http`/`os`/`math` imports if they are now unused in `recommend.go` (Task 6 owns the generation code that used them; if it still lives in `recommend.go` at this point, leave imports until Task 6 moves it). Keep the file compiling.

- [ ] **Step 4: Wire main.go env validation + client**

In `main.go`, replace the `OPENAI_API_KEY` check block with:

```go
	if os.Getenv("GOOGLE_CLOUD_PROJECT") == "" {
		log.Fatalw("GOOGLE_CLOUD_PROJECT environment variable is required")
	}
	if os.Getenv("GOOGLE_CLOUD_LOCATION") == "" {
		log.Fatalw("GOOGLE_CLOUD_LOCATION environment variable is required")
	}

	geminiModel := os.Getenv("GEMINI_MODEL")
	if geminiModel == "" {
		geminiModel = "gemini-2.5-flash"
	}
	chat, err := recommend.NewGeminiChatter(ctx, geminiModel)
	if err != nil {
		log.Fatalw("Failed to create Gemini client", zap.Error(err))
	}
```

Change the recommender construction to `recommender, err := recommend.New(gormDB, plexClient, tmdbClient, chat)`.

- [ ] **Step 5: Build**

Run: `go build ./...`
Expected: success (there may be an unused-var/import error inside the not-yet-rewritten generation code; if so, comment out the `GenerateRecommendations` body's OpenAI call temporarily with a `return fmt.Errorf("not yet implemented")` — Task 6 replaces it. Prefer to proceed straight to Task 6 if the two are hard to separate).

- [ ] **Step 6: Commit**

```bash
git add lib/recommend/llm.go lib/recommend/recommend.go main.go go.mod go.sum
git commit -m "feat: add Gemini/Vertex Chatter and wire it into New/main

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Candidate selection, scoring, and date-seeded shuffle

Pure, deterministic functions that turn the cached library into a scored, day-varying shortlist. No external calls; fully unit-tested.

**Files:**
- Create: `lib/recommend/candidates.go`, `lib/recommend/candidates_test.go`

**Interfaces:**
- Produces:
  - `type candidate struct { ID uint; Type string; Title string; Year int; Rating float64; Genres []string; PosterURL string; Runtime int; ViewCount int; TMDbID *int; Affinity float64 }`
  - `func dateSeed(date time.Time) int64`
  - `func scoreCandidate(c candidate) float64`
  - `func buildShortlist(cands []candidate, date time.Time, poolSize, shortlistSize int) []candidate`
  - `func formatShortlist(cands []candidate) string`
  - `func (r *Recommender) loadCandidates(ctx context.Context, date time.Time) (movies, tvshows []candidate, err error)`
- Consumes: `models.Movie`, `models.TVShow`, `models.Recommendation` (Task 1 fields).

- [ ] **Step 1: Write the deterministic-behavior tests**

Create `lib/recommend/candidates_test.go`:

```go
package recommend

import (
	"testing"
	"time"
)

func mkCand(id uint, rating float64, view int, genres ...string) candidate {
	return candidate{ID: id, Type: "movie", Title: "T", Rating: rating, ViewCount: view, Genres: genres}
}

func TestScoreCandidate_ratingAndNovelty(t *testing.T) {
	unwatched := scoreCandidate(mkCand(1, 8.0, 0))
	watched := scoreCandidate(mkCand(2, 8.0, 3))
	if unwatched <= watched {
		t.Errorf("unwatched (%.2f) should outscore watched (%.2f)", unwatched, watched)
	}
	high := scoreCandidate(mkCand(3, 9.0, 0))
	low := scoreCandidate(mkCand(4, 4.0, 0))
	if high <= low {
		t.Errorf("higher rating should score higher: %.2f vs %.2f", high, low)
	}
}

func TestDateSeed_stableAndDistinct(t *testing.T) {
	d1 := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	d1b := time.Date(2026, 7, 6, 13, 0, 0, 0, time.UTC) // same calendar day
	d2 := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	if dateSeed(d1) != dateSeed(d1b) {
		t.Error("same UTC day must yield same seed")
	}
	if dateSeed(d1) == dateSeed(d2) {
		t.Error("different days must yield different seeds")
	}
}

func TestBuildShortlist_deterministicPerDayAndVaries(t *testing.T) {
	var cands []candidate
	for i := uint(1); i <= 200; i++ {
		cands = append(cands, mkCand(i, 5.0+float64(i%5), 0))
	}
	d1 := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)

	a := buildShortlist(cands, d1, 120, 40)
	b := buildShortlist(cands, d1, 120, 40)
	if len(a) != 40 {
		t.Fatalf("shortlist len = %d, want 40", len(a))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatal("same day must produce identical shortlist")
		}
	}
	c := buildShortlist(cands, d2, 120, 40)
	same := true
	for i := range a {
		if a[i].ID != c[i].ID {
			same = false
			break
		}
	}
	if same {
		t.Error("different days should produce a different order")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lib/recommend/... -run 'TestScoreCandidate|TestDateSeed|TestBuildShortlist' -v`
Expected: FAIL (undefined identifiers).

- [ ] **Step 3: Implement candidates.go**

Create `lib/recommend/candidates.go`:

```go
package recommend

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/icco/recommender/models"
)

// candidate is a Plex-owned title eligible for recommendation, with a computed score.
type candidate struct {
	ID        uint
	Type      string
	Title     string
	Year      int
	Rating    float64
	Genres    []string
	PosterURL string
	Runtime   int // minutes (movie) or seasons (tv)
	ViewCount int
	TMDbID    *int
	Affinity  float64 // taste-profile boost (Phase 2); 0 otherwise
}

// dateSeed derives a stable per-UTC-day seed so shortlists are reproducible.
func dateSeed(date time.Time) int64 {
	y, m, d := date.UTC().Date()
	return int64(y)*10000 + int64(m)*100 + int64(d)
}

// scoreCandidate ranks a title: rating drives it, unwatched gets a novelty
// boost, taste affinity adds on top.
func scoreCandidate(c candidate) float64 {
	s := c.Rating / 10.0 * 2.0
	if c.ViewCount == 0 {
		s += 1.0
	}
	s += c.Affinity
	return s
}

// buildShortlist keeps the best poolSize titles by score, then applies a
// date-seeded shuffle and returns the first shortlistSize. This yields quality
// (only good titles) plus daily variety, deterministically.
func buildShortlist(cands []candidate, date time.Time, poolSize, shortlistSize int) []candidate {
	sorted := make([]candidate, len(cands))
	copy(sorted, cands)
	sort.SliceStable(sorted, func(i, j int) bool {
		si, sj := scoreCandidate(sorted[i]), scoreCandidate(sorted[j])
		if si == sj {
			return sorted[i].ID < sorted[j].ID // stable tie-break
		}
		return si > sj
	})
	if poolSize < len(sorted) {
		sorted = sorted[:poolSize]
	}
	rng := rand.New(rand.NewSource(dateSeed(date)))
	rng.Shuffle(len(sorted), func(i, j int) { sorted[i], sorted[j] = sorted[j], sorted[i] })
	if shortlistSize < len(sorted) {
		sorted = sorted[:shortlistSize]
	}
	return sorted
}

// formatShortlist renders candidates for the prompt, keyed by DB ID so the model
// returns IDs (never titles).
func formatShortlist(cands []candidate) string {
	var b strings.Builder
	for _, c := range cands {
		watched := "unwatched"
		if c.ViewCount > 0 {
			watched = "watched"
		}
		fmt.Fprintf(&b, "[id=%d] %s (%d) — Rating: %.1f — Genres: %s — %s\n",
			c.ID, c.Title, c.Year, c.Rating, strings.Join(c.Genres, ", "), watched)
	}
	return b.String()
}

// loadCandidates loads eligible movies and TV shows, excluding titles recommended
// in the last 30 days. TV is restricted to unwatched shows.
func (r *Recommender) loadCandidates(ctx context.Context, date time.Time) (movies, tvshows []candidate, err error) {
	excludeMovies, excludeTV, err := r.recentlyRecommendedIDs(ctx, date, 30)
	if err != nil {
		return nil, nil, err
	}

	var dbMovies []models.Movie
	if err := r.db.WithContext(ctx).Find(&dbMovies).Error; err != nil {
		return nil, nil, fmt.Errorf("load movies: %w", err)
	}
	for _, m := range dbMovies {
		if _, skip := excludeMovies[m.ID]; skip {
			continue
		}
		movies = append(movies, candidate{
			ID: m.ID, Type: models.TypeMovie, Title: m.Title, Year: m.Year,
			Rating: m.Rating, Genres: splitGenres(m.Genre), PosterURL: m.PosterURL,
			Runtime: m.Runtime, ViewCount: m.ViewCount, TMDbID: m.TMDbID,
		})
	}

	var dbShows []models.TVShow
	if err := r.db.WithContext(ctx).Where("view_count = 0").Find(&dbShows).Error; err != nil {
		return nil, nil, fmt.Errorf("load tv shows: %w", err)
	}
	for _, s := range dbShows {
		if _, skip := excludeTV[s.ID]; skip {
			continue
		}
		tvshows = append(tvshows, candidate{
			ID: s.ID, Type: models.TypeTVShow, Title: s.Title, Year: s.Year,
			Rating: s.Rating, Genres: splitGenres(s.Genre), PosterURL: s.PosterURL,
			Runtime: s.Seasons, ViewCount: s.ViewCount, TMDbID: s.TMDbID,
		})
	}
	return movies, tvshows, nil
}

// recentlyRecommendedIDs returns Movie/TVShow IDs recommended within the last `days` days.
func (r *Recommender) recentlyRecommendedIDs(ctx context.Context, date time.Time, days int) (map[uint]struct{}, map[uint]struct{}, error) {
	cutoff := date.AddDate(0, 0, -days)
	var recs []models.Recommendation
	if err := r.db.WithContext(ctx).
		Where(`"date" >= ? AND "date" <= ?`, cutoff, date).
		Find(&recs).Error; err != nil {
		return nil, nil, fmt.Errorf("load recent recommendations: %w", err)
	}
	m := make(map[uint]struct{})
	tv := make(map[uint]struct{})
	for _, rec := range recs {
		if rec.MovieID != nil {
			m[*rec.MovieID] = struct{}{}
		}
		if rec.TVShowID != nil {
			tv[*rec.TVShowID] = struct{}{}
		}
	}
	return m, tv, nil
}

// splitGenres parses the comma-joined genre column into a slice.
func splitGenres(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./lib/recommend/... -run 'TestScoreCandidate|TestDateSeed|TestBuildShortlist' -v`
Expected: PASS.

- [ ] **Step 5: Add a dedupe DB test**

Append to `lib/recommend/candidates_test.go`:

```go
import (
	// add:
	"github.com/icco/recommender/models"
)

func TestLoadCandidates_excludesRecentAndWatchedTV(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := t.Context()
	today := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)

	m1 := models.Movie{Title: "Keep", Year: 2000, Rating: 8, PlexRatingKey: "k1"}
	m2 := models.Movie{Title: "RecentlyRecd", Year: 2001, Rating: 8, PlexRatingKey: "k2"}
	if err := db.Create(&m1).Error; err != nil { t.Fatal(err) }
	if err := db.Create(&m2).Error; err != nil { t.Fatal(err) }
	watched := models.TVShow{Title: "Seen", Year: 2010, ViewCount: 5, PlexRatingKey: "t1"}
	unwatched := models.TVShow{Title: "Fresh", Year: 2011, ViewCount: 0, PlexRatingKey: "t2"}
	if err := db.Create(&watched).Error; err != nil { t.Fatal(err) }
	if err := db.Create(&unwatched).Error; err != nil { t.Fatal(err) }

	rec := models.Recommendation{Date: today.AddDate(0, 0, -3), Title: "RecentlyRecd", Type: models.TypeMovie, Year: 2001, MovieID: &m2.ID, TMDbID: 1}
	if err := db.Create(&rec).Error; err != nil { t.Fatal(err) }

	movies, tv, err := r.loadCandidates(ctx, today)
	if err != nil { t.Fatal(err) }
	if len(movies) != 1 || movies[0].Title != "Keep" {
		t.Errorf("movies = %+v, want only Keep", movies)
	}
	if len(tv) != 1 || tv[0].Title != "Fresh" {
		t.Errorf("tv = %+v, want only Fresh", tv)
	}
}
```

- [ ] **Step 6: Run and verify**

Run: `go test ./lib/recommend/... -run TestLoadCandidates -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add lib/recommend/candidates.go lib/recommend/candidates_test.go
git commit -m "feat: candidate scoring, date-seeded shortlist, and 30-day dedupe

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Gemini response parsing, ID matching, and deterministic slotting

Turns the model's ID picks into stored `Recommendation`s with correct roles. Ignores hallucinated IDs; enforces the rewatch slot's `ViewCount > 0`; pads under-delivered roles from the ranked shortlist.

**Files:**
- Create: `lib/recommend/slotting.go`, `lib/recommend/slotting_test.go`

**Interfaces:**
- Produces:
  - `type pick struct { ID uint `json:"id"`; Explanation string `json:"explanation"` }`
  - `type pickResponse struct { Movies []pick `json:"movies"`; TVShows []pick `json:"tvshows"` }`
  - `func parsePickResponse(raw string) (pickResponse, error)`
  - `func selectMovies(picks []pick, shortlist []candidate, target int) []models.Recommendation`
  - `func selectTVShows(picks []pick, shortlist []candidate, target int) []models.Recommendation`
  - `func pickSchema() *genai.Schema`
- Consumes: `candidate` (Task 4), `models.Recommendation`, `models.TypeMovie/TypeTVShow`.

- [ ] **Step 1: Write slotting tests**

Create `lib/recommend/slotting_test.go`:

```go
package recommend

import (
	"testing"
)

func cand(id uint, typ string, view int, genres ...string) candidate {
	return candidate{ID: id, Type: typ, Title: "t", Genres: genres, ViewCount: view, Rating: 7}
}

func TestParsePickResponse_ok(t *testing.T) {
	raw := `{"movies":[{"id":5,"explanation":"funny"}],"tvshows":[{"id":9,"explanation":"good"}]}`
	pr, err := parsePickResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Movies) != 1 || pr.Movies[0].ID != 5 || pr.Movies[0].Explanation != "funny" {
		t.Errorf("bad movies parse: %+v", pr.Movies)
	}
}

func TestSelectMovies_ignoresUnknownIDsAndFillsRoles(t *testing.T) {
	shortlist := []candidate{
		cand(1, "movie", 0, "Comedy"),
		cand(2, "movie", 0, "Action"),
		cand(3, "movie", 4, "Drama"), // watched -> eligible for rewatch slot
		cand(4, "movie", 0, "Horror"),
	}
	picks := []pick{
		{ID: 1, Explanation: "funny"},
		{ID: 999, Explanation: "hallucinated"}, // unknown -> ignored
		{ID: 2, Explanation: "action"},
		{ID: 3, Explanation: "rewatch"},
		{ID: 4, Explanation: "extra"},
	}
	recs := selectMovies(picks, shortlist, 4)
	if len(recs) != 4 {
		t.Fatalf("got %d movies, want 4", len(recs))
	}
	ids := map[uint]bool{}
	for _, r := range recs {
		if r.MovieID != nil {
			ids[*r.MovieID] = true
		}
	}
	if ids[999] {
		t.Error("hallucinated ID must not appear")
	}
}

func TestSelectMovies_rewatchRequiresWatched(t *testing.T) {
	// Only unwatched titles available: rewatch slot cannot be filled by a watched
	// title, but the target count is still met by padding.
	shortlist := []candidate{cand(1, "movie", 0, "Comedy"), cand(2, "movie", 0, "Action"), cand(3, "movie", 0, "Drama")}
	picks := []pick{{ID: 1}, {ID: 2}, {ID: 3}}
	recs := selectMovies(picks, shortlist, 4)
	if len(recs) != 3 {
		t.Fatalf("got %d, want 3 (only three candidates exist)", len(recs))
	}
	for _, r := range recs {
		c := findCand(shortlist, *r.MovieID)
		if c.ViewCount != 0 {
			t.Error("no watched candidate exists; none should be selected as watched")
		}
	}
}

func findCand(cs []candidate, id uint) candidate {
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	return candidate{}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lib/recommend/... -run 'TestParsePickResponse|TestSelectMovies' -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement slotting.go**

Create `lib/recommend/slotting.go`:

```go
package recommend

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/icco/recommender/models"
	"google.golang.org/genai"
)

type pick struct {
	ID          uint   `json:"id"`
	Explanation string `json:"explanation"`
}

type pickResponse struct {
	Movies  []pick `json:"movies"`
	TVShows []pick `json:"tvshows"`
}

// parsePickResponse decodes the model's JSON. Unknown fields are ignored.
func parsePickResponse(raw string) (pickResponse, error) {
	var pr pickResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &pr); err != nil {
		return pr, fmt.Errorf("parse pick response: %w", err)
	}
	return pr, nil
}

// pickSchema is the Gemini response schema: two arrays of {id, explanation}.
func pickSchema() *genai.Schema {
	item := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"id":          {Type: genai.TypeInteger},
			"explanation": {Type: genai.TypeString},
		},
		Required: []string{"id", "explanation"},
	}
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"movies":  {Type: genai.TypeArray, Items: item},
			"tvshows": {Type: genai.TypeArray, Items: item},
		},
		Required: []string{"movies", "tvshows"},
	}
}

func candByID(shortlist []candidate) map[uint]candidate {
	m := make(map[uint]candidate, len(shortlist))
	for _, c := range shortlist {
		m[c.ID] = c
	}
	return m
}

func toRec(c candidate, explanation string, date time.Time) models.Recommendation {
	rec := models.Recommendation{
		Title: c.Title, Type: c.Type, Year: c.Year, Rating: c.Rating,
		Genre: strings.Join(c.Genres, ", "), PosterURL: c.PosterURL, Runtime: c.Runtime,
		Explanation: explanation, Date: date,
	}
	if c.TMDbID != nil {
		rec.TMDbID = *c.TMDbID
	}
	switch c.Type {
	case models.TypeMovie:
		id := c.ID
		rec.MovieID = &id
	case models.TypeTVShow:
		id := c.ID
		rec.TVShowID = &id
	}
	return rec
}

func hasGenre(c candidate, want string) bool {
	for _, g := range c.Genres {
		if strings.Contains(strings.ToLower(g), want) {
			return true
		}
	}
	return false
}

// selectMovies fills up to `target` movie slots (comedy, action/drama, rewatch,
// then wildcards) from the model's valid picks, padding from the shortlist when
// the model under-delivers. Unknown IDs are ignored; the rewatch slot only
// accepts a watched (ViewCount>0) title. Date is set by the caller (0 here).
func selectMovies(picks []pick, shortlist []candidate, target int) []models.Recommendation {
	byID := candByID(shortlist)
	used := make(map[uint]bool)
	var out []models.Recommendation

	take := func(c candidate, expl string) {
		used[c.ID] = true
		out = append(out, toRec(c, expl, time.Time{}))
	}

	// Ordered list of valid movie picks with their explanations.
	type vc struct {
		c    candidate
		expl string
	}
	var valid []vc
	for _, p := range picks {
		c, ok := byID[p.ID]
		if !ok || c.Type != models.TypeMovie {
			continue
		}
		valid = append(valid, vc{c, p.Explanation})
	}

	fillRole := func(match func(candidate) bool) {
		if len(out) >= target {
			return
		}
		for _, v := range valid {
			if used[v.c.ID] {
				continue
			}
			if match(v.c) {
				take(v.c, v.expl)
				return
			}
		}
	}

	fillRole(func(c candidate) bool { return hasGenre(c, "comedy") })
	fillRole(func(c candidate) bool { return hasGenre(c, "action") || hasGenre(c, "drama") })
	fillRole(func(c candidate) bool { return c.ViewCount > 0 }) // rewatch
	// Wildcards from remaining valid picks.
	for _, v := range valid {
		if len(out) >= target {
			break
		}
		if used[v.c.ID] {
			continue
		}
		take(v.c, v.expl)
	}
	// Pad from ranked shortlist if still short (e.g. model returned too few).
	for _, c := range shortlist {
		if len(out) >= target {
			break
		}
		if c.Type != models.TypeMovie || used[c.ID] {
			continue
		}
		take(c, "")
	}
	return out
}

// selectTVShows fills up to `target` TV slots from valid picks, padding from the
// shortlist. All candidates here are already unwatched (loadCandidates filters).
func selectTVShows(picks []pick, shortlist []candidate, target int) []models.Recommendation {
	byID := candByID(shortlist)
	used := make(map[uint]bool)
	var out []models.Recommendation
	for _, p := range picks {
		if len(out) >= target {
			break
		}
		c, ok := byID[p.ID]
		if !ok || c.Type != models.TypeTVShow || used[c.ID] {
			continue
		}
		used[c.ID] = true
		out = append(out, toRec(c, p.Explanation, time.Time{}))
	}
	for _, c := range shortlist {
		if len(out) >= target {
			break
		}
		if c.Type != models.TypeTVShow || used[c.ID] {
			continue
		}
		used[c.ID] = true
		out = append(out, toRec(c, "", time.Time{}))
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./lib/recommend/... -run 'TestParsePickResponse|TestSelectMovies' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/recommend/slotting.go lib/recommend/slotting_test.go
git commit -m "feat: ID-based pick parsing and deterministic slotting

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Rewrite GenerateRecommendations + explicit run state

Replaces the old generation body with the new pipeline, records a `GenerationRun`, and switches completeness to "did a run succeed today." Removes the last `go-openai` import.

**Files:**
- Create: `lib/recommend/generate.go`
- Modify: `lib/recommend/recommend.go` (delete old `GenerateRecommendations`, `CheckRecommendationsComplete`, `CheckRecommendationsExist`, `formatContent`, `loadPromptTemplate`, `RecommendationContext`, `UnwatchedContent`, `retryOpenAIRequest`; keep `GetRecommendationsForDate`, `GetRecommendationDates`, `GetStats`, `recommendationUTCDayRange`)
- Modify: `lib/recommend/prompts/system_openai.txt`, `recommendation_openai.txt` (rewrite; rename optional)
- Modify: `handlers/handlers.go` (`HandleCron` uses `DidRunToday`)

**Interfaces:**
- Consumes: `Chatter.Complete` (Task 3), `loadCandidates`/`buildShortlist`/`formatShortlist` (Task 4), `parsePickResponse`/`selectMovies`/`selectTVShows`/`pickSchema` (Task 5), `r.tmdb` for finalist poster fill.
- Produces:
  - `func (r *Recommender) GenerateRecommendations(ctx context.Context, date time.Time) error`
  - `func (r *Recommender) DidRunToday(ctx context.Context, date time.Time) (bool, error)`
  - `Recommender` gains `model string` field set from env in `New` (for `GenerationRun.Model`). Set `chat` + `model` together.

- [ ] **Step 1: Add DidRunToday + a model field**

Add to `lib/recommend/recommend.go` struct: `model string`. In `New`, accept the model name: change signature to `New(db *gorm.DB, plexClient *plex.Client, tmdbClient *tmdb.Client, chat Chatter, model string)` and set `model: model`. Update `main.go` call to `recommend.New(gormDB, plexClient, tmdbClient, chat, geminiModel)`.

Add to `recommend.go`:

```go
// DidRunToday reports whether a successful generation run exists for the day.
func (r *Recommender) DidRunToday(ctx context.Context, date time.Time) (bool, error) {
	start, end := recommendationUTCDayRange(date)
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.GenerationRun{}).
		Where(`"date" >= ? AND "date" < ? AND status = ?`, start, end, models.RunStatusOK).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("check run: %w", err)
	}
	return count > 0, nil
}
```

- [ ] **Step 2: Rewrite the prompt templates**

Replace `lib/recommend/prompts/system_openai.txt` with:

```
You are a media recommendation assistant. You will be given a numbered shortlist
of titles the user already owns. Choose the best fits for each requested slot and
return only their IDs with a one-sentence reason each. Never invent IDs or titles.
```

Replace `lib/recommend/prompts/recommendation_openai.txt` with:

```
Pick recommendations for the user from ONLY the shortlist below, using the id values.

Movies: choose up to {{.TargetMovies}} — ideally one comedy, one action/drama,
one worth rewatching, and the rest your best picks.
TV shows: choose up to {{.TargetTVShows}}.

Rules:
- Use only ids present in the shortlist. Do not repeat an id.
- Give a short, specific reason per pick.

{{if .Profile}}User taste profile:
{{.Profile}}
{{end}}
Movie shortlist:
{{.Movies}}

TV shortlist:
{{.TVShows}}
```

- [ ] **Step 3: Implement generate.go**

Create `lib/recommend/generate.go`:

```go
package recommend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/icco/gutil/logging"
	"github.com/icco/recommender/lib/tmdb"
	"github.com/icco/recommender/models"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	poolSize      = 240
	shortlistSize = 80
	targetMovies  = 4
	targetTVShows = 3
)

type promptData struct {
	TargetMovies  int
	TargetTVShows int
	Profile       string
	Movies        string
	TVShows       string
}

// GenerateRecommendations builds the day's recommendations from the cached Plex
// library using Gemini to pick from a scored shortlist. It records a
// GenerationRun and is a no-op if a successful run already exists for the day.
func (r *Recommender) GenerateRecommendations(ctx context.Context, date time.Time) error {
	l := logging.FromContext(ctx)
	start := time.Now()

	done, err := r.DidRunToday(ctx, date)
	if err != nil {
		return err
	}
	if done {
		l.Infow("Recommendations already generated for date", "date", date)
		return nil
	}

	movies, tvshows, err := r.loadCandidates(ctx, date)
	if err != nil {
		return r.recordRun(ctx, date, 0, 0, err)
	}
	if len(movies) == 0 && len(tvshows) == 0 {
		err := fmt.Errorf("no eligible candidates; run /cron/cache first")
		return r.recordRun(ctx, date, 0, 0, err)
	}

	movieShortlist := buildShortlist(movies, date, poolSize, shortlistSize)
	tvShortlist := buildShortlist(tvshows, date, poolSize, shortlistSize)

	system, user, err := r.renderPrompts(ctx, movieShortlist, tvShortlist, date)
	if err != nil {
		return r.recordRun(ctx, date, 0, 0, err)
	}

	raw, err := r.chat.Complete(ctx, system, user, pickSchema())
	if err != nil {
		return r.recordRun(ctx, date, 0, 0, fmt.Errorf("gemini: %w", err))
	}

	pr, err := parsePickResponse(raw)
	if err != nil {
		return r.recordRun(ctx, date, 0, 0, err)
	}

	combined := append([]candidate{}, movieShortlist...)
	combined = append(combined, tvShortlist...)
	recs := selectMovies(pr.Movies, combined, targetMovies)
	recs = append(recs, selectTVShows(pr.TVShows, combined, targetTVShows)...)
	if len(recs) == 0 {
		return r.recordRun(ctx, date, 0, 0, fmt.Errorf("no recommendations selected"))
	}

	for i := range recs {
		recs[i].Date = date
		r.fillPoster(ctx, &recs[i])
	}

	movieCount, tvCount := 0, 0
	for _, rec := range recs {
		if rec.Type == models.TypeMovie {
			movieCount++
		} else {
			tvCount++
		}
	}

	if err := r.saveRecommendations(ctx, date, recs); err != nil {
		return r.recordRun(ctx, date, movieCount, tvCount, err)
	}

	if err := r.recordRun(ctx, date, movieCount, tvCount, nil); err != nil {
		return err
	}
	l.Infow("Generated recommendations", "movies", movieCount, "tvshows", tvCount, "duration", time.Since(start))
	return nil
}

func (r *Recommender) renderPrompts(ctx context.Context, movies, tvshows []candidate, date time.Time) (system, user string, err error) {
	sysTmpl, err := prompts.FS.ReadFile("system_openai.txt")
	if err != nil {
		return "", "", fmt.Errorf("read system prompt: %w", err)
	}
	userTmplBytes, err := prompts.FS.ReadFile("recommendation_openai.txt")
	if err != nil {
		return "", "", fmt.Errorf("read user prompt: %w", err)
	}
	userTmpl, err := template.New("rec").Parse(string(userTmplBytes))
	if err != nil {
		return "", "", fmt.Errorf("parse user prompt: %w", err)
	}
	profile, err := r.tasteProfile(ctx) // Phase 2; returns "" until Task 8
	if err != nil {
		logging.FromContext(ctx).Warnw("taste profile failed; continuing without", zap.Error(err))
		profile = ""
	}
	var b strings.Builder
	if err := userTmpl.Execute(&b, promptData{
		TargetMovies: targetMovies, TargetTVShows: targetTVShows, Profile: profile,
		Movies: formatShortlist(movies), TVShows: formatShortlist(tvshows),
	}); err != nil {
		return "", "", fmt.Errorf("execute user prompt: %w", err)
	}
	return string(sysTmpl), b.String(), nil
}

// fillPoster lazily fetches a TMDb poster only when one is missing. Bounded to the
// finalist set, so at most a handful of calls per run.
func (r *Recommender) fillPoster(ctx context.Context, rec *models.Recommendation) {
	if rec.PosterURL != "" || r.tmdb == nil {
		return
	}
	l := logging.FromContext(ctx)
	switch rec.Type {
	case models.TypeMovie:
		res, err := r.tmdb.SearchMovie(ctx, rec.Title, rec.Year)
		if err != nil {
			if !errors.Is(err, tmdb.ErrCircuitOpen) {
				l.Warnw("poster fill (movie) failed", "title", rec.Title, zap.Error(err))
			}
			return
		}
		if len(res.Results) > 0 {
			rec.PosterURL = r.tmdb.GetPosterURL(res.Results[0].PosterPath)
		}
	case models.TypeTVShow:
		res, err := r.tmdb.SearchTVShow(ctx, rec.Title, rec.Year)
		if err != nil {
			if !errors.Is(err, tmdb.ErrCircuitOpen) {
				l.Warnw("poster fill (tv) failed", "title", rec.Title, zap.Error(err))
			}
			return
		}
		if len(res.Results) > 0 {
			rec.PosterURL = r.tmdb.GetPosterURL(res.Results[0].PosterPath)
		}
	}
}

func (r *Recommender) saveRecommendations(ctx context.Context, date time.Time, recs []models.Recommendation) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(`"date" = ?`, date).Delete(&models.Recommendation{}).Error; err != nil {
			return fmt.Errorf("clear existing recs: %w", err)
		}
		// The (date, title) unique index rejects two Plex items with the same title
		// on one day; skip in-batch title collisions rather than fail the run.
		seen := make(map[string]bool, len(recs))
		for i := range recs {
			if seen[recs[i].Title] {
				continue
			}
			seen[recs[i].Title] = true
			if err := tx.Create(&recs[i]).Error; err != nil {
				return fmt.Errorf("create rec %q: %w", recs[i].Title, err)
			}
		}
		return nil
	})
}

func (r *Recommender) recordRun(ctx context.Context, date time.Time, movieCount, tvCount int, genErr error) error {
	run := models.GenerationRun{
		Date: date, Status: models.RunStatusOK, MovieCount: movieCount,
		TVShowCount: tvCount, Model: r.model,
	}
	if genErr != nil {
		run.Status = models.RunStatusError
		run.Error = genErr.Error()
	}
	if err := r.db.WithContext(ctx).Create(&run).Error; err != nil {
		return fmt.Errorf("record run: %w (original: %v)", err, genErr)
	}
	return genErr
}
```

Add the `prompts` import to `generate.go`: `"github.com/icco/recommender/lib/recommend/prompts"`.

- [ ] **Step 4: Add a stub tasteProfile so it compiles (Task 8 implements it)**

Create `lib/recommend/profile.go` with a stub:

```go
package recommend

import "context"

// tasteProfile summarizes the user's taste from stored signals. Phase 2 fills
// this in; until then it returns an empty profile.
func (r *Recommender) tasteProfile(ctx context.Context) (string, error) {
	return "", nil
}
```

- [ ] **Step 5: Delete the old generation code**

In `lib/recommend/recommend.go`, delete `GenerateRecommendations`, `CheckRecommendationsComplete`, `CheckRecommendationsExist`, `formatContent`, `loadPromptTemplate`, `logTMDbErr`, `RecommendationContext`, `UnwatchedContent`, and `retryOpenAIRequest`. Remove now-unused imports (`encoding/json`, `math`, `text/template`, `os`, `net/http`, `github.com/sashabaranov/go-openai`, `github.com/icco/recommender/lib/recommend/prompts`, `strings` if unused). Keep `GetRecommendationsForDate`, `GetRecommendationDates`, `GetStats`, `recommendationUTCDayRange`, `StatsData`.

- [ ] **Step 6: Update HandleCron to use DidRunToday**

In `handlers/handlers.go` `HandleCron`, replace the two `r.CheckRecommendationsExist(ctx, today)` calls with `r.DidRunToday(ctx, today)` (same boolean semantics: true → already done). No other handler changes needed — `HandleHome`/`HandleDate` already render an empty state for zero rows via `home.html`.

- [ ] **Step 7: Run go mod tidy to drop go-openai**

Run: `go mod tidy && go build ./...`
Expected: `github.com/sashabaranov/go-openai` removed from `go.mod`; build succeeds.

- [ ] **Step 8: Write the end-to-end generation test with a fake Chatter**

Create `lib/recommend/generate_test.go`:

```go
package recommend

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/icco/recommender/models"
	"google.golang.org/genai"
)

type fakeChatter struct{ reply string }

func (f fakeChatter) Complete(ctx context.Context, system, user string, schema *genai.Schema) (string, error) {
	return f.reply, nil
}

func TestGenerateRecommendations_endToEnd(t *testing.T) {
	db := testDB(t)
	if err := db.AutoMigrate(&models.GenerationRun{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	date := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)

	comedy := models.Movie{Title: "Funny", Year: 2000, Rating: 8, Genre: "Comedy", PosterURL: "p1", PlexRatingKey: "m1"}
	action := models.Movie{Title: "Boom", Year: 2001, Rating: 8, Genre: "Action", PosterURL: "p2", PlexRatingKey: "m2"}
	show := models.TVShow{Title: "Series", Year: 2010, Rating: 8, Genre: "Drama", PosterURL: "p3", ViewCount: 0, PlexRatingKey: "s1"}
	for _, m := range []*models.Movie{&comedy, &action} {
		if err := db.Create(m).Error; err != nil { t.Fatal(err) }
	}
	if err := db.Create(&show).Error; err != nil { t.Fatal(err) }

	reply := fmt.Sprintf(`{"movies":[{"id":%d,"explanation":"lol"},{"id":%d,"explanation":"bang"}],"tvshows":[{"id":%d,"explanation":"gripping"}]}`,
		comedy.ID, action.ID, show.ID)
	r := &Recommender{db: db, chat: fakeChatter{reply: reply}, model: "test"}

	if err := r.GenerateRecommendations(ctx, date); err != nil {
		t.Fatalf("generate: %v", err)
	}

	recs, err := r.GetRecommendationsForDate(ctx, date)
	if err != nil { t.Fatal(err) }
	if len(recs) != 3 {
		t.Fatalf("got %d recs, want 3", len(recs))
	}
	var gotExpl bool
	for _, rec := range recs {
		if rec.Explanation != "" {
			gotExpl = true
		}
	}
	if !gotExpl {
		t.Error("expected explanations stored")
	}

	done, err := r.DidRunToday(ctx, date)
	if err != nil { t.Fatal(err) }
	if !done {
		t.Error("expected a successful GenerationRun")
	}

	// Second call is a no-op (already ran).
	if err := r.GenerateRecommendations(ctx, date); err != nil {
		t.Fatalf("second generate: %v", err)
	}
	recs2, _ := r.GetRecommendationsForDate(ctx, date)
	if len(recs2) != 3 {
		t.Fatalf("rerun changed rec count to %d", len(recs2))
	}
}
```

- [ ] **Step 9: Run the full recommend suite**

Run: `go test ./lib/recommend/... -v`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add lib/recommend/generate.go lib/recommend/profile.go lib/recommend/generate_test.go lib/recommend/recommend.go lib/recommend/prompts handlers/handlers.go go.mod go.sum
git commit -m "feat: new Gemini generation pipeline with explicit run state

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Surface explanations in the UI

Renders the stored `Explanation` on each card. Small template change.

**Files:**
- Modify: `handlers/templates/home.html`

**Interfaces:**
- Consumes: `models.Recommendation.Explanation` (Task 1).

- [ ] **Step 1: Add explanation to the movie card**

In `handlers/templates/home.html`, inside the movie card `<div class="p-4">`, after the Runtime line add:

```html
          {{if .Explanation}}<p class="text-gray-500 italic mt-2">{{.Explanation}}</p>{{end}}
```

- [ ] **Step 2: Add explanation to the TV card**

Inside the TV card `<div class="p-4">`, after the Seasons line add the same block:

```html
          {{if .Explanation}}<p class="text-gray-500 italic mt-2">{{.Explanation}}</p>{{end}}
```

- [ ] **Step 3: Build and test**

Run: `go build ./... && go test ./...`
Expected: PASS (templates are embedded; a build error here would mean a typo).

- [ ] **Step 4: Commit**

```bash
git add handlers/templates/home.html
git commit -m "feat: show recommendation explanations on cards

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Taste profile from Plex signals (Phase 2)

Derives a taste profile from Plex watch counts + ratings and feeds genre affinity into scoring and a profile string into the prompt. No external API.

> **Design note:** Phase 2 computes affinity directly from `models.Movie`/`models.TVShow` rows — it does **not** populate `ExternalSignal`. Nothing consumes `ExternalSignal` yet, so writing it now would be dead data (YAGNI). The table exists (Task 1) as the shared store that Phase 3 (Trakt) will populate and this affinity code will then read alongside Plex rows.

**Files:**
- Modify: `lib/recommend/profile.go` (replace stub)
- Modify: `lib/recommend/candidates.go` (apply affinity in `loadCandidates`)
- Create: `lib/recommend/profile_test.go`

**Interfaces:**
- Produces:
  - `func (r *Recommender) genreAffinity(ctx context.Context) (map[string]float64, error)` — normalized 0..1 affinity per genre from watched+highly-rated titles.
  - `func (r *Recommender) tasteProfile(ctx context.Context) (string, error)` — human-readable top-genres summary for the prompt.
- Consumes: `models.Movie`, `models.TVShow`, `splitGenres` (Task 4).

- [ ] **Step 1: Write the profile test**

Create `lib/recommend/profile_test.go`:

```go
package recommend

import (
	"context"
	"testing"

	"github.com/icco/recommender/models"
)

func TestGenreAffinity_favorsWatchedAndRated(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := context.Background()

	// Two watched high-rated comedies, one unwatched horror.
	db.Create(&models.Movie{Title: "C1", Genre: "Comedy", Rating: 9, ViewCount: 3, PlexRatingKey: "a"})
	db.Create(&models.Movie{Title: "C2", Genre: "Comedy", Rating: 8, ViewCount: 2, PlexRatingKey: "b"})
	db.Create(&models.Movie{Title: "H1", Genre: "Horror", Rating: 8, ViewCount: 0, PlexRatingKey: "c"})

	aff, err := r.genreAffinity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if aff["Comedy"] <= aff["Horror"] {
		t.Errorf("Comedy affinity (%.2f) should exceed Horror (%.2f)", aff["Comedy"], aff["Horror"])
	}
	if aff["Comedy"] > 1.0 || aff["Comedy"] < 0 {
		t.Errorf("affinity must be normalized 0..1, got %.2f", aff["Comedy"])
	}
}

func TestTasteProfile_nonEmptyWhenSignalsExist(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := context.Background()
	db.Create(&models.Movie{Title: "C1", Genre: "Comedy", Rating: 9, ViewCount: 3, PlexRatingKey: "a"})
	p, err := r.tasteProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p == "" {
		t.Error("expected a non-empty profile when watched titles exist")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lib/recommend/... -run 'TestGenreAffinity|TestTasteProfile' -v`
Expected: FAIL (stub returns "", `genreAffinity` undefined).

- [ ] **Step 3: Replace profile.go**

Overwrite `lib/recommend/profile.go`:

```go
package recommend

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/icco/recommender/models"
)

// genreAffinity computes a normalized (0..1) taste weight per genre from watched
// and highly-rated Plex titles. Watched titles and higher ratings weigh more.
func (r *Recommender) genreAffinity(ctx context.Context) (map[string]float64, error) {
	raw := make(map[string]float64)

	accumulate := func(genre string, rating float64, viewCount int) {
		for _, g := range splitGenres(genre) {
			w := rating / 10.0
			if viewCount > 0 {
				w += 1.0
			}
			raw[g] += w
		}
	}

	var movies []models.Movie
	if err := r.db.WithContext(ctx).Find(&movies).Error; err != nil {
		return nil, fmt.Errorf("affinity movies: %w", err)
	}
	for _, m := range movies {
		accumulate(m.Genre, m.Rating, m.ViewCount)
	}
	var shows []models.TVShow
	if err := r.db.WithContext(ctx).Find(&shows).Error; err != nil {
		return nil, fmt.Errorf("affinity shows: %w", err)
	}
	for _, s := range shows {
		accumulate(s.Genre, s.Rating, s.ViewCount)
	}

	max := 0.0
	for _, v := range raw {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		return map[string]float64{}, nil
	}
	out := make(map[string]float64, len(raw))
	for g, v := range raw {
		out[g] = v / max
	}
	return out, nil
}

// tasteProfile renders the top genres as a short prompt fragment.
func (r *Recommender) tasteProfile(ctx context.Context) (string, error) {
	aff, err := r.genreAffinity(ctx)
	if err != nil {
		return "", err
	}
	if len(aff) == 0 {
		return "", nil
	}
	type gv struct {
		g string
		v float64
	}
	var gvs []gv
	for g, v := range aff {
		gvs = append(gvs, gv{g, v})
	}
	sort.Slice(gvs, func(i, j int) bool {
		if gvs[i].v == gvs[j].v {
			return gvs[i].g < gvs[j].g
		}
		return gvs[i].v > gvs[j].v
	})
	n := 5
	if len(gvs) < n {
		n = len(gvs)
	}
	tops := make([]string, 0, n)
	for _, x := range gvs[:n] {
		tops = append(tops, x.g)
	}
	return "Favorite genres, most to least: " + strings.Join(tops, ", ") + ".", nil
}
```

- [ ] **Step 4: Apply affinity to candidate scoring**

In `lib/recommend/candidates.go` `loadCandidates`, compute affinity once and set `Affinity` on each candidate. After `excludeMovies, excludeTV, err := ...`, add:

```go
	aff, err := r.genreAffinity(ctx)
	if err != nil {
		return nil, nil, err
	}
	affinityFor := func(genres []string) float64 {
		best := 0.0
		for _, g := range genres {
			if v := aff[g]; v > best {
				best = v
			}
		}
		return best
	}
```

In both candidate-append blocks, set `Affinity: affinityFor(splitGenres(m.Genre))` (movies) and `Affinity: affinityFor(splitGenres(s.Genre))` (shows). `scoreCandidate` already adds `c.Affinity`.

- [ ] **Step 5: Run the recommend suite**

Run: `go test ./lib/recommend/... -v`
Expected: PASS (profile tests pass; earlier `TestLoadCandidates` still passes — affinity only reorders, doesn't drop titles).

- [ ] **Step 6: Commit**

```bash
git add lib/recommend/profile.go lib/recommend/profile_test.go lib/recommend/candidates.go
git commit -m "feat: Plex-derived taste profile feeds scoring and the prompt

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Config + docs

Updates env-var surfaces and docs to Gemini/Vertex. Final full-suite verification.

**Files:**
- Modify: `README.md`, `template.env`, `docker-compose.yml`, `CLAUDE.md`

**Interfaces:** none (docs/config only).

- [ ] **Step 1: Update template.env**

Set `template.env` to the new variables:

```
PLEX_URL=
PLEX_TOKEN=
TMDB_API_KEY=
GOOGLE_GENAI_USE_VERTEXAI=true
GOOGLE_CLOUD_PROJECT=
GOOGLE_CLOUD_LOCATION=us-central1
GEMINI_MODEL=gemini-2.5-flash
GOOGLE_APPLICATION_CREDENTIALS=
PORT=8080
DB_PATH=recommender.db
```

- [ ] **Step 2: Update docker-compose.yml**

In `docker-compose.yml`, replace the `OPENAI_API_KEY` environment entry with the Google variables (`GOOGLE_GENAI_USE_VERTEXAI`, `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION`, `GEMINI_MODEL`, `GOOGLE_APPLICATION_CREDENTIALS`), and if a service-account key is used locally, mount it read-only and point `GOOGLE_APPLICATION_CREDENTIALS` at the mount path.

- [ ] **Step 3: Update README.md**

In `README.md`: change the stack line from OpenAI to "Gemini on Vertex AI (`google.golang.org/genai`)"; replace the `OPENAI_API_KEY` row in the env table with the four Google variables (noting `GOOGLE_CLOUD_PROJECT` and `GOOGLE_CLOUD_LOCATION` are required and auth uses ADC); update the "Recommendation flow" summary to describe candidate shortlisting + ID-based picking; note TMDb is now a cache-time enrichment + finalist poster fallback.

- [ ] **Step 4: Update CLAUDE.md**

In `CLAUDE.md`: replace OpenAI references in "Project Overview", "Required Environment Variables", "API Integration Features", and "Logging" with Gemini/Vertex equivalents; update the recommendation-logic description to the shortlist + ID-pick + `GenerationRun` model.

- [ ] **Step 5: Full verification**

Run: `go build ./... && go test ./... && gofmt -l . && go vet ./...`
Expected: build OK; all tests PASS; `gofmt -l .` prints nothing; `go vet` clean.

- [ ] **Step 6: Commit**

```bash
git add README.md template.env docker-compose.yml CLAUDE.md
git commit -m "docs: switch configuration and docs to Gemini on Vertex AI

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Verification checklist (whole PR)

- [ ] `go build ./...` succeeds; `go vet ./...` clean; `gofmt -l .` empty.
- [ ] `go test ./...` passes, including new tests in `lib/db`, `lib/plex`, `lib/recommend`.
- [ ] No remaining `sashabaranov/go-openai` import (`grep -r sashabaranov .` → only maybe `go.sum` history; `go.mod` clean after tidy).
- [ ] `main.go` fails fast without `GOOGLE_CLOUD_PROJECT` / `GOOGLE_CLOUD_LOCATION`.
- [ ] Recommend-time TMDb calls are bounded to finalists with missing posters (`fillPoster` only).
- [ ] Manual smoke (requires a Plex library + Vertex access + ADC): `go run .`, then `curl localhost:8080/cron/cache`, wait, `curl localhost:8080/cron/recommend`, then load `/` and confirm 4 movies + 3 TV with explanations, and `/stats` shows cached counts. Re-hitting `/cron/recommend` the same day is a no-op (a `GenerationRun` exists).

## Notes for the implementer

- Tasks 3–6 are tightly coupled through the `Chatter` swap; if separating Task 3's build-green step is awkward, implement 3→6 back-to-back and commit at each task boundary.
- The `genai` API surface can vary by SDK version. If `genai.Text`, `genai.NewContentFromText`, `resp.Text()`, `genai.TypeObject`, or `ResponseSchema` differ from the code above, consult the installed version's godoc (`go doc google.golang.org/genai`) and adapt — the Chatter interface isolates all such changes to `llm.go` + `slotting.go`'s `pickSchema`.
- Keep functions small and files focused (per the repo's existing style). Don't reintroduce the removed in-memory cache.
