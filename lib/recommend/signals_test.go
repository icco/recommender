package recommend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/icco/recommender/lib/anilist"
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
			]}]}}}`))
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
