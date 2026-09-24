// Package ghclient builds an authenticated GitHub API client and provides the
// shared plumbing (pagination, rate-limit retry, error classification) used by
// the alert scanners.
package ghclient

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v88/github"

	"github-exporter/internal/config"
)

const requestTimeout = 30 * time.Second

// New returns a GitHub client for the configured authentication mode.
//
// For GitHub App auth the installation token is refreshed automatically by the
// transport, so a long-running exporter never ends up with an expired token.
func New(cfg *config.Config) (*github.Client, error) {
	opts := []github.ClientOptionsFunc{github.WithTimeout(requestTimeout)}

	apiURL := ""
	if cfg.APIURL != "" {
		var err error
		if apiURL, err = normalizeAPIURL(cfg.APIURL); err != nil {
			return nil, fmt.Errorf("invalid GITHUB_API_URL: %w", err)
		}
		opts = append(opts, github.WithEnterpriseURLs(apiURL, apiURL))
	}

	switch cfg.AuthMode {
	case config.AuthPAT:
		opts = append(opts, github.WithAuthToken(cfg.Token))
	case config.AuthGitHubApp:
		tr, err := ghinstallation.NewKeyFromFile(http.DefaultTransport, cfg.AppID, cfg.InstallationID, cfg.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("github app auth: %w", err)
		}
		if apiURL != "" {
			tr.BaseURL = apiURL
		}
		opts = append(opts, github.WithTransport(tr))
	default:
		return nil, fmt.Errorf("unsupported auth mode %q", cfg.AuthMode)
	}

	return github.NewClient(opts...)
}

// normalizeAPIURL returns the REST API root of a GitHub Enterprise Server
// instance without a trailing slash. "https://ghe.example.com" and
// "https://ghe.example.com/api/v3/" both become "https://ghe.example.com/api/v3".
// The token exchange for GitHub Apps and the REST client must agree on this
// root, so it is computed once.
func normalizeAPIURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%q must be an absolute URL such as https://ghe.example.com/api/v3", raw)
	}

	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, "/api/v3") && !strings.HasPrefix(u.Host, "api.") && !strings.Contains(u.Host, ".api.") {
		path += "/api/v3"
	}
	u.Path = path
	return u.String(), nil
}
