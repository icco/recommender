package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/icco/recommender/lib/recommend"
)

func TestHandleTraktConnect_gate(t *testing.T) {
	rec, err := recommend.New(nil, nil, nil, nil, "test", recommend.SignalConfig{}, "")
	if err != nil {
		t.Fatal(err)
	}

	// No token configured → disabled.
	h := HandleTraktConnect(rec, "")
	w := httptest.NewRecorder()
	h(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/trakt/connect", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("unset token: got %d, want 503", w.Code)
	}

	// Configured token, wrong value → unauthorized (before any device-flow work).
	h = HandleTraktConnect(rec, "secret")
	w = httptest.NewRecorder()
	h(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/trakt/connect?token=nope", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", w.Code)
	}
}
