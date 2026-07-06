# Signal Sources (Trakt + AniList) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Feed Trakt (watched/ratings/watchlist) and AniList (anime scores) into recommendations as ranking signals over Plex-owned titles, in a single PR.

**Architecture:** Two API clients (`lib/trakt`, `lib/anilist`) with overridable base URLs for httptest. A `SignalSource` interface + adapters in `lib/recommend/signals.go` join fetched data to Plex rows by ID (Trakt) or title+year (AniList) and upsert `ExternalSignal`. `/cron/cache` runs `SyncSignals` after the Plex upsert. Consumption extends `genreAffinity`, `loadCandidates`/`scoreCandidate`, and the prompt. Both sources are optional (env-gated).

**Tech Stack:** Go 1.26, GORM + SQLite, Gemini/Vertex, `net/http` + `net/http/httptest`.

## Global Constraints

- Module `github.com/icco/recommender`, `go 1.26.2`.
- **Signals only rank Plex-owned titles; store a signal only when it joins to an owned title.** Unmatched → dropped, not an error.
- Both sources **optional**: `TRAKT_CLIENT_ID`+`TRAKT_CLIENT_SECRET` unset → Trakt skipped; `ANILIST_USERNAME` unset → AniList skipped.
- Join order for Trakt: TMDb → IMDb → TVDb. AniList: title (romaji/english) + year.
- Signal kinds (exist in `models`): `SignalKindWatched`, `SignalKindRated`, `SignalKindScore`, `SignalKindWatchlist`. Sources: `SourcePlex` (exists); add `SourceTrakt`, `SourceAniList`.
- Per-source sync failures are logged and never fail the cache job or other sources.
- `ExternalSignal` unique index is `(source, external_ref, kind)` — upsert on conflict.
- Loggers from `logging.FromContext(ctx)`; no `log/slog`.
- Branch `signal-sources-trakt-anilist`. Never commit to main; never force-push.
- Every task ends green: `go build ./... && go test ./... && gofmt -l . && go vet ./...`.
- golangci-lint (CI enables `bodyclose,misspell,gosec,errorlint,noctx,contextcheck,nilerr,wastedassign,unparam,copyloopvar,intrange,revive,gocritic,unconvert`) must pass: wrap errors with `%w`; give HTTP requests a ctx (`http.NewRequestWithContext`); `defer resp.Body.Close()`; exported struct fields use Go initialisms (`ID`, `URL`, `IMDb`/`TMDb` acceptable as-is since they're not initialisms revive flags — but `Url`→`URL`).
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`. No "Generated with Claude Code" footer.

## File Structure

**New:**
- `lib/trakt/client.go`, `lib/trakt/client_test.go` — Trakt API client (device flow, refresh, sync).
- `lib/anilist/client.go`, `lib/anilist/client_test.go` — AniList GraphQL client.
- `lib/recommend/signals.go`, `lib/recommend/signals_test.go` — `SignalSource`, Trakt/AniList adapters, `SyncSignals`, Trakt connect.

**Modified:**
- `models/models.go` — `SourceTrakt`/`SourceAniList` consts, `OAuthToken` model.
- `lib/db/migrations.go` — AutoMigrate `OAuthToken`.
- `lib/recommend/recommend.go` — `New` gains `SignalConfig`; struct fields.
- `lib/recommend/recommend_test.go` — `testDB` migrates `OAuthToken`.
- `lib/recommend/profile.go` — `genreAffinity` blends signals; `lovedTitles`.
- `lib/recommend/candidates.go` — watchlist boost + external-watched; `candidate.Watchlisted`; `scoreCandidate`.
- `lib/recommend/generate.go` — prompt `Loved` context.
- `lib/recommend/prompts/recommendation.txt` — render `Loved`.
- `handlers/handlers.go` — `HandleCache` runs `SyncSignals`; new `HandleTraktConnect`.
- `main.go` — read env, build `SignalConfig`, register `/trakt/connect`, pass recommender to `HandleCache`.
- `README.md`, `template.env`, `CLAUDE.md` — new env vars + endpoints.

---

## Task 1: OAuthToken model + source constants

**Files:** Modify `models/models.go`, `lib/db/migrations.go`, `lib/recommend/recommend_test.go`; Test `lib/db/migrations_test.go`.

**Interfaces:**
- Produces: `models.SourceTrakt = "trakt"`, `models.SourceAniList = "anilist"`; `models.OAuthToken{ID uint; Source string; AccessToken string; RefreshToken string; ExpiresAt time.Time; UpdatedAt time.Time}`.

- [ ] **Step 1: Add constants and model**

In `models/models.go`, add to the signal-source const block:

```go
	SourceTrakt   = "trakt"
	SourceAniList = "anilist"
```

Append the model:

```go
// OAuthToken stores an OAuth token set for an external source (e.g. Trakt).
type OAuthToken struct {
	ID           uint   `gorm:"primarykey"`
	Source       string `gorm:"type:varchar(32);not null;uniqueIndex:idx_oauth_source"`
	AccessToken  string `gorm:"type:varchar(512)"`
	RefreshToken string `gorm:"type:varchar(512)"`
	ExpiresAt    time.Time
	UpdatedAt    time.Time
}
```

- [ ] **Step 2: Register in migrations and test DB**

In `lib/db/migrations.go`, add `&models.OAuthToken{}` to the `AutoMigrate(...)` call. In `lib/recommend/recommend_test.go` `testDB`, add `&models.OAuthToken{}` to its `AutoMigrate(...)`.

- [ ] **Step 3: Extend the migration test**

In `lib/db/migrations_test.go`, add to `TestRunMigrations_createsNewTables` before the final assertions:

```go
	if !gdb.Migrator().HasTable(&models.OAuthToken{}) {
		t.Fatal("oauth_tokens table missing")
	}
```

- [ ] **Step 4: Run**

Run: `go test ./lib/db/... -run TestRunMigrations_createsNewTables -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add models/models.go lib/db/migrations.go lib/db/migrations_test.go lib/recommend/recommend_test.go
git commit -m "feat: add OAuthToken model and Trakt/AniList source constants

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Trakt API client

**Files:** Create `lib/trakt/client.go`, `lib/trakt/client_test.go`.

**Interfaces:**
- Produces:
  - `trakt.NewClient(clientID, clientSecret string) *Client` (field `BaseURL string` overridable in tests).
  - `type IDs struct { Trakt int; IMDb string; TMDb int; TVDb int }` (json `trakt`/`imdb`/`tmdb`/`tvdb`).
  - `type Media struct { Title string; Year int; IDs IDs }`.
  - `type SyncRow struct { Rating int; Movie *Media; Show *Media }`.
  - `type DeviceCode struct { DeviceCode, UserCode, VerificationURL string; ExpiresIn, Interval int }`.
  - `type Token struct { AccessToken, RefreshToken string; ExpiresIn int; CreatedAt int64 }` with `func (Token) ExpiresAt() time.Time`.
  - `func (c *Client) RequestDeviceCode(ctx) (*DeviceCode, error)`
  - `func (c *Client) PollForToken(ctx, deviceCode string) (*Token, error)`
  - `func (c *Client) RefreshToken(ctx, refreshToken string) (*Token, error)`
  - `func (c *Client) Sync(ctx, accessToken, path string) ([]SyncRow, error)`

- [ ] **Step 1: Write the client test**

Create `lib/trakt/client_test.go`:

```go
package trakt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSync_parsesMoviesWithIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("trakt-api-key") != "cid" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing auth headers: %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"rating":9,"movie":{"title":"The Matrix","year":1999,"ids":{"trakt":1,"imdb":"tt0133093","tmdb":603}}}]`))
	}))
	defer srv.Close()

	c := NewClient("cid", "secret")
	c.BaseURL = srv.URL
	rows, err := c.Sync(context.Background(), "tok", "sync/ratings/movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Movie == nil || rows[0].Movie.IDs.TMDb != 603 || rows[0].Rating != 9 {
		t.Fatalf("bad parse: %+v", rows)
	}
}

func TestRequestDeviceCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"1234","verification_url":"https://trakt.tv/activate","expires_in":600,"interval":5}`))
	}))
	defer srv.Close()
	c := NewClient("cid", "secret")
	c.BaseURL = srv.URL
	dc, err := c.RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dc.UserCode != "1234" || dc.DeviceCode != "dc" {
		t.Fatalf("bad device code: %+v", dc)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lib/trakt/... -v`
Expected: FAIL (package does not compile — `NewClient` undefined).

- [ ] **Step 3: Implement the client**

Create `lib/trakt/client.go`:

```go
// Package trakt is a minimal Trakt API client: OAuth device flow, token
// refresh, and the sync endpoints the recommender uses as ranking signals.
package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultBaseURL = "https://api.trakt.tv"
	apiVersion     = "2"
)

// Client talks to the Trakt API. BaseURL is overridable for tests.
type Client struct {
	clientID     string
	clientSecret string
	BaseURL      string
	httpClient   *http.Client
}

// NewClient returns a Trakt client for the given API app credentials.
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		BaseURL:      defaultBaseURL,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// IDs holds the external identifiers Trakt returns for a title.
type IDs struct {
	Trakt int    `json:"trakt"`
	IMDb  string `json:"imdb"`
	TMDb  int    `json:"tmdb"`
	TVDb  int    `json:"tvdb"`
}

// Media is a movie or show entry within a sync row.
type Media struct {
	Title string `json:"title"`
	Year  int    `json:"year"`
	IDs   IDs    `json:"ids"`
}

// SyncRow is one item from a sync/* endpoint. Exactly one of Movie/Show is set;
// Rating is populated for ratings endpoints.
type SyncRow struct {
	Rating int    `json:"rating"`
	Movie  *Media `json:"movie"`
	Show   *Media `json:"show"`
}

// DeviceCode is the response from the device-code endpoint.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// Token is an OAuth token set.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	CreatedAt    int64  `json:"created_at"`
}

// ExpiresAt is when the access token expires.
func (t Token) ExpiresAt() time.Time {
	return time.Unix(t.CreatedAt, 0).Add(time.Duration(t.ExpiresIn) * time.Second)
}

func (c *Client) postJSON(ctx context.Context, path string, body, out any) (int, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/"+path, bytes.NewReader(buf))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("trakt %s: HTTP %d: %s", path, resp.StatusCode, string(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

// RequestDeviceCode starts the OAuth device flow.
func (c *Client) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	var dc DeviceCode
	if _, err := c.postJSON(ctx, "oauth/device/code", map[string]string{"client_id": c.clientID}, &dc); err != nil {
		return nil, err
	}
	return &dc, nil
}

// PollForToken polls once for the token; HTTP 400 means "authorization pending".
// Returns (nil, nil) when still pending so the caller can wait and retry.
func (c *Client) PollForToken(ctx context.Context, deviceCode string) (*Token, error) {
	var tok Token
	status, err := c.postJSON(ctx, "oauth/device/token", map[string]string{
		"code": deviceCode, "client_id": c.clientID, "client_secret": c.clientSecret,
	}, &tok)
	if status == http.StatusBadRequest {
		return nil, nil // pending
	}
	if err != nil {
		return nil, err
	}
	return &tok, nil
}

// RefreshToken exchanges a refresh token for a new token set.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (*Token, error) {
	var tok Token
	if _, err := c.postJSON(ctx, "oauth/token", map[string]string{
		"refresh_token": refreshToken,
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"grant_type":    "refresh_token",
	}, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// Sync GETs a sync/* endpoint (e.g. "sync/watched/movies") with the access token.
func (c *Client) Sync(ctx context.Context, accessToken, path string) ([]SyncRow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("trakt-api-version", apiVersion)
	req.Header.Set("trakt-api-key", c.clientID)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("trakt %s: HTTP %d: %s", path, resp.StatusCode, string(data))
	}
	var rows []SyncRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return rows, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/trakt/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/trakt/
git commit -m "feat(trakt): API client with device flow and sync endpoints

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: AniList API client

**Files:** Create `lib/anilist/client.go`, `lib/anilist/client_test.go`.

**Interfaces:**
- Produces:
  - `anilist.NewClient() *Client` (field `URL string` overridable).
  - `type Entry struct { Title string; Year int; Score float64 }` (Score normalized 0..10).
  - `func (c *Client) List(ctx, username string) ([]Entry, error)` — returns rated entries (score > 0), english title preferred then romaji.

- [ ] **Step 1: Write the client test**

Create `lib/anilist/client_test.go`:

```go
package anilist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestList_normalizesScoresAndPicksTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"User":{"mediaListOptions":{"scoreFormat":"POINT_100"}},
			"MediaListCollection":{"lists":[{"entries":[
				{"score":90,"media":{"seasonYear":2019,"title":{"romaji":"Kimetsu","english":"Demon Slayer"}}},
				{"score":0,"media":{"seasonYear":2020,"title":{"romaji":"Unrated","english":null}}}
			]}]}}}}`))
	}))
	defer srv.Close()

	c := NewClient()
	c.URL = srv.URL
	entries, err := c.List(context.Background(), "nat")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 rated entry, got %d (%+v)", len(entries), entries)
	}
	if entries[0].Title != "Demon Slayer" || entries[0].Year != 2019 {
		t.Errorf("bad title/year: %+v", entries[0])
	}
	if entries[0].Score < 8.9 || entries[0].Score > 9.1 {
		t.Errorf("POINT_100 90 should normalize to ~9.0, got %.2f", entries[0].Score)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lib/anilist/... -v`
Expected: FAIL (compile error, `NewClient` undefined).

- [ ] **Step 3: Implement the client**

Create `lib/anilist/client.go`:

