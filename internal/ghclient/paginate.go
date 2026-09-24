package ghclient

import (
	"context"

	"github.com/google/go-github/v88/github"
)

// PerPage is the page size requested from GitHub (the API maximum).
const PerPage = 100

// Page identifies which page to request. The zero value is the first page.
//
// GitHub uses two schemes: numbered pages (Link header carries page=N) and
// cursors (Link header carries after=TOKEN). Dependabot alerts use cursors, so
// following only page numbers silently truncates results after the first page.
type Page struct {
	Num   int
	After string
}

// Cursor returns the list options carrying the page size and cursor.
func (p Page) Cursor() github.ListCursorOptions {
	return github.ListCursorOptions{PerPage: PerPage, After: p.After}
}

// Offset returns the list options carrying the page number. It is only meant
// for endpoints that embed both option types; the page size is set via Cursor.
func (p Page) Offset() github.ListOptions {
	return github.ListOptions{Page: p.Num}
}

// Paginate calls fetch for every page, passing each item to visit. It follows
// whichever scheme the API answers with and retries around rate limits.
func Paginate[T any](
	ctx context.Context,
	fetch func(Page) ([]T, *github.Response, error),
	visit func(T),
) error {
	var page Page
	for {
		items, resp, err := withRetry(ctx, func() ([]T, *github.Response, error) { return fetch(page) })
		if err != nil {
			return err
		}
		for _, item := range items {
			visit(item)
		}

		switch {
		case resp.After != "":
			page = Page{After: resp.After}
		case resp.NextPage != 0:
			page = Page{Num: resp.NextPage}
		default:
			return nil
		}
	}
}
