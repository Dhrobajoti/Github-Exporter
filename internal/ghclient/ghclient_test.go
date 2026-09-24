package ghclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github-exporter/internal/config"
	"github-exporter/internal/ghtest"
)

// stubSleep replaces the rate-limit sleep and records the requested waits.
func stubSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	orig := sleep
	sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	t.Cleanup(func() { sleep = orig })
	return &waits
}

func TestPaginateFollowsCursors(t *testing.T) {
	// Dependabot alerts paginate with ?after=<cursor>, not page numbers. Only
	// following NextPage would stop after the first page.
	var calls []string
	var base string
	client, base := ghtest.NewClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after")
		calls = append(calls, after)
		switch after {
		case "":
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/dependabot/alerts?after=c1&per_page=100>; rel="next"`, base))
			fmt.Fprint(w, `[{"number":1},{"number":2}]`)
		case "c1":
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/dependabot/alerts?after=c2&per_page=100>; rel="next"`, base))
			fmt.Fprint(w, `[{"number":3}]`)
		default:
			fmt.Fprint(w, `[{"number":4}]`)
		}
	}))

	var numbers []int
	err := Paginate(context.Background(),
		func(p Page) ([]*github.DependabotAlert, *github.Response, error) {
			return client.Dependabot.ListRepoAlerts(context.Background(), "o", "r", &github.ListAlertsOptions{
				ListCursorOptions: p.Cursor(),
				ListOptions:       p.Offset(),
			})
		},
		func(a *github.DependabotAlert) { numbers = append(numbers, a.GetNumber()) },
	)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3, 4}, numbers)
	assert.Equal(t, []string{"", "c1", "c2"}, calls)
}

func TestPaginateFollowsPageNumbers(t *testing.T) {
	var base string
	client, base := ghtest.NewClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		if page < 3 {
			w.Header().Set("Link", fmt.Sprintf(`<%s/orgs/o/repos?page=%d>; rel="next"`, base, page+1))
		}
		fmt.Fprintf(w, `[{"name":"repo-%d"}]`, page)
	}))

	names, err := ListRepos(context.Background(), client, "o", RepoFilter{})
	require.NoError(t, err)
	assert.Equal(t, []string{"repo-1", "repo-2", "repo-3"}, names)
}

func TestListReposFilters(t *testing.T) {
	client, _ := ghtest.NewClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
			{"name":"svc-api"},
			{"name":"svc-old","archived":true},
			{"name":"svc-sandbox"},
			{"name":"docs"},
			{"name":"svc-broken","disabled":true}
		]`)
	}))

	names, err := ListRepos(context.Background(), client, "o", RepoFilter{
		Include: regexp.MustCompile(`^svc-`),
		Exclude: regexp.MustCompile(`-sandbox$`),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"svc-api"}, names)

	names, err = ListRepos(context.Background(), client, "o", RepoFilter{IncludeArchived: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"svc-api", "svc-old", "svc-sandbox", "docs"}, names)
}

func rateLimitedHandler(calls *atomic.Int32, limitedCalls int32, reset time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= limitedCalls {
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
			return
		}
		fmt.Fprint(w, `[{"name":"ok"}]`)
	}
}

func TestPaginateWaitsOutRateLimit(t *testing.T) {
	waits := stubSleep(t)
	var calls atomic.Int32
	client, _ := ghtest.NewClient(t, rateLimitedHandler(&calls, 1, time.Now().Add(-time.Minute)))

	names, err := ListRepos(context.Background(), client, "o", RepoFilter{})
	require.NoError(t, err)
	assert.Equal(t, []string{"ok"}, names)
	assert.EqualValues(t, 2, calls.Load())
	assert.Len(t, *waits, 1)
}

func TestPaginateGivesUpAfterRepeatedRateLimits(t *testing.T) {
	stubSleep(t)
	var calls atomic.Int32
	client, _ := ghtest.NewClient(t, rateLimitedHandler(&calls, 100, time.Now().Add(-time.Minute)))

	_, err := ListRepos(context.Background(), client, "o", RepoFilter{})
	var rl *github.RateLimitError
	require.ErrorAs(t, err, &rl)
	assert.EqualValues(t, maxRateLimitRetries+1, calls.Load())
}

func TestPaginateDoesNotWaitForDistantReset(t *testing.T) {
	waits := stubSleep(t)
	var calls atomic.Int32
	client, _ := ghtest.NewClient(t, rateLimitedHandler(&calls, 100, time.Now().Add(2*time.Hour)))

	_, err := ListRepos(context.Background(), client, "o", RepoFilter{})
	require.Error(t, err)
	assert.EqualValues(t, 1, calls.Load())
	assert.Empty(t, *waits)
}

func TestPaginateHonoursSecondaryRateLimitRetryAfter(t *testing.T) {
	// go-github itself refuses to send requests until the Retry-After period
	// has passed, so this test really has to wait.
	var waits []time.Duration
	orig := sleep
	sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return orig(ctx, d)
	}
	t.Cleanup(func() { sleep = orig })

	var calls atomic.Int32
	client, _ := ghtest.NewClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"You have exceeded a secondary rate limit.","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`)
			return
		}
		fmt.Fprint(w, `[{"name":"ok"}]`)
	}))

	names, err := ListRepos(context.Background(), client, "o", RepoFilter{})
	require.NoError(t, err)
	assert.Equal(t, []string{"ok"}, names)
	assert.Equal(t, []time.Duration{2 * time.Second}, waits)
}