```go
// Package anilist is a minimal AniList GraphQL client for a user's public anime
// list and scores, used as a recommendation ranking signal.
package anilist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultURL = "https://graphql.anilist.co"

// Client queries the public AniList GraphQL API. URL is overridable for tests.
type Client struct {
	URL        string
	httpClient *http.Client
}

// NewClient returns an AniList client.
func NewClient() *Client {
	return &Client{URL: defaultURL, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

// Entry is one rated anime from a user's list, score normalized to 0..10.
type Entry struct {
	Title string
	Year  int
	Score float64
}

const listQuery = `query($u:String){
  User(name:$u){ mediaListOptions { scoreFormat } }
  MediaListCollection(userName:$u, type:ANIME){ lists { entries {
    score
    media { seasonYear title { romaji english } }
  } } }
}`

// List returns the user's rated anime (score > 0) with scores normalized to 0..10.
func (c *Client) List(ctx context.Context, username string) ([]Entry, error) {
	reqBody, err := json.Marshal(map[string]any{
		"query":     listQuery,
		"variables": map[string]string{"u": username},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("anilist: HTTP %d: %s", resp.StatusCode, string(data))
	}

	var out struct {
		Data struct {
			User struct {
				MediaListOptions struct {
					ScoreFormat string `json:"scoreFormat"`
				} `json:"mediaListOptions"`
			} `json:"User"`
			MediaListCollection struct {
				Lists []struct {
					Entries []struct {
						Score float64 `json:"score"`
						Media struct {
							SeasonYear int `json:"seasonYear"`
							Title      struct {
								Romaji  string `json:"romaji"`
								English string `json:"english"`
							} `json:"title"`
						} `json:"media"`
					} `json:"entries"`
				} `json:"lists"`
			} `json:"MediaListCollection"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode anilist: %w", err)
	}

	format := out.Data.User.MediaListOptions.ScoreFormat
	var entries []Entry
	for _, l := range out.Data.MediaListCollection.Lists {
		for _, e := range l.Entries {
			if e.Score <= 0 {
				continue
			}
			title := e.Media.Title.English
			if title == "" {
				title = e.Media.Title.Romaji
			}
			if title == "" {
				continue
			}
			entries = append(entries, Entry{
				Title: title,
				Year:  e.Media.SeasonYear,
				Score: normalizeScore(e.Score, format),
			})
		}
	}
	return entries, nil
}

