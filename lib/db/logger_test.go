package db

import (
	"strings"
	"testing"
)

func TestRedactSQL(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		redacts bool
	}{
		{
			name:    "oauth token upsert is redacted",
			sql:     `INSERT INTO "o_auth_tokens" ("source","access_token","refresh_token") VALUES ('trakt','live-access','live-refresh')`,
			redacts: true,
		},
		{
			name:    "case-insensitive table match",
			sql:     `SELECT * FROM O_AUTH_TOKENS WHERE source = 'trakt'`,
			redacts: true,
		},
		{
			name:    "ordinary query is untouched",
			sql:     `SELECT * FROM movies WHERE year = 2001`,
			redacts: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSQL(tc.sql)
			if tc.redacts {
				if got == tc.sql {
					t.Fatalf("expected redaction, got original SQL")
				}
				if strings.Contains(got, "live-access") || strings.Contains(got, "live-refresh") {
					t.Fatalf("redacted SQL still leaked token values: %q", got)
				}
			} else if got != tc.sql {
				t.Fatalf("non-sensitive SQL altered: got %q want %q", got, tc.sql)
			}
		})
	}
}
