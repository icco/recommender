package recommend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/icco/recommender/lib/omdb"
	"github.com/icco/recommender/models"
)

// omdbStub serves canned OMDb payloads keyed by IMDb id, and counts lookups.
func omdbStub(t *testing.T, byID map[string]string, calls *int) *omdb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		body, ok := byID[r.URL.Query().Get("i")]
		if !ok {
			body = `{"Response":"False","Error":"Incorrect IMDb ID."}`
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := omdb.NewClient("test-key")
	c.BaseURL = srv.URL
	return c
}

func TestMetascoreSource_Sync_stampsScoresAndMisses(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	seed := []models.Movie{
		{Title: "The Matrix", Year: 1999, IMDbID: "tt0133093", PlexRatingKey: "m1"},
		{Title: "Unknown To OMDb", Year: 2001, IMDbID: "tt9999999", PlexRatingKey: "m2"},
		// No IMDb id at all: nothing to look up, so it must be skipped entirely.
		{Title: "No GUID", Year: 1980, PlexRatingKey: "m3"},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	show := models.TVShow{Title: "Severance", Year: 2022, IMDbID: "tt11280740", PlexRatingKey: "s1"}
	if err := db.Create(&show).Error; err != nil {
		t.Fatal(err)
	}

	calls := 0
	client := omdbStub(t, map[string]string{
		"tt0133093":  `{"Title":"The Matrix","Year":"1999","Type":"movie","Metascore":"73","Response":"True"}`,
		"tt11280740": `{"Title":"Severance","Year":"2022–","Type":"series","Metascore":"N/A","Response":"True"}`,
	}, &calls)

	s := &metascoreSource{db: db, client: client, batch: 10}
	scored, err := s.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if scored != 1 {
		t.Errorf("scored = %d, want 1 (only The Matrix has a Metascore)", scored)
	}
	// Three lookups: two movies with ids plus the show. The GUID-less movie is skipped.
	if calls != 3 {
		t.Errorf("omdb calls = %d, want 3", calls)
	}

	var matrix models.Movie
	if err := db.Where("plex_rating_key = ?", "m1").First(&matrix).Error; err != nil {
		t.Fatal(err)
	}
	if matrix.Metascore == nil || *matrix.Metascore != 73 {
		t.Errorf("matrix Metascore = %v, want 73", matrix.Metascore)
	}
	if matrix.MetascoreAt == nil {
		t.Error("matrix MetascoreAt not stamped")
	}

	// A title OMDb has no record of is still stamped, so the next run spends its
	// budget on titles it has never tried instead of retrying this one.
	var missing models.Movie
	if err := db.Where("plex_rating_key = ?", "m2").First(&missing).Error; err != nil {
		t.Fatal(err)
	}
	if missing.Metascore != nil {
		t.Errorf("unknown title Metascore = %d, want nil", *missing.Metascore)
	}
	if missing.MetascoreAt == nil {
		t.Error("unknown title MetascoreAt not stamped; it would be retried forever")
	}

	// Same for a series Metacritic does not score at the series level.
	var severance models.TVShow
	if err := db.Where("plex_rating_key = ?", "s1").First(&severance).Error; err != nil {
		t.Fatal(err)
	}
	if severance.Metascore != nil {
		t.Errorf("severance Metascore = %d, want nil", *severance.Metascore)
	}
	if severance.MetascoreAt == nil {
		t.Error("severance MetascoreAt not stamped")
	}

	// A second run must find nothing left to do.
	before := calls
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != before {
		t.Errorf("second run made %d extra calls, want 0 (all rows fresh)", calls-before)
	}
}

func TestMetascoreSource_Sync_respectsBatchCap(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	for i := range 5 {
		m := models.Movie{
			Title:         "Movie",
			Year:          2000 + i,
			IMDbID:        "tt000000" + string(rune('1'+i)),
			PlexRatingKey: "m" + string(rune('1'+i)),
		}
		if err := db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}

	calls := 0
	client := omdbStub(t, map[string]string{}, &calls)

	s := &metascoreSource{db: db, client: client, batch: 2}
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("omdb calls = %d, want 2 (batch cap)", calls)
	}
}