// normalizeScore maps AniList's per-user score format to a 0..10 scale.
func normalizeScore(score float64, format string) float64 {
	switch format {
	case "POINT_100":
		return score / 10.0
	case "POINT_5":
		return score * 2.0
	case "POINT_3":
		return score / 3.0 * 10.0
	default: // POINT_10, POINT_10_DECIMAL
		return score
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/anilist/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/anilist/
git commit -m "feat(anilist): GraphQL client for a user's anime scores

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: SignalSource interface + Trakt adapter

**Files:** Create `lib/recommend/signals.go`, `lib/recommend/signals_test.go`.

**Interfaces:**
- Consumes: `trakt.Client`, `models.OAuthToken`, `models.ExternalSignal`, `models.Movie/TVShow`.
- Produces:
  - `type SignalSource interface { Name() string; Sync(ctx context.Context) (int, error) }`
  - `type traktSource struct { db *gorm.DB; client *trakt.Client }`
  - `func (s *traktSource) Name() string` → `models.SourceTrakt`
  - `func (s *traktSource) Sync(ctx) (int, error)`
  - `func upsertSignal(ctx, db, sig models.ExternalSignal) error`
  - `func matchPlexID(ctx, db, tmdb *int, imdb, tvdb string) (movieID, tvID *uint)`

- [ ] **Step 1: Write the adapter test**

Create `lib/recommend/signals_test.go`:

```go
package recommend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/icco/recommender/lib/trakt"
	"github.com/icco/recommender/models"
)

func TestTraktSource_Sync_joinsAndUpserts(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	tmdb603 := 603
	if err := db.Create(&models.Movie{Title: "The Matrix", Year: 1999, TMDbID: &tmdb603, PlexRatingKey: "m1"}).Error; err != nil {
		t.Fatal(err)
	}
	// Seed a valid, non-expired token so Sync skips refresh.
	if err := db.Create(&models.OAuthToken{Source: models.SourceTrakt, AccessToken: "tok", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/ratings/movies":
			_, _ = w.Write([]byte(`[{"rating":10,"movie":{"ids":{"tmdb":603}}},{"rating":8,"movie":{"ids":{"tmdb":999999}}}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	c := trakt.NewClient("cid", "secret")
	c.BaseURL = srv.URL
	s := &traktSource{db: db, client: c}

	n, err := s.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("expected some signals synced")
	}
	var sigs []models.ExternalSignal
	if err := db.Where("source = ? AND kind = ?", models.SourceTrakt, models.SignalKindRated).Find(&sigs).Error; err != nil {
		t.Fatal(err)
	}
	// Only the tmdb=603 movie is owned; the 999999 one is dropped.
	if len(sigs) != 1 || sigs[0].MovieID == nil || sigs[0].Value != 10 {
		t.Fatalf("bad signals: %+v", sigs)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lib/recommend/... -run TestTraktSource_Sync -v`
Expected: FAIL (undefined `traktSource`).

- [ ] **Step 3: Implement signals.go (interface + Trakt adapter)**

Create `lib/recommend/signals.go`:

```go
package recommend

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/icco/gutil/logging"
	"github.com/icco/recommender/lib/trakt"
	"github.com/icco/recommender/models"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SignalSource fetches external data and upserts ExternalSignal rows joined to
// owned Plex titles. Name is the models.Source* value it writes.
type SignalSource interface {
	Name() string
	Sync(ctx context.Context) (int, error)
}

// upsertSignal inserts or updates a signal on its (source, external_ref, kind) key.
func upsertSignal(ctx context.Context, db *gorm.DB, sig models.ExternalSignal) error {
	sig.UpdatedAt = time.Now()
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source"}, {Name: "external_ref"}, {Name: "kind"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "movie_id", "tvshow_id", "updated_at"}),
	}).Create(&sig).Error
}

// matchPlexID resolves external identifiers to an owned Plex movie or TV show,
// preferring TMDb, then IMDb, then TVDb. Returns (nil, nil) if not owned.
func matchPlexID(ctx context.Context, db *gorm.DB, tmdb *int, imdb, tvdb string, isShow bool) (movieID, tvID *uint) {
	find := func(q *gorm.DB) *uint {
		if isShow {
			var s models.TVShow
			if err := q.First(&s).Error; err == nil {
				return &s.ID
			}
			return nil
		}
		var m models.Movie
		if err := q.First(&m).Error; err == nil {
			return &m.ID
		}
		return nil
	}
	table := db.WithContext(ctx).Model(&models.Movie{})
	if isShow {
		table = db.WithContext(ctx).Model(&models.TVShow{})
	}
	var id *uint
	if tmdb != nil && *tmdb > 0 {
		id = find(table.Session(&gorm.Session{}).Where("tm_db_id = ?", *tmdb))
	}
	if id == nil && imdb != "" {
		id = find(table.Session(&gorm.Session{}).Where("im_db_id = ?", imdb))
	}
	if id == nil && tvdb != "" {
		id = find(table.Session(&gorm.Session{}).Where("tv_db_id = ?", tvdb))
	}
	if isShow {
		return nil, id
	}
	return id, nil
}

// traktSource syncs Trakt watched/ratings/watchlist into ExternalSignal rows.
type traktSource struct {
	db     *gorm.DB
	client *trakt.Client
}

func (s *traktSource) Name() string { return models.SourceTrakt }

// syncPath maps a Trakt sync endpoint to the signal kind it produces.
type syncPath struct {
	path string
	kind string
}

var traktPaths = []syncPath{
	{"sync/watched/movies", models.SignalKindWatched},
	{"sync/watched/shows", models.SignalKindWatched},
	{"sync/ratings/movies", models.SignalKindRated},
	{"sync/ratings/shows", models.SignalKindRated},
	{"sync/watchlist/movies", models.SignalKindWatchlist},
	{"sync/watchlist/shows", models.SignalKindWatchlist},
}

// Sync fetches all Trakt sync endpoints, joins to owned Plex titles, and upserts
// signals. A valid access token is required (see ensureTraktToken).
func (s *traktSource) Sync(ctx context.Context) (int, error) {
	l := logging.FromContext(ctx)
	token, err := s.accessToken(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, sp := range traktPaths {
		rows, err := s.client.Sync(ctx, token, sp.path)
		if err != nil {
			l.Warnw("trakt sync path failed", "path", sp.path, zap.Error(err))
			continue
		}
		for _, row := range rows {
			media := row.Movie
			isShow := false
			if media == nil {
				media = row.Show
				isShow = true
			}
			if media == nil {
				continue
			}
			movieID, tvID := matchPlexID(ctx, s.db, nilIfZero(media.IDs.TMDb), media.IDs.IMDb, itoaOrEmpty(media.IDs.TVDb), isShow)
			if movieID == nil && tvID == nil {
				continue // not owned
			}
			value := 1.0
			if sp.kind == models.SignalKindRated {
				value = float64(row.Rating)
			}
			ref := fmt.Sprintf("%s:%d", sp.kind, media.IDs.Trakt)
			if err := upsertSignal(ctx, s.db, models.ExternalSignal{
				Source: models.SourceTrakt, ExternalRef: ref, Kind: sp.kind,
				MovieID: movieID, TVShowID: tvID, Value: value,
			}); err != nil {
				l.Warnw("upsert trakt signal failed", "ref", ref, zap.Error(err))
				continue
			}
			count++
		}
	}
	return count, nil
}

// accessToken returns a valid Trakt access token, refreshing if expired.
func (s *traktSource) accessToken(ctx context.Context) (string, error) {
	var tok models.OAuthToken
	err := s.db.WithContext(ctx).Where("source = ?", models.SourceTrakt).First(&tok).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("trakt not connected; visit /trakt/connect")
	}
	if err != nil {
		return "", fmt.Errorf("load trakt token: %w", err)
	}
	if time.Now().Before(tok.ExpiresAt.Add(-1 * time.Minute)) {
		return tok.AccessToken, nil
	}
	refreshed, err := s.client.RefreshToken(ctx, tok.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("refresh trakt token: %w", err)
	}
	if err := s.saveToken(ctx, refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

// saveToken upserts the Trakt token row.
func (s *traktSource) saveToken(ctx context.Context, tok *trakt.Token) error {
	row := models.OAuthToken{
		Source: models.SourceTrakt, AccessToken: tok.AccessToken,
		RefreshToken: tok.RefreshToken, ExpiresAt: tok.ExpiresAt(), UpdatedAt: time.Now(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{"access_token", "refresh_token", "expires_at", "updated_at"}),
	}).Create(&row).Error
}

func nilIfZero(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func itoaOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/recommend/... -run TestTraktSource_Sync -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/recommend/signals.go lib/recommend/signals_test.go
git commit -m "feat: SignalSource interface and Trakt adapter with token refresh

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: AniList adapter

**Files:** Modify `lib/recommend/signals.go`, `lib/recommend/signals_test.go`.

**Interfaces:**
- Produces: `type anilistSource struct { db *gorm.DB; client *anilist.Client; username string }`; `Name()` → `models.SourceAniList`; `Sync(ctx) (int, error)`.

- [ ] **Step 1: Write the test**

Add to `lib/recommend/signals_test.go`:

```go
func TestAniListSource_Sync_matchesByTitleYear(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Create(&models.TVShow{Title: "Demon Slayer", Year: 2019, PlexRatingKey: "s1"}).Error; err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"User":{"mediaListOptions":{"scoreFormat":"POINT_10"}},
			"MediaListCollection":{"lists":[{"entries":[
				{"score":9,"media":{"seasonYear":2019,"title":{"romaji":"Kimetsu","english":"Demon Slayer"}}},
				{"score":9,"media":{"seasonYear":1990,"title":{"romaji":"Nope","english":"Not Owned"}}}
			]}]}}}}`))
	}))
	defer srv.Close()

	c := anilist.NewClient()
	c.URL = srv.URL
	s := &anilistSource{db: db, client: c, username: "nat"}
	n, err := s.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 matched signal, got %d", n)
	}
	var sigs []models.ExternalSignal
	db.Where("source = ?", models.SourceAniList).Find(&sigs)
	if len(sigs) != 1 || sigs[0].TVShowID == nil || sigs[0].Value != 9 {
		t.Fatalf("bad anilist signals: %+v", sigs)
	}
}
```

Add `"github.com/icco/recommender/lib/anilist"` to the test's imports.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lib/recommend/... -run TestAniListSource_Sync -v`
Expected: FAIL (undefined `anilistSource`).

- [ ] **Step 3: Implement the AniList adapter**

Add to `lib/recommend/signals.go` (add `"github.com/icco/recommender/lib/anilist"` and `"strings"` to imports):