func errResp(status int, msg string) error {
	return &github.ErrorResponse{Response: &http.Response{StatusCode: status}, Message: msg}
}

func TestClassify(t *testing.T) {
	other := fmt.Errorf("boom")
	tests := []struct {
		name string
		in   error
		want error
	}{
		{"nil", nil, nil},
		{"404", errResp(404, "Secret scanning is disabled on this repository."), ErrNotEnabled},
		{"403 dependabot disabled", errResp(403, "Dependabot alerts are disabled for this repository."), ErrNotEnabled},
		{"403 code scanning off", errResp(403, "Code scanning is not enabled for this repository."), ErrNotEnabled},
		{"403 advanced security", errResp(403, "Advanced Security must be enabled for this repository to use code scanning."), ErrNotEnabled},
		{"403 permissions stay errors", errResp(403, "Resource not accessible by integration"), errResp(403, "Resource not accessible by integration")},
		{"500", errResp(500, "oops"), errResp(500, "oops")},
		{"non-github error", other, other},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.in)
			if tc.want == nil || tc.want == ErrNotEnabled || tc.want == other {
				assert.Equal(t, tc.want, got)
				return
			}
			assert.Equal(t, tc.want.Error(), got.Error())
		})
	}

	t.Run("rate limits pass through", func(t *testing.T) {
		rl := &github.RateLimitError{Message: "x"}
		assert.Same(t, rl, Classify(rl))
	})
}

func TestNewPATSendsBearerToken(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		assert.Equal(t, "/api/v3/orgs/o/repos", r.URL.Path)
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	client, err := New(&config.Config{AuthMode: config.AuthPAT, Token: "fake-token", APIURL: srv.URL})
	require.NoError(t, err)

	_, err = ListRepos(context.Background(), client, "o", RepoFilter{})
	require.NoError(t, err)
	assert.Equal(t, "Bearer fake-token", auth)
}

func writeTestKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "app.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	require.NoError(t, os.WriteFile(path, pemBytes, 0o600))
	return path
}

func TestNewGitHubAppExchangesJWTForInstallationToken(t *testing.T) {
	var tokenRequests atomic.Int32
	var repoAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/app/installations/34/access_tokens":
			tokenRequests.Add(1)
			// The app authenticates itself with a signed JWT.
			assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey"), "expected a JWT, got %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"fake-installation-token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "/api/v3/orgs/acme/repos":
			repoAuth = r.Header.Get("Authorization")
			fmt.Fprint(w, `[{"name":"api"}]`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	// GITHUB_API_URL is given without /api/v3, as GHES admins commonly write it.
	client, err := New(&config.Config{
		AuthMode:       config.AuthGitHubApp,
		AppID:          12,
		InstallationID: 34,
		PrivateKeyPath: writeTestKey(t),
		APIURL:         srv.URL,
	})
	require.NoError(t, err)

	for range 2 {
		names, err := ListRepos(context.Background(), client, "acme", RepoFilter{})
		require.NoError(t, err)
		assert.Equal(t, []string{"api"}, names)
	}
	assert.Equal(t, "token fake-installation-token", repoAuth)
	assert.EqualValues(t, 1, tokenRequests.Load(), "the installation token is cached until it nears expiry")
}

func TestNormalizeAPIURL(t *testing.T) {
	tests := map[string]string{
		"https://ghe.example.com":            "https://ghe.example.com/api/v3",
		"https://ghe.example.com/":           "https://ghe.example.com/api/v3",
		"https://ghe.example.com/api/v3":     "https://ghe.example.com/api/v3",
		"https://ghe.example.com/api/v3/":    "https://ghe.example.com/api/v3",
		"https://api.github.com":             "https://api.github.com",
		"https://api.acme.ghe.com":           "https://api.acme.ghe.com",
		"https://ghe.example.com:8443/proxy": "https://ghe.example.com:8443/proxy/api/v3",
	}
	for in, want := range tests {
		got, err := normalizeAPIURL(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}

	for _, bad := range []string{"ghe.example.com", "/api/v3", ""} {
		_, err := normalizeAPIURL(bad)
		assert.Error(t, err, bad)
	}
}

func TestNewGitHubAppFailsOnMissingKey(t *testing.T) {
	_, err := New(&config.Config{
		AuthMode:       config.AuthGitHubApp,
		AppID:          1,
		InstallationID: 2,
		PrivateKeyPath: t.TempDir() + "/missing.pem",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github app auth")
}

func TestNewRejectsUnknownAuthMode(t *testing.T) {
	_, err := New(&config.Config{AuthMode: "oauth"})
	assert.Error(t, err)
}
