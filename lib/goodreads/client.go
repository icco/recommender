// Package goodreads reads a user's public bookshelves via the per-shelf RSS
// feed. The official API was retired in December 2020 and issues no new keys;
// the feed needs neither key nor auth for a public profile.
package goodreads

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultURL = "https://www.goodreads.com"

	// ShelfRead carries the taste signal (the user's star ratings).
	ShelfRead = "read"
	// ShelfToRead is the recommendable pool.
	ShelfToRead = "to-read"

	// 100 items per page; an empty channel follows the last one. Bounds the walk
	// in case a feed stops advancing.
	maxPages = 40
)

// Client fetches shelves from Goodreads. URL is overridable for tests.
type Client struct {
	URL        string
	httpClient *http.Client
}

// NewClient returns a Goodreads shelf client.
func NewClient() *Client {
	return &Client{URL: defaultURL, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

// Book is one shelved book as the RSS feed reports it.
type Book struct {
	GoodreadsID   string
	Title         string
	Author        string
	ISBN          string
	AverageRating float64 // community average, native 0-5 scale
	UserRating    int     // the user's own stars 1-5; 0 = unrated
	Year          int
	Pages         int
	CoverURL      string
	Shelves       []string // the user's own shelf names; often empty
	ReadAt        *time.Time
	AddedAt       *time.Time
}

// rssItem mirrors the fields we consume from one <item> in the shelf feed.
type rssItem struct {
	BookID        string `xml:"book_id"`
	Title         string `xml:"title"`
	AuthorName    string `xml:"author_name"`
	ISBN          string `xml:"isbn"`
	AverageRating string `xml:"average_rating"`
	UserRating    string `xml:"user_rating"`
	BookPublished string `xml:"book_published"`
	UserShelves   string `xml:"user_shelves"`
	UserReadAt    string `xml:"user_read_at"`
	UserDateAdded string `xml:"user_date_added"`
	NumPages      string `xml:"book>num_pages"` // nested one level down
	// Largest cover offered; the others are thumbnails.
	LargeImageURL  string `xml:"book_large_image_url"`
	MediumImageURL string `xml:"book_medium_image_url"`
	ImageURL       string `xml:"book_image_url"`
}

type rssFeed struct {
	Items []rssItem `xml:"channel>item"`
}

// Shelf returns every book on one shelf, walking pages until one comes back
// empty. userID is from the profile URL (goodreads.com/user/show/<id>-name) —
// a different namespace from the author id on /author/show/<id>.
func (c *Client) Shelf(ctx context.Context, userID, shelf string) ([]Book, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("goodreads: empty user id")
	}
	var out []Book
	seen := make(map[string]struct{})
	for page := 1; page <= maxPages; page++ {
		items, err := c.page(ctx, userID, shelf, page)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		fresh := 0
		for _, it := range items {
			b, ok := convert(it)
			if !ok {
				continue
			}
			if _, dup := seen[b.GoodreadsID]; dup {
				continue
			}
			seen[b.GoodreadsID] = struct{}{}
			out = append(out, b)
			fresh++
		}
		// An all-duplicate page means the feed stopped advancing.
		if fresh == 0 {
			break
		}
	}
	return out, nil
}

// page fetches and decodes a single RSS page of a shelf.
func (c *Client) page(ctx context.Context, userID, shelf string, page int) ([]rssItem, error) {
	u := fmt.Sprintf("%s/review/list_rss/%s?%s", strings.TrimSuffix(c.URL, "/"),
		url.PathEscape(userID), url.Values{
			"shelf": {shelf},
			"page":  {strconv.Itoa(page)},
		}.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	hc := c.httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("goodreads fetch %s page %d: %w", shelf, page, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("goodreads read %s page %d: %w", shelf, page, err)
	}
	if resp.StatusCode >= 400 {
		// A private or missing profile 404s; that must not read as an empty shelf.
		return nil, fmt.Errorf("goodreads: HTTP %d for shelf %q page %d", resp.StatusCode, shelf, page)
	}

	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("goodreads decode %s page %d: %w", shelf, page, err)
	}
	return feed.Items, nil
}

// convert maps a feed item to a Book, reporting false for rows with no id (can't
// dedupe) or no title (can't display).
func convert(it rssItem) (Book, bool) {
	id := strings.TrimSpace(it.BookID)
	title := strings.TrimSpace(it.Title)
	if id == "" || title == "" {
		return Book{}, false
	}
	b := Book{
		GoodreadsID:   id,
		Title:         title,
		Author:        strings.TrimSpace(it.AuthorName),
		ISBN:          strings.TrimSpace(it.ISBN),
		AverageRating: parseFloat(it.AverageRating),
		UserRating:    int(parseFloat(it.UserRating)),
		Year:          int(parseFloat(it.BookPublished)),
		Pages:         int(parseFloat(it.NumPages)),
		CoverURL:      firstNonEmpty(it.LargeImageURL, it.MediumImageURL, it.ImageURL),
		Shelves:       splitShelves(it.UserShelves),
		ReadAt:        parseTime(it.UserReadAt),
		AddedAt:       parseTime(it.UserDateAdded),
	}
	return b, true
}

// BookURL is the goodreads.com page for a book id.
func BookURL(goodreadsID string) string {
	if strings.TrimSpace(goodreadsID) == "" {
		return ""
	}
	return defaultURL + "/book/show/" + goodreadsID
}

// parseFloat reads a numeric feed field. Missing year, page count, and rating
// all arrive as "", so unparseable yields 0.
func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

var rssTimeLayouts = []string{time.RFC1123Z, time.RFC1123}

// parseTime yields nil when absent or unrecognized; user_read_at is empty for
// most books.
func parseTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range rssTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// splitShelves parses user_shelves, dropping bookkeeping names.
func splitShelves(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		t := strings.TrimSpace(p)
		if t == "" || t == ShelfRead || t == ShelfToRead || t == "currently-reading" {
			continue
		}
		out = append(out, t)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