```go
// anilistSource syncs a user's AniList anime scores, matched to owned Plex titles
// by title + year.
type anilistSource struct {
	db       *gorm.DB
	client   *anilist.Client
	username string
}

func (s *anilistSource) Name() string { return models.SourceAniList }

// Sync fetches the AniList list and upserts score signals for titles owned in Plex.
func (s *anilistSource) Sync(ctx context.Context) (int, error) {
	l := logging.FromContext(ctx)
	entries, err := s.client.List(ctx, s.username)
	if err != nil {
		return 0, fmt.Errorf("anilist list: %w", err)
	}
	count := 0
	for _, e := range entries {
		movieID, tvID := matchByTitleYear(ctx, s.db, e.Title, e.Year)
		if movieID == nil && tvID == nil {
			continue
		}
		ref := fmt.Sprintf("score:%s:%d", strings.ToLower(e.Title), e.Year)
		if err := upsertSignal(ctx, s.db, models.ExternalSignal{
			Source: models.SourceAniList, ExternalRef: ref, Kind: models.SignalKindScore,
			MovieID: movieID, TVShowID: tvID, Value: e.Score,
		}); err != nil {
			l.Warnw("upsert anilist signal failed", "ref", ref, zap.Error(err))
			continue
		}
		count++
	}
	l.Infow("anilist sync", "entries", len(entries), "matched", count)
	return count, nil
}

// matchByTitleYear finds an owned Plex title by case-insensitive title + year,
// checking TV shows first (anime are usually series), then movies.
func matchByTitleYear(ctx context.Context, db *gorm.DB, title string, year int) (movieID, tvID *uint) {
	var show models.TVShow
	if err := db.WithContext(ctx).Where("title = ? COLLATE NOCASE AND year = ?", title, year).First(&show).Error; err == nil {
		return nil, &show.ID
	}
	var movie models.Movie
	if err := db.WithContext(ctx).Where("title = ? COLLATE NOCASE AND year = ?", title, year).First(&movie).Error; err == nil {
		return &movie.ID, nil
	}
	return nil, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/recommend/... -run 'TestAniListSource_Sync|TestTraktSource_Sync' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/recommend/signals.go lib/recommend/signals_test.go
git commit -m "feat: AniList adapter matching anime scores to owned Plex titles

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: SyncSignals + wiring (New config, HandleCache, main)

**Files:** Modify `lib/recommend/signals.go`, `lib/recommend/recommend.go`, `handlers/handlers.go`, `main.go`.

**Interfaces:**
- Produces:
  - `type SignalConfig struct { TraktClientID, TraktClientSecret, AniListUsername string }`
  - `New(db, plex, tmdb, chat, model, sigCfg SignalConfig)` (extended signature)
  - `Recommender` fields `sigCfg SignalConfig`
  - `func (r *Recommender) SyncSignals(ctx context.Context)` — best-effort over configured sources
  - `func (r *Recommender) traktClient() *trakt.Client` (nil if unconfigured)
  - `HandleCache(p *plex.Client, r *recommend.Recommender, fl *lock.FileLock)`

- [ ] **Step 1: Add SignalConfig + SyncSignals**

In `lib/recommend/recommend.go`, add the field and extend `New`:

```go
type Recommender struct {
	db     *gorm.DB
	plex   *plex.Client
	tmdb   *tmdb.Client
	chat   Chatter
	model  string
	sigCfg SignalConfig
}

func New(db *gorm.DB, plexClient *plex.Client, tmdbClient *tmdb.Client, chat Chatter, model string, sigCfg SignalConfig) (*Recommender, error) {
	return &Recommender{db: db, plex: plexClient, tmdb: tmdbClient, chat: chat, model: model, sigCfg: sigCfg}, nil
}
```

In `lib/recommend/signals.go`, add (import `"github.com/icco/recommender/lib/anilist"` already present; add nothing new):

```go
// SignalConfig holds credentials/usernames for external signal sources. Empty
// fields disable that source.
type SignalConfig struct {
	TraktClientID     string
	TraktClientSecret string
	AniListUsername   string
}

// traktClient returns a Trakt client if credentials are configured, else nil.
func (r *Recommender) traktClient() *trakt.Client {
	if r.sigCfg.TraktClientID == "" || r.sigCfg.TraktClientSecret == "" {
		return nil
	}
	return trakt.NewClient(r.sigCfg.TraktClientID, r.sigCfg.TraktClientSecret)
}

// configuredSources returns the enabled signal sources.
func (r *Recommender) configuredSources() []SignalSource {
	var out []SignalSource
	if c := r.traktClient(); c != nil {
		out = append(out, &traktSource{db: r.db, client: c})
	}
	if r.sigCfg.AniListUsername != "" {
		out = append(out, &anilistSource{db: r.db, client: anilist.NewClient(), username: r.sigCfg.AniListUsername})
	}
	return out
}

// SyncSignals runs every configured source best-effort; failures are logged.
func (r *Recommender) SyncSignals(ctx context.Context) {
	l := logging.FromContext(ctx)
	for _, src := range r.configuredSources() {
		n, err := src.Sync(ctx)
		if err != nil {
			l.Warnw("signal source sync failed", "source", src.Name(), zap.Error(err))
			continue
		}
		l.Infow("signal source synced", "source", src.Name(), "count", n)
	}
}
```

- [ ] **Step 2: Update main.go wiring**

In `main.go`, after `geminiModel` is resolved and before `recommend.New`, read env and build config:

```go
	sigCfg := recommend.SignalConfig{
		TraktClientID:     os.Getenv("TRAKT_CLIENT_ID"),
		TraktClientSecret: os.Getenv("TRAKT_CLIENT_SECRET"),
		AniListUsername:   os.Getenv("ANILIST_USERNAME"),
	}
```

Change the constructor call to `recommend.New(gormDB, plexClient, tmdbClient, chat, geminiModel, sigCfg)`. Change the cache route to `r.Get("/cron/cache", handlers.HandleCache(plexClient, recommender, fileLock))`.

- [ ] **Step 3: Run SyncSignals from HandleCache**

In `handlers/handlers.go`, change the signature to `func HandleCache(p *plex.Client, rec *recommend.Recommender, fl *lock.FileLock) http.HandlerFunc` and, inside the background goroutine, after the successful `p.UpdateCache(bgCtx)` block, add a signal sync. Replace:

```go
			if err := p.UpdateCache(bgCtx); err != nil {
				l.Errorw("Failed to update cache", zap.Error(err))
			} else {
				l.Infow("Cache update completed successfully",
					"duration", time.Since(startTime),
				)
			}
```

with:

```go
			if err := p.UpdateCache(bgCtx); err != nil {
				l.Errorw("Failed to update cache", zap.Error(err))
			} else {
				l.Infow("Cache update completed successfully",
					"duration", time.Since(startTime),
				)
				rec.SyncSignals(bgCtx)
			}
```

- [ ] **Step 4: Build and test**

Run: `go build ./... && go test ./lib/recommend/... ./...`
Expected: build OK; tests PASS. (Existing recommend tests construct `Recommender` via struct literal, so the `New` signature change doesn't break them; `main.go` is the only `New` caller.)

- [ ] **Step 5: Commit**

```bash
git add lib/recommend/recommend.go lib/recommend/signals.go handlers/handlers.go main.go
git commit -m "feat: SyncSignals wired into cache cron and app config

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Trakt device-flow connect endpoint

**Files:** Modify `lib/recommend/signals.go`, `handlers/handlers.go`, `main.go`; Test `lib/recommend/signals_test.go`.

**Interfaces:**
- Produces:
  - `func (r *Recommender) TraktConnect(ctx) (userCode, verificationURL string, err error)` — starts device flow, spawns a background poll that stores the token.
  - `func (r *Recommender) storeTraktToken(ctx, *trakt.Token) error` (used by the poll; wraps `traktSource.saveToken`).
  - `HandleTraktConnect(r *recommend.Recommender) http.HandlerFunc` → `GET /trakt/connect`.

