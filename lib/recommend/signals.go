package recommend

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/icco/gutil/logging"
	"github.com/icco/recommender/lib/anilist"
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
		DoUpdates: clause.AssignmentColumns([]string{"value", "movie_id", "tv_show_id", "updated_at"}),
	}).Create(&sig).Error
}

// matchPlexID resolves external identifiers to an owned Plex movie or TV show,
// preferring TMDb, then IMDb, then TVDb. Returns (nil, nil) if not owned.
func matchPlexID(ctx context.Context, db *gorm.DB, tmdb *int, imdb, tvdb string, isShow bool) (movieID, tvID *uint) {
	lookup := func(column string, value any) *uint {
		if isShow {
			var s models.TVShow
			if err := db.WithContext(ctx).Where(column+" = ?", value).First(&s).Error; err == nil {
				return &s.ID
			}
			return nil
		}
		var m models.Movie
		if err := db.WithContext(ctx).Where(column+" = ?", value).First(&m).Error; err == nil {
			return &m.ID
		}
		return nil
	}

	var id *uint
	if tmdb != nil && *tmdb > 0 {
		id = lookup("tm_db_id", *tmdb)
	}
	if id == nil && imdb != "" {
		id = lookup("im_db_id", imdb)
	}
	if id == nil && tvdb != "" {
		id = lookup("tv_db_id", tvdb)
	}
	if id == nil {
		return nil, nil
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
// signals. A valid access token is required (see accessToken).
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
