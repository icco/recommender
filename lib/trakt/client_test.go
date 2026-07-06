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