// Stale rows must come back for a refresh, and never-checked rows must be
// served first so a backfill finishes before any re-checking starts.
func TestStaleMetascoreRows_prioritizesNeverChecked(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	old := time.Now().Add(-metascoreTTL - time.Hour)
	fresh := time.Now()
	score := 73
	rows := []models.Movie{
		{Title: "Stale", Year: 1990, IMDbID: "tt1", PlexRatingKey: "m1", MetascoreAt: &old},
		{Title: "Never", Year: 1991, IMDbID: "tt2", PlexRatingKey: "m2"},
		{Title: "Fresh", Year: 1992, IMDbID: "tt3", PlexRatingKey: "m3", MetascoreAt: &fresh},
		// Already scored and long past the TTL: a critic score is final, so
		// re-fetching it would just re-spend the quota for the same answer.
		{Title: "Scored", Year: 1993, IMDbID: "tt4", PlexRatingKey: "m4", MetascoreAt: &old, Metascore: &score},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	got, err := staleMetascoreRows[models.Movie](ctx, db, time.Now().Add(-metascoreTTL), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (stale + never; not fresh, not already-scored)", len(got))
	}
	if got[0].Title != "Never" {
		t.Errorf("first row = %q, want %q (never-checked first)", got[0].Title, "Never")
	}
	if got[0].IMDbID != "tt2" {
		t.Errorf("IMDbID = %q, want tt2 — the im_db_id column mapping is wrong", got[0].IMDbID)
	}
	for _, r := range got {
		if r.Title == "Scored" {
			t.Error("an already-scored title was queued for re-fetch; scores are final")
		}
	}
}

// A slow OMDb must not hold the shared cron lock past the 120s gap cron.sh
// leaves before /cron/recommend, or the day's recommendations never generate.
func TestMetascoreSource_Sync_stopsAtBudget(t *testing.T) {
	db := testDB(t)

	for i := range 3 {
		m := models.Movie{
			Title:         "Slow",
			Year:          2000 + i,
			IMDbID:        "tt000000" + string(rune('1'+i)),
			PlexRatingKey: "m" + string(rune('1'+i)),
		}
		if err := db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Serve slower than the budget the run is given.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"Response":"False","Error":"slow"}`))
	}))
	defer srv.Close()

	client := omdb.NewClient("k")
	client.BaseURL = srv.URL
	s := &metascoreSource{db: db, client: client, batch: 10}

	// Shrink the budget for the test by cancelling the parent instead.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := s.Sync(ctx); err != nil {
		t.Fatalf("Sync should end quietly on budget exhaustion, got: %v", err)
	}
	// Well under 3 × the 2s server delay: the run bailed rather than grinding on,
	// and did not sleep through its retry backoff after the deadline passed.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Sync took %s; it should abandon the run when its context expires", elapsed)
	}
}

func TestMetascoreMaps(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	score := 73
	movie := models.Movie{Title: "Scored", Year: 1999, PlexRatingKey: "m1", Metascore: &score}
	unscored := models.Movie{Title: "Unscored", Year: 2000, PlexRatingKey: "m2"}
	show := models.TVShow{Title: "Scored Show", Year: 2022, PlexRatingKey: "s1", Metascore: &score}
	for _, rec := range []any{&movie, &unscored, &show} {
		if err := db.Create(rec).Error; err != nil {
			t.Fatal(err)
		}
	}

	r := &Recommender{db: db}
	movies, shows, err := r.metascoreMaps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := movies[movie.ID]; !ok || got != 73 {
		t.Errorf("movies[%d] = (%d, %v), want (73, true)", movie.ID, got, ok)
	}
	if _, ok := movies[unscored.ID]; ok {
		t.Error("unscored movie should be absent from the map, not present as 0")
	}
	if got, ok := shows[show.ID]; !ok || got != 73 {
		t.Errorf("shows[%d] = (%d, %v), want (73, true)", show.ID, got, ok)
	}
}

// A cache refresh re-upserts every Plex row; the Metascore columns are not in
// the upsert list, so a refresh must not wipe scores already fetched.
func TestMetascoreSurvivesCacheUpsert(t *testing.T) {
	db := testDB(t)

	score := 88
	m := models.Movie{Title: "Keeper", Year: 1999, PlexRatingKey: "m1", Metascore: &score}
	if err := db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}

	// Simulate the cache path updating only the Plex-owned columns.
	if err := db.Model(&models.Movie{}).Where("plex_rating_key = ?", "m1").
		Updates(map[string]any{"rating": 8.1, "view_count": 2}).Error; err != nil {
		t.Fatal(err)
	}

	var got models.Movie
	if err := db.Where("plex_rating_key = ?", "m1").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.Metascore == nil || *got.Metascore != 88 {
		t.Errorf("Metascore = %v after cache update, want 88", got.Metascore)
	}
}

// Guard the schema: the columns the enrichment queries name must actually exist.
func TestMetascoreColumnsMigrate(t *testing.T) {
	db := testDB(t)

	for _, c := range []struct{ table, column string }{
		{"movies", "metascore"},
		{"movies", "metascore_at"},
		{"tv_shows", "metascore"},
		{"tv_shows", "metascore_at"},
		{"recommendations", "metascore"},
		{"recommendations", "metacritic_url"},
	} {
		if !db.Migrator().HasColumn(tableModel(c.table), c.column) {
			t.Errorf("%s.%s missing after migrate", c.table, c.column)
		}
	}
}

func tableModel(table string) any {
	switch table {
	case "movies":
		return &models.Movie{}
	case "tv_shows":
		return &models.TVShow{}
	default:
		return &models.Recommendation{}
	}
}
