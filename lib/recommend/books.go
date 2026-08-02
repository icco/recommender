package recommend

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/icco/gutil/logging"
	"github.com/icco/recommender/lib/goodreads"
	"github.com/icco/recommender/models"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// goodreadsSource mirrors the user's Goodreads shelves into the books table.
//
// Unlike the other SignalSources, which write ExternalSignal rows to re-rank
// owned Plex titles, this one owns its candidate pool: the to-read shelf *is*
// the recommendable set, and the read shelf carries the taste signal in its star
// ratings. Books therefore live in their own table rather than in ExternalSignal,
// which can only join to a Movie or TVShow.
type goodreadsSource struct {
	db     *gorm.DB
	client *goodreads.Client
	userID string
}

func (s *goodreadsSource) Name() string { return models.SourceGoodreads }

// goodreadsRatingScale converts Goodreads' 0-5 stars to the 0-10 scale Movie and
// TVShow ratings use, so book candidates score and render on the same axis.
const goodreadsRatingScale = 2.0

// Sync refreshes both shelves. Shelf membership is refreshed on every run
// because a book moves from to-read to read once finished, and a stale row would
// keep recommending a book the user has already read.
func (s *goodreadsSource) Sync(ctx context.Context) (int, error) {
	l := logging.FromContext(ctx)
	now := time.Now()
	count := 0
	for _, shelf := range []string{goodreads.ShelfRead, goodreads.ShelfToRead} {
		books, err := s.client.Shelf(ctx, s.userID, shelf)
		if err != nil {
			// One unreachable shelf shouldn't discard the other: the to-read pool
			// is still usable without a refreshed read shelf, and vice versa.
			l.Warnw("goodreads shelf fetch failed", "shelf", shelf, zap.Error(err))
			continue
		}
		for _, b := range books {
			row := models.Book{
				GoodreadsID: b.GoodreadsID,
				Title:       b.Title,
				Author:      b.Author,
				Year:        b.Year,
				Rating:      b.AverageRating * goodreadsRatingScale,
				UserRating:  b.UserRating,
				Shelf:       shelf,
				Genre:       strings.Join(b.Shelves, ", "),
				CoverURL:    b.CoverURL,
				ISBN:        b.ISBN,
				Pages:       b.Pages,
				ReadAt:      b.ReadAt,
				AddedAt:     b.AddedAt,
				SyncedAt:    now,
			}
			if err := s.upsert(ctx, row); err != nil {
				l.Warnw("upsert book failed", "goodreads_id", b.GoodreadsID, zap.Error(err))
				continue
			}
			count++
		}
		l.Infow("goodreads shelf synced", "shelf", shelf, "books", len(books))
	}
	if count == 0 {
		return 0, fmt.Errorf("goodreads: no books synced for user %q", s.userID)
	}
	return count, nil
}

// upsert inserts or refreshes one book on its Goodreads id.
func (s *goodreadsSource) upsert(ctx context.Context, b models.Book) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "goodreads_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"title", "author", "year", "rating", "user_rating", "shelf",
			"genre", "cover_url", "isbn", "pages", "read_at", "added_at",
			"synced_at", "updated_at",
		}),
	}).Create(&b).Error
}

