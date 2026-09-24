// Package ghtest provides helpers for testing code that talks to the GitHub API.
package ghtest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/require"
)

// NewClient returns a GitHub client whose requests are served by handler, and
// the base URL of that server (without trailing slash).
func NewClient(t testing.TB, handler http.Handler) (*github.Client, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	base := srv.URL + "/"
	client, err := github.NewClient(github.WithURLs(&base, &base))
	require.NoError(t, err)
	return client, srv.URL
}