- [ ] **Step 1: Write a token-store test**

Add to `lib/recommend/signals_test.go`:

```go
func TestStoreTraktToken_upserts(t *testing.T) {
	db := testDB(t)
	r := &Recommender{db: db, sigCfg: SignalConfig{TraktClientID: "a", TraktClientSecret: "b"}}
	ctx := context.Background()
	if err := r.storeTraktToken(ctx, &trakt.Token{AccessToken: "x", RefreshToken: "y", ExpiresIn: 3600, CreatedAt: 1700000000}); err != nil {
		t.Fatal(err)
	}
	var tok models.OAuthToken
	if err := db.Where("source = ?", models.SourceTrakt).First(&tok).Error; err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "x" {
		t.Fatalf("bad token: %+v", tok)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lib/recommend/... -run TestStoreTraktToken -v`
Expected: FAIL (undefined `storeTraktToken`).

- [ ] **Step 3: Implement TraktConnect + storeTraktToken**

Add to `lib/recommend/signals.go` (add `"time"` already imported):

```go
// storeTraktToken persists a Trakt token set.
func (r *Recommender) storeTraktToken(ctx context.Context, tok *trakt.Token) error {
	return (&traktSource{db: r.db}).saveToken(ctx, tok)
}

// TraktConnect starts the OAuth device flow and returns the user code + URL to
// visit. A background goroutine polls until authorized and stores the token.
func (r *Recommender) TraktConnect(ctx context.Context) (userCode, verificationURL string, err error) {
	client := r.traktClient()
	if client == nil {
		return "", "", fmt.Errorf("trakt not configured (set TRAKT_CLIENT_ID/SECRET)")
	}
	dc, err := client.RequestDeviceCode(ctx)
	if err != nil {
		return "", "", fmt.Errorf("request device code: %w", err)
	}
	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	//nolint:contextcheck // background poll must outlive the request
	go func() {
		bg := logging.NewContext(context.Background(), logging.FromContext(ctx))
		l := logging.FromContext(bg)
		for time.Now().Before(deadline) {
			time.Sleep(interval)
			tok, perr := client.PollForToken(bg, dc.DeviceCode)
			if perr != nil {
				l.Warnw("trakt poll failed", zap.Error(perr))
				return
			}
			if tok == nil {
				continue // pending
			}
			if serr := r.storeTraktToken(bg, tok); serr != nil {
				l.Errorw("store trakt token failed", zap.Error(serr))
				return
			}
			l.Infow("trakt connected; token stored")
			return
		}
		l.Warnw("trakt device code expired before authorization")
	}()

	return dc.UserCode, dc.VerificationURL, nil
}
```

> `time.Sleep` in the poll loop is acceptable here (a detached background goroutine, not a request handler). If `revive`/`gocritic` flags it, keep it — it's the documented device-flow poll cadence.

- [ ] **Step 4: Add the handler and route**

In `handlers/handlers.go`, add:

```go
// HandleTraktConnect starts the Trakt OAuth device flow and returns the code to enter.
func HandleTraktConnect(r *recommend.Recommender) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 15*time.Second)
		defer cancel()
		code, url, err := r.TraktConnect(ctx)
		if err != nil {
			writeError(w, req, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(w, `{"message":"Go to %s and enter code %s","user_code":"%s","verification_url":"%s"}`,
			url, code, code, url); err != nil {
			logging.FromContext(ctx).Errorw("write trakt connect response", zap.Error(err))
		}
	}
}
```

In `main.go`, register: `r.Get("/trakt/connect", handlers.HandleTraktConnect(recommender))`.

- [ ] **Step 5: Run tests + build**

Run: `go test ./lib/recommend/... -run TestStoreTraktToken -v && go build ./...`
Expected: PASS + build OK.

- [ ] **Step 6: Commit**

