package db

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

// TestParamsFilterDropsValues verifies bound parameters are stripped so GORM
// logs placeholders instead of interpolated values (keeping OAuth tokens and
// other secrets out of query traces).
func TestParamsFilterDropsValues(t *testing.T) {
	l := NewGormLogger(zap.NewNop())
	sql := `INSERT INTO "o_auth_tokens" ("access_token","refresh_token") VALUES ($1,$2)`
	gotSQL, gotParams := l.ParamsFilter(context.Background(), sql, "live-access", "live-refresh")
	if gotSQL != sql {
		t.Fatalf("SQL altered: got %q want %q", gotSQL, sql)
	}
	if gotParams != nil {
		t.Fatalf("expected params dropped, got %v", gotParams)
	}
}
