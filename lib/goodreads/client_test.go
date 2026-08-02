package goodreads

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// feedItem renders one <item> as the real feed does, with the nested
// <book><num_pages> element and CDATA-wrapped text.
func feedItem(id, title, author string, extra string) string {
	return fmt.Sprintf(`<item>
    <guid><![CDATA[https://www.goodreads.com/review/show/%s]]></guid>
    <title><![CDATA[%s]]></title>
    <book_id>%s</book_id>
    <book_large_image_url><![CDATA[https://i.gr-assets.com/books/%s.jpg]]></book_large_image_url>
    <book id="%s"><num_pages>210</num_pages></book>
    <author_name>%s</author_name>
    %s
  </item>`, id, title, id, id, id, author, extra)
}

func feed(items ...string) string {
	body := ""
	for _, it := range items {
		body += it
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title><![CDATA[Nat's bookshelf: read]]></title>` + body + `</channel></rss>`
}

func TestShelf_parsesFeedFields(t *testing.T) {
	item := feedItem("807968", "A Wizard of Earthsea (Earthsea Cycle, #1)", "Ursula K. Le Guin", `
    <isbn>0544084373</isbn>
    <user_rating>5</user_rating>
    <average_rating>4.03</average_rating>
    <book_published>1968</book_published>
    <user_shelves>fiction, top-ten, read</user_shelves>
    <user_read_at><![CDATA[Mon, 27 Jul 2026 00:00:00 +0000]]></user_read_at>
    <user_date_added><![CDATA[Mon, 27 Jul 2026 20:47:23 -0700]]></user_date_added>`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = fmt.Fprint(w, feed(item))
			return
		}
		_, _ = fmt.Fprint(w, feed())
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, httpClient: srv.Client()}
	books, err := c.Shelf(context.Background(), "12680", ShelfRead)
	if err != nil {
		t.Fatalf("Shelf: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1", len(books))
	}

	b := books[0]
	if b.GoodreadsID != "807968" {
		t.Errorf("GoodreadsID = %q, want 807968", b.GoodreadsID)
	}
	if b.Title != "A Wizard of Earthsea (Earthsea Cycle, #1)" {
		t.Errorf("Title = %q", b.Title)
	}
	if b.Author != "Ursula K. Le Guin" {
		t.Errorf("Author = %q", b.Author)
	}
	if b.AverageRating != 4.03 {
		t.Errorf("AverageRating = %v, want 4.03", b.AverageRating)
	}
	if b.UserRating != 5 {
		t.Errorf("UserRating = %d, want 5", b.UserRating)
	}
	if b.Year != 1968 {
		t.Errorf("Year = %d, want 1968", b.Year)
	}
	if b.Pages != 210 { // nested under <book id=…>
		t.Errorf("Pages = %d, want 210", b.Pages)
	}
	if b.CoverURL != "https://i.gr-assets.com/books/807968.jpg" {
		t.Errorf("CoverURL = %q", b.CoverURL)
	}
	// "read" is bookkeeping, not a genre.
	if len(b.Shelves) != 2 || b.Shelves[0] != "fiction" || b.Shelves[1] != "top-ten" {
		t.Errorf("Shelves = %v, want [fiction top-ten]", b.Shelves)
	}
	if b.ReadAt == nil || b.ReadAt.Year() != 2026 {
		t.Errorf("ReadAt = %v, want a 2026 time", b.ReadAt)
	}
	if b.AddedAt == nil {
		t.Error("AddedAt = nil, want a time")
	}
}

// The to-read shelf routinely omits year, rating, and read date; those must
// decode as zero values rather than failing the page.
func TestShelf_toleratesMissingFields(t *testing.T) {
	item := feedItem("55399", "Goodbye Chinatown", "Kit Fan", `
    <isbn></isbn>
    <user_rating>0</user_rating>
    <average_rating>3.56</average_rating>
    <book_published></book_published>
    <user_shelves>to-read</user_shelves>
    <user_read_at></user_read_at>`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = fmt.Fprint(w, feed(item))
			return
		}
		_, _ = fmt.Fprint(w, feed())
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, httpClient: srv.Client()}
	books, err := c.Shelf(context.Background(), "12680", ShelfToRead)
	if err != nil {
		t.Fatalf("Shelf: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1", len(books))
	}
	b := books[0]
	if b.Year != 0 {
		t.Errorf("Year = %d, want 0 for an absent book_published", b.Year)
	}
	if b.UserRating != 0 {
		t.Errorf("UserRating = %d, want 0", b.UserRating)
	}
	if b.ReadAt != nil {
		t.Errorf("ReadAt = %v, want nil for an empty user_read_at", b.ReadAt)
	}
	if len(b.Shelves) != 0 { // "to-read" is bookkeeping too
		t.Errorf("Shelves = %v, want empty", b.Shelves)
	}
}

func TestShelf_walksPagesUntilEmpty(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		switch page {
		case "1":
			_, _ = fmt.Fprint(w, feed(feedItem("1", "One", "A", ""), feedItem("2", "Two", "B", "")))
		case "2":
			_, _ = fmt.Fprint(w, feed(feedItem("3", "Three", "C", "")))
		default:
			_, _ = fmt.Fprint(w, feed())
		}
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, httpClient: srv.Client()}
	books, err := c.Shelf(context.Background(), "12680", ShelfToRead)
	if err != nil {
		t.Fatalf("Shelf: %v", err)
	}
	if len(books) != 3 {
		t.Fatalf("got %d books, want 3 across two pages", len(books))
	}
	if len(pages) != 3 {
		t.Errorf("requested pages %v, want three requests ending in an empty page", pages)
	}
}