```bash
git add lib/recommend/signals.go lib/recommend/signals_test.go handlers/handlers.go main.go
git commit -m "feat: /trakt/connect device-flow endpoint

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Genre affinity blends signals

**Files:** Modify `lib/recommend/profile.go`; Test `lib/recommend/profile_test.go`.

**Interfaces:**
- Consumes: `models.ExternalSignal` (`rated`/`score` kinds with `MovieID`/`TVShowID`).
- Produces: `genreAffinity` now adds signal-weighted genre affinity (same normalized 0..1 return).

- [ ] **Step 1: Write the test**

Add to `lib/recommend/profile_test.go`:

```go
func TestGenreAffinity_blendsRatedSignals(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := context.Background()

	// One unwatched Comedy and one unwatched Horror, no Plex views/ratings.
	comedy := models.Movie{Title: "C", Genre: "Comedy", Rating: 0, ViewCount: 0, PlexRatingKey: "a"}
	horror := models.Movie{Title: "H", Genre: "Horror", Rating: 0, ViewCount: 0, PlexRatingKey: "b"}
	db.Create(&comedy)
	db.Create(&horror)
	// A high external rating on the comedy should lift Comedy above Horror.
	db.Create(&models.ExternalSignal{Source: models.SourceTrakt, ExternalRef: "rated:1", Kind: models.SignalKindRated, MovieID: &comedy.ID, Value: 10})

	aff, err := r.genreAffinity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if aff["Comedy"] <= aff["Horror"] {
		t.Errorf("rated signal should lift Comedy (%.2f) above Horror (%.2f)", aff["Comedy"], aff["Horror"])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lib/recommend/... -run TestGenreAffinity_blendsRatedSignals -v`
Expected: FAIL (Comedy == Horror == 0, both zero-rated).

- [ ] **Step 3: Blend signals into genreAffinity**

In `lib/recommend/profile.go`, replace the body of `genreAffinity` between building the Plex `raw` map and the normalization with a version that also folds in signals. Full replacement of the function:

```go
func (r *Recommender) genreAffinity(ctx context.Context) (map[string]float64, error) {
	raw := make(map[string]float64)
	movieGenres := make(map[uint][]string)
	tvGenres := make(map[uint][]string)

	accumulate := func(genres []string, rating float64, viewCount int) {
		for _, g := range genres {
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
		g := splitGenres(m.Genre)
		movieGenres[m.ID] = g
		accumulate(g, m.Rating, m.ViewCount)
	}
	var shows []models.TVShow
	if err := r.db.WithContext(ctx).Find(&shows).Error; err != nil {
		return nil, fmt.Errorf("affinity shows: %w", err)
	}
	for _, s := range shows {
		g := splitGenres(s.Genre)
		tvGenres[s.ID] = g
		accumulate(g, s.Rating, s.ViewCount)
	}

	// Fold in external rated/score signals: a high signal lifts its title's genres.
	var sigs []models.ExternalSignal
	if err := r.db.WithContext(ctx).
		Where("kind IN ?", []string{models.SignalKindRated, models.SignalKindScore}).
		Find(&sigs).Error; err != nil {
		return nil, fmt.Errorf("affinity signals: %w", err)
	}
	for _, sig := range sigs {
		var genres []string
		switch {
		case sig.MovieID != nil:
			genres = movieGenres[*sig.MovieID]
		case sig.TVShowID != nil:
			genres = tvGenres[*sig.TVShowID]
		}
		for _, g := range genres {
			raw[g] += sig.Value / 10.0
		}
	}

	peak := 0.0
	for _, v := range raw {
		if v > peak {
			peak = v
		}
	}
	if peak == 0 {
		return map[string]float64{}, nil
	}
	out := make(map[string]float64, len(raw))
	for g, v := range raw {
		out[g] = v / peak
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/recommend/... -run TestGenreAffinity -v`
Expected: PASS (both the existing Plex test and the new signals test).

- [ ] **Step 5: Commit**

```bash
git add lib/recommend/profile.go lib/recommend/profile_test.go
git commit -m "feat: blend Trakt/AniList ratings into genre affinity

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Watchlist boost + Trakt-watched-as-watched

**Files:** Modify `lib/recommend/candidates.go`; Test `lib/recommend/candidates_test.go`.

**Interfaces:**
- Produces: `candidate.Watchlisted bool`; `scoreCandidate` adds `watchlistBoost` when set; `loadCandidates` marks watchlisted candidates, treats externally-watched movies as watched, and excludes externally-watched TV.
- Consumes: `models.ExternalSignal` (`watchlist`/`watched`).

- [ ] **Step 1: Write tests**

Add to `lib/recommend/candidates_test.go`:

```go
func TestScoreCandidate_watchlistBoost(t *testing.T) {
	base := mkCand(1, 7.0, 0)
	boosted := base
	boosted.Watchlisted = true
	if scoreCandidate(boosted) <= scoreCandidate(base) {
		t.Error("watchlisted candidate should score higher")
	}
}

func TestLoadCandidates_externalWatched(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := t.Context()
	today := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)

	movie := models.Movie{Title: "M", Year: 2000, Rating: 8, ViewCount: 0, PlexRatingKey: "m1"}
	show := models.TVShow{Title: "S", Year: 2001, Rating: 8, ViewCount: 0, PlexRatingKey: "s1"}
	db.Create(&movie)
	db.Create(&show)
	db.Create(&models.ExternalSignal{Source: models.SourceTrakt, ExternalRef: "watched:m", Kind: models.SignalKindWatched, MovieID: &movie.ID, Value: 1})
	db.Create(&models.ExternalSignal{Source: models.SourceTrakt, ExternalRef: "watched:s", Kind: models.SignalKindWatched, TVShowID: &show.ID, Value: 1})

	movies, tv, err := r.loadCandidates(ctx, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(tv) != 0 {
		t.Errorf("externally-watched TV should be excluded, got %d", len(tv))
	}
	if len(movies) != 1 || movies[0].ViewCount == 0 {
		t.Errorf("externally-watched movie should be treated as watched: %+v", movies)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lib/recommend/... -run 'TestScoreCandidate_watchlistBoost|TestLoadCandidates_externalWatched' -v`
Expected: FAIL (`Watchlisted` undefined; external-watched not handled).

- [ ] **Step 3: Add the field, boost, and signal sets**

In `lib/recommend/candidates.go`, add `Watchlisted bool` to the `candidate` struct (after `Affinity`). Add the constant and boost in `scoreCandidate`:

```go
// watchlistBoost lifts titles the user has explicitly watchlisted externally.
const watchlistBoost = 1.5

func scoreCandidate(c candidate) float64 {
	s := c.Rating / 10.0 * 2.0
	if c.ViewCount == 0 {
		s += 1.0
	}
	s += c.Affinity
	if c.Watchlisted {
		s += watchlistBoost
	}
	return s
}
```

Add a helper to load signal ID sets, and use it in `loadCandidates`. Add near `recentlyRecommendedIDs`:

```go
// signalIDSet returns the Movie and TVShow IDs that have a signal of the given kind.
func (r *Recommender) signalIDSet(ctx context.Context, kind string) (map[uint]struct{}, map[uint]struct{}, error) {
	var sigs []models.ExternalSignal
	if err := r.db.WithContext(ctx).Where("kind = ?", kind).Find(&sigs).Error; err != nil {
		return nil, nil, fmt.Errorf("load %s signals: %w", kind, err)
	}
	m := make(map[uint]struct{})
	tv := make(map[uint]struct{})
	for _, s := range sigs {
		if s.MovieID != nil {
			m[*s.MovieID] = struct{}{}
		}
		if s.TVShowID != nil {
			tv[*s.TVShowID] = struct{}{}
		}
	}
	return m, tv, nil
}
```

In `loadCandidates`, after the `affinityFor` closure, load the sets:

```go
	watchlistMovies, watchlistTV, err := r.signalIDSet(ctx, models.SignalKindWatchlist)
	if err != nil {
		return nil, nil, err
	}
	watchedMovies, watchedTV, err := r.signalIDSet(ctx, models.SignalKindWatched)
	if err != nil {
		return nil, nil, err
	}
```

In the movie append loop, compute an effective view count and watchlist flag:

```go
		vc := m.ViewCount
		if _, w := watchedMovies[m.ID]; w && vc == 0 {
			vc = 1 // treat Trakt-watched as watched
		}
		_, wl := watchlistMovies[m.ID]
		movies = append(movies, candidate{
			ID: m.ID, Type: models.TypeMovie, Title: m.Title, Year: m.Year,
			Rating: m.Rating, Genres: genres, PosterURL: m.PosterURL,
			Runtime: m.Runtime, ViewCount: vc, TMDbID: m.TMDbID,
			Affinity: affinityFor(genres), Watchlisted: wl,
		})
```

In the TV loop, skip externally-watched shows and set the watchlist flag:

```go
		if _, skip := excludeTV[s.ID]; skip {
			continue
		}
		if _, watched := watchedTV[s.ID]; watched {
			continue // watched elsewhere; not a fresh TV pick
		}
		genres := splitGenres(s.Genre)
		_, wl := watchlistTV[s.ID]
		tvshows = append(tvshows, candidate{
			ID: s.ID, Type: models.TypeTVShow, Title: s.Title, Year: s.Year,
			Rating: s.Rating, Genres: genres, PosterURL: s.PosterURL,
			Runtime: s.Seasons, ViewCount: s.ViewCount, TMDbID: s.TMDbID,
			Affinity: affinityFor(genres), Watchlisted: wl,
		})
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/recommend/... -v`
Expected: PASS (new tests + all existing).

- [ ] **Step 5: Commit**

```bash
git add lib/recommend/candidates.go lib/recommend/candidates_test.go
git commit -m "feat: watchlist boost and Trakt-watched handling in candidate loading

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: Prompt context (recently loved)

**Files:** Modify `lib/recommend/profile.go`, `lib/recommend/generate.go`, `lib/recommend/prompts/recommendation.txt`; Test `lib/recommend/profile_test.go`.

**Interfaces:**
- Produces: `func (r *Recommender) lovedTitles(ctx) (string, error)` — a one-line summary of highly-rated owned titles (Value ≥ 8), max 5; `promptData.Loved string`.

- [ ] **Step 1: Write the test**

Add to `lib/recommend/profile_test.go`:

```go
func TestLovedTitles_listsHighlyRated(t *testing.T) {
	db := testDB(t)
	r := testRecommender(db)
	ctx := context.Background()
	m := models.Movie{Title: "Loved Film", Year: 2000, PlexRatingKey: "a"}
	db.Create(&m)
	db.Create(&models.ExternalSignal{Source: models.SourceTrakt, ExternalRef: "rated:1", Kind: models.SignalKindRated, MovieID: &m.ID, Value: 10})

	s, err := r.lovedTitles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "Loved Film") {
		t.Errorf("expected loved summary to include the title, got %q", s)
	}
}
```

Add `"strings"` to the test imports if not present.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./lib/recommend/... -run TestLovedTitles -v`
Expected: FAIL (undefined `lovedTitles`).

- [ ] **Step 3: Implement lovedTitles**

Add to `lib/recommend/profile.go`:

```go
// lovedTitles summarizes up to 5 highly-rated (Value >= 8) owned titles from
// external signals, for prompt context. Empty when there are none.
func (r *Recommender) lovedTitles(ctx context.Context) (string, error) {
	var sigs []models.ExternalSignal
	if err := r.db.WithContext(ctx).
		Where("kind IN ? AND value >= ?", []string{models.SignalKindRated, models.SignalKindScore}, 8.0).
		Order("value DESC").Limit(20).Find(&sigs).Error; err != nil {
		return "", fmt.Errorf("loved signals: %w", err)
	}
	seen := make(map[string]struct{})
	var titles []string
	for _, sig := range sigs {
		var title string
		if sig.MovieID != nil {
			var m models.Movie
			if err := r.db.WithContext(ctx).First(&m, *sig.MovieID).Error; err == nil {
				title = m.Title
			}
		} else if sig.TVShowID != nil {
			var s models.TVShow
			if err := r.db.WithContext(ctx).First(&s, *sig.TVShowID).Error; err == nil {
				title = s.Title
			}
		}
		if title == "" {
			continue
		}
		if _, dup := seen[title]; dup {
			continue
		}
		seen[title] = struct{}{}
		titles = append(titles, title)
		if len(titles) == 5 {
			break
		}
	}
	if len(titles) == 0 {
		return "", nil
	}
	return "Recently loved: " + strings.Join(titles, ", ") + ".", nil
}
```

- [ ] **Step 4: Wire into the prompt**

In `lib/recommend/generate.go`, add `Loved string` to `promptData`. In `renderPrompts`, after computing `profile`, add:

```go
	loved, err := r.lovedTitles(ctx)
	if err != nil {
		logging.FromContext(ctx).Warnw("loved titles failed; continuing without", zap.Error(err))
		loved = ""
	}
```

and pass `Loved: loved` in the `promptData` literal.

In `lib/recommend/prompts/recommendation.txt`, add after the `{{if .Profile}}...{{end}}` block:

```
{{if .Loved}}{{.Loved}}
{{end}}
```

- [ ] **Step 5: Run tests + build**

Run: `go test ./lib/recommend/... -v && go build ./...`
Expected: PASS + build OK.

- [ ] **Step 6: Commit**

```bash
git add lib/recommend/profile.go lib/recommend/generate.go lib/recommend/prompts/recommendation.txt lib/recommend/profile_test.go
git commit -m "feat: fold recently-loved titles into the recommendation prompt

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: Config + docs

**Files:** Modify `README.md`, `template.env`, `CLAUDE.md`. Final verification.

- [ ] **Step 1: template.env**

Append to `template.env`:

```
# Optional signal sources (leave blank to disable)
TRAKT_CLIENT_ID=
TRAKT_CLIENT_SECRET=
ANILIST_USERNAME=
```

- [ ] **Step 2: README.md**

In the env-var table, add rows for `TRAKT_CLIENT_ID`, `TRAKT_CLIENT_SECRET` (no; enables Trakt signals), `ANILIST_USERNAME` (no; enables AniList signals). Add a short "Signal sources" subsection: Trakt needs a registered API app; after deploy, hit `GET /trakt/connect` once and enter the returned code at the Trakt URL to authorize; AniList only needs a public username. Note signals are synced during `/cron/cache` and only ever re-rank owned titles.

- [ ] **Step 3: CLAUDE.md**

Add the three env vars to both env-var lists, and a line under the recommendation-logic section: external signals (Trakt watched/ratings/watchlist, AniList scores) are synced in `/cron/cache` into `ExternalSignal` and consumed as genre affinity, watchlist boost, watched-handling, and prompt context.

- [ ] **Step 4: Full verification**

Run: `go build ./... && go test ./... && gofmt -l . && go vet ./...`
Expected: build OK; all tests PASS; `gofmt -l .` empty; vet clean.

- [ ] **Step 5: golangci-lint (if available)**

Run: `golangci-lint run -E bodyclose,misspell,gosec,errorlint,noctx,contextcheck,nilerr,wastedassign,unparam,copyloopvar,intrange,revive,gocritic,unconvert`
Expected: `0 issues`. Fix any findings (common ones: wrap `%w`, `defer Body.Close()`, `http.NewRequestWithContext`).

- [ ] **Step 6: Commit**

```bash
git add README.md template.env CLAUDE.md
git commit -m "docs: document Trakt and AniList signal sources

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Verification checklist (whole PR)

- [ ] `go build ./...`, `go test ./...`, `gofmt -l .`, `go vet ./...`, golangci-lint all clean.
- [ ] Trakt + AniList unconfigured → app builds/runs; `SyncSignals` is a no-op; recommendations still generate.
- [ ] `TestTraktSource_Sync`, `TestAniListSource_Sync` join by ID / title+year and drop unmatched.
- [ ] `genreAffinity` blends signals; watchlist boost reorders; externally-watched TV excluded and movies treated as watched; `lovedTitles` appears in the prompt.
- [ ] No network in the unit suite (all via httptest / in-memory DB).
- [ ] Manual (needs creds): set `TRAKT_CLIENT_ID/SECRET`, `curl /trakt/connect`, authorize; set `ANILIST_USERNAME`; run `/cron/cache`; confirm `external_signals` populated; run `/cron/recommend`.

## Notes for the implementer

- Tasks 4–7 all touch `signals.go`; implement in order and commit at each boundary.
- The `New` signature changes in Task 6; only `main.go` calls it (tests build `Recommender` literals), so the blast radius is small.
- Keep signal weights (`watchlistBoost`, the `/10` affinity factor) as named constants for tuning.
- Trakt/AniList clients expose `BaseURL`/`URL` fields specifically so tests point them at `httptest` servers — never hit the real APIs in tests.
