package plex

import "testing"

func TestSameHost(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"identical plex host", "http://plex.local:32400/photo/x.jpg", "http://plex.local:32400", true},
		{"case-insensitive host", "http://Plex.Local:32400/x.jpg", "http://plex.local:32400", true},
		{"different host", "https://attacker.example/collect", "http://plex.local:32400", false},
		{"internal metadata endpoint", "http://169.254.169.254/latest/meta-data/", "http://plex.local:32400", false},
		{"different port", "http://plex.local:9999/x.jpg", "http://plex.local:32400", false},
		{"unparseable image url", "://bad", "http://plex.local:32400", false},
		{"empty host relative", "/library/metadata/1/thumb", "http://plex.local:32400", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameHost(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameHost(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