// A feed that ignores the page parameter must not spin for maxPages.
func TestShelf_stopsOnRepeatedPage(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = fmt.Fprint(w, feed(feedItem("1", "One", "A", "")))
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, httpClient: srv.Client()}
	books, err := c.Shelf(context.Background(), "12680", ShelfToRead)
	if err != nil {
		t.Fatalf("Shelf: %v", err)
	}
	if len(books) != 1 {
		t.Errorf("got %d books, want 1 deduped", len(books))
	}
	if requests > 2 {
		t.Errorf("made %d requests, want to stop after the first repeat", requests)
	}
}

// A 404 (private or missing profile) must not read as an empty shelf, which
// would wipe the candidate pool.
func TestShelf_errorsOnHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, httpClient: srv.Client()}
	if _, err := c.Shelf(context.Background(), "12680", ShelfToRead); err == nil {
		t.Fatal("Shelf: want error on HTTP 404, got nil")
	}
}

func TestShelf_rejectsEmptyUserID(t *testing.T) {
	c := NewClient()
	if _, err := c.Shelf(context.Background(), "  ", ShelfToRead); err == nil {
		t.Fatal("Shelf: want error on empty user id, got nil")
	}
}

// Rows with no id or title are dropped without failing the page.
func TestShelf_skipsUnusableItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			_, _ = fmt.Fprint(w, feed())
			return
		}
		_, _ = fmt.Fprint(w, feed(
			`<item><book_id></book_id><title><![CDATA[No ID]]></title></item>`,
			`<item><book_id>42</book_id><title></title></item>`,
			feedItem("7", "Real Book", "Author", ""),
		))
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, httpClient: srv.Client()}
	books, err := c.Shelf(context.Background(), "12680", ShelfToRead)
	if err != nil {
		t.Fatalf("Shelf: %v", err)
	}
	if len(books) != 1 || books[0].GoodreadsID != "7" {
		t.Fatalf("got %+v, want only the complete book", books)
	}
}

func TestBookURL(t *testing.T) {
	if got := BookURL("807968"); got != "https://www.goodreads.com/book/show/807968" {
		t.Errorf("BookURL = %q", got)
	}
	if got := BookURL(""); got != "" {
		t.Errorf("BookURL(\"\") = %q, want empty", got)
	}
}