// authorAffinity computes a normalized (0..1) taste weight per author from the
// read shelf's star ratings.
//
// Books get author affinity where movies and TV get genre affinity, because the
// Goodreads feed carries no genre field — only the user's own shelf names, which
// most accounts leave empty. An author the user rated highly is both available
// and a stronger signal for books than a genre would be.
func (r *Recommender) authorAffinity(ctx context.Context) (map[string]float64, error) {
	var read []models.Book
	if err := r.db.WithContext(ctx).
		Where("shelf = ? AND user_rating > 0", models.ShelfRead).
		Find(&read).Error; err != nil {
		return nil, fmt.Errorf("affinity books: %w", err)
	}

	// Center on 3 stars so a book the user actively disliked pushes its author
	// down instead of merely failing to push them up.
	raw := make(map[string]float64)
	for _, b := range read {
		author := normalizeAuthor(b.Author)
		if author == "" {
			continue
		}
		raw[author] += float64(b.UserRating) - 3.0
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
	for a, v := range raw {
		out[a] = v / peak
	}
	return out, nil
}

// normalizeAuthor lowercases and trims an author name for affinity keying.
func normalizeAuthor(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// loadBookCandidates loads the to-read shelf as candidates, excluding books
// recommended within the last `days` days.
func (r *Recommender) loadBookCandidates(ctx context.Context, date time.Time, days int) ([]candidate, error) {
	exclude, err := r.recentlyRecommendedBookIDs(ctx, date, days)
	if err != nil {
		return nil, err
	}

	aff, err := r.authorAffinity(ctx)
	if err != nil {
		return nil, err
	}

	var books []models.Book
	if err := r.db.WithContext(ctx).Where("shelf = ?", models.ShelfToRead).Find(&books).Error; err != nil {
		return nil, fmt.Errorf("load books: %w", err)
	}

	var out []candidate
	for _, b := range books {
		if _, skip := exclude[b.ID]; skip {
			continue
		}
		out = append(out, candidate{
			ID: b.ID, Type: models.TypeBook, Title: b.Title, Year: b.Year,
			Rating: b.Rating, Genres: splitGenres(b.Genre), PosterURL: b.CoverURL,
			Runtime: b.Pages, Author: b.Author, SourceURL: goodreads.BookURL(b.GoodreadsID),
			Affinity: aff[normalizeAuthor(b.Author)],
		})
	}
	return out, nil
}

// recentlyRecommendedBookIDs returns Book IDs recommended within the last `days` days.
func (r *Recommender) recentlyRecommendedBookIDs(ctx context.Context, date time.Time, days int) (map[uint]struct{}, error) {
	cutoff := date.AddDate(0, 0, -days)
	var recs []models.Recommendation
	if err := r.db.WithContext(ctx).
		Where(`"date" >= ? AND "date" <= ? AND book_id IS NOT NULL`, cutoff, date).
		Find(&recs).Error; err != nil {
		return nil, fmt.Errorf("load recent book recommendations: %w", err)
	}
	out := make(map[uint]struct{})
	for _, rec := range recs {
		if rec.BookID != nil {
			out[*rec.BookID] = struct{}{}
		}
	}
	return out, nil
}

// lovedBooks summarizes up to 5 of the user's highest-rated finished books for
// prompt context. Titles and authors let the model reason about literary taste,
// which the empty genre column cannot.
func (r *Recommender) lovedBooks(ctx context.Context) (string, error) {
	var books []models.Book
	if err := r.db.WithContext(ctx).
		Where("shelf = ? AND user_rating >= 4", models.ShelfRead).
		Order("user_rating DESC, read_at DESC NULLS LAST").
		Limit(5).Find(&books).Error; err != nil {
		return "", fmt.Errorf("loved books: %w", err)
	}
	if len(books) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(books))
	for _, b := range books {
		if b.Author == "" {
			parts = append(parts, b.Title)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s by %s", b.Title, b.Author))
	}
	return "Books the user rated highly: " + strings.Join(parts, "; ") + ".", nil
}

// favoriteAuthors renders the top authors by affinity as a prompt fragment.
func (r *Recommender) favoriteAuthors(ctx context.Context) (string, error) {
	aff, err := r.authorAffinity(ctx)
	if err != nil {
		return "", err
	}
	if len(aff) == 0 {
		return "", nil
	}

	var books []models.Book
	if err := r.db.WithContext(ctx).
		Where("shelf = ? AND user_rating > 0", models.ShelfRead).
		Find(&books).Error; err != nil {
		return "", fmt.Errorf("favorite authors: %w", err)
	}
	// Affinity keys are normalized for matching; recover a display spelling.
	display := make(map[string]string, len(aff))
	for _, b := range books {
		if k := normalizeAuthor(b.Author); k != "" {
			display[k] = strings.TrimSpace(b.Author)
		}
	}

	type av struct {
		a string
		v float64
	}
	avs := make([]av, 0, len(aff))
	for a, v := range aff {
		if v <= 0 {
			continue // only surface authors the user actually liked
		}
		avs = append(avs, av{a, v})
	}
	sort.Slice(avs, func(i, j int) bool {
		if avs[i].v == avs[j].v {
			return avs[i].a < avs[j].a
		}
		return avs[i].v > avs[j].v
	})
	if len(avs) > 5 {
		avs = avs[:5]
	}
	if len(avs) == 0 {
		return "", nil
	}
	names := make([]string, 0, len(avs))
	for _, x := range avs {
		name := display[x.a]
		if name == "" {
			name = x.a
		}
		names = append(names, name)
	}
	return "Favorite authors, most to least: " + strings.Join(names, ", ") + ".", nil
}
