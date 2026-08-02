package plex

import (
	"math"
	"testing"
)

func f32(v float32) *float32 { return &v }

// Plex sets `rating` on movies but only `audienceRating` on shows; decoding
// just `rating` left every TV card at 0.0.
func TestSectionMetadataRatingFallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		rating, audience *float32
		want             *float64
	}{
		{"critic wins", f32(8.5), f32(7.1), ptr(8.5)},
		{"falls back to audience", nil, f32(7.5), ptr(7.5)},
		{"critic only", f32(6.4), nil, ptr(6.4)},
		{"neither", nil, nil, nil},
	}
	for _, c := range cases {
		got := sectionMetadataToPlexItem(sectionListMetadata{
			Title: c.name, Rating: c.rating, AudienceRating: c.audience,
		}).Rating
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: got %v, want nil", c.name, *got)
		case c.want != nil && got == nil:
			t.Errorf("%s: got nil, want %v", c.name, *c.want)
		// float32 -> float64 widening is inexact (6.4 -> 6.400000095…);
		// the DB stores float64 and the UI formats %.1f.
		case c.want != nil && math.Abs(*got-*c.want) > 1e-6:
			t.Errorf("%s: got %v, want %v", c.name, *got, *c.want)
		}
	}
}

func ptr(v float64) *float64 { return &v }
