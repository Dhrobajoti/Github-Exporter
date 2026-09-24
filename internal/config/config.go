// Package config loads and validates the exporter configuration from
// environment variables.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultListenPort is the port used when LISTEN_PORT is not set.
const DefaultListenPort = "8080"

// Names of the supported alert scanners.
const (
	ScannerDependabot     = "dependabot"
	ScannerCodeScanning   = "code_scanning"
	ScannerSecretScanning = "secret_scanning"
)

// AllScanners lists every supported scanner, in the order they are reported.
var AllScanners = []string{ScannerDependabot, ScannerCodeScanning, ScannerSecretScanning}

// Supported values of GITHUB_AUTH_MODE.
const (
	AuthPAT       = "pat"
	AuthGitHubApp = "app"
)

// Config holds the runtime configuration of the exporter.
type Config struct {
	LogLevel   string
	ListenPort string
	Schedule   string

	// MetricsCacheTTL is how long a rendered /metrics response is reused.
	// Zero disables the cache.
	MetricsCacheTTL time.Duration
	// WebConfigFile is the path of an optional TLS / basic-auth file in the
	// Prometheus web configuration format. Empty serves plain HTTP.
	WebConfigFile string

	Org    string
	APIURL string // optional, GitHub Enterprise Server API base URL

	AuthMode       string
	Token          string
	AppID          int64
	InstallationID int64
	PrivateKeyPath string

	Scanners        []string
	IncludeClosed   bool
	IncludeArchived bool
	RepoInclude     *regexp.Regexp
	RepoExclude     *regexp.Regexp
	Concurrency     int
}

// FromEnv builds a Config using lookup to read environment variables.
// All validation problems are reported together.
func FromEnv(lookup func(string) string) (*Config, error) {
	get := func(key, fallback string) string {
		if v := strings.TrimSpace(lookup(key)); v != "" {
			return v
		}
		return fallback
	}

	var errs []error
	cfg := &Config{
		LogLevel:   strings.ToLower(get("LOG_LEVEL", "info")),
		ListenPort: get("LISTEN_PORT", DefaultListenPort),
		Schedule:   get("CRON_SCHEDULE", "0 0 * * *"),

		WebConfigFile: get("WEB_CONFIG_FILE", ""),
		Org:           get("GITHUB_ORG", ""),
		APIURL:        get("GITHUB_API_URL", ""),
		AuthMode:      strings.ToLower(get("GITHUB_AUTH_MODE", "")),
	}

	if port, err := strconv.Atoi(cfg.ListenPort); err != nil || port < 1 || port > 65535 {
		errs = append(errs, errors.New("LISTEN_PORT must be a port number between 1 and 65535"))
	}

	if cfg.Org == "" {
		errs = append(errs, errors.New("GITHUB_ORG must be set"))
	}

	switch cfg.AuthMode {
	case AuthPAT:
		cfg.Token = get("GITHUB_TOKEN", "")
		if cfg.Token == "" {
			errs = append(errs, errors.New("GITHUB_TOKEN must be set for GITHUB_AUTH_MODE=pat"))
		}
	case AuthGitHubApp:
		var err error
		if cfg.AppID, err = strconv.ParseInt(get("GITHUB_APP_ID", ""), 10, 64); err != nil {
			errs = append(errs, errors.New("GITHUB_APP_ID must be a number for GITHUB_AUTH_MODE=app"))
		}
		if cfg.InstallationID, err = strconv.ParseInt(get("GITHUB_APP_INSTALLATION_ID", ""), 10, 64); err != nil {
			errs = append(errs, errors.New("GITHUB_APP_INSTALLATION_ID must be a number for GITHUB_AUTH_MODE=app"))
		}
		cfg.PrivateKeyPath = get("GITHUB_APP_PRIVATE_KEY_PATH", "")
		if cfg.PrivateKeyPath == "" {
			errs = append(errs, errors.New("GITHUB_APP_PRIVATE_KEY_PATH must be set for GITHUB_AUTH_MODE=app"))
		}
	case "":
		errs = append(errs, errors.New("GITHUB_AUTH_MODE must be set (pat or app)"))
	default:
		errs = append(errs, fmt.Errorf("unknown GITHUB_AUTH_MODE %q (expected pat or app)", cfg.AuthMode))
	}

	var err error
	if cfg.MetricsCacheTTL, err = time.ParseDuration(get("METRICS_CACHE_TTL", "1m")); err != nil || cfg.MetricsCacheTTL < 0 {
		errs = append(errs, errors.New("METRICS_CACHE_TTL must be a non-negative duration such as 90s or 2m (0 disables the cache)"))
	}

	if cfg.Scanners, err = parseScanners(get("ENABLED_SCANNERS", strings.Join(AllScanners, ","))); err != nil {
		errs = append(errs, err)
	}

	if cfg.IncludeClosed, err = parseBool(get("INCLUDE_CLOSED_ALERTS", "true")); err != nil {
		errs = append(errs, fmt.Errorf("INCLUDE_CLOSED_ALERTS: %w", err))
	}
	if cfg.IncludeArchived, err = parseBool(get("INCLUDE_ARCHIVED_REPOS", "false")); err != nil {
		errs = append(errs, fmt.Errorf("INCLUDE_ARCHIVED_REPOS: %w", err))
	}

	if cfg.RepoInclude, err = parseRegexp(get("REPO_INCLUDE_REGEX", "")); err != nil {
		errs = append(errs, fmt.Errorf("REPO_INCLUDE_REGEX: %w", err))
	}
	if cfg.RepoExclude, err = parseRegexp(get("REPO_EXCLUDE_REGEX", "")); err != nil {
		errs = append(errs, fmt.Errorf("REPO_EXCLUDE_REGEX: %w", err))
	}

	if cfg.Concurrency, err = strconv.Atoi(get("CONCURRENCY", "5")); err != nil || cfg.Concurrency < 1 {
		errs = append(errs, errors.New("CONCURRENCY must be a positive integer"))
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

func parseScanners(raw string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			continue
		}
		known := false
		for _, k := range AllScanners {
			if name == k {
				known = true
			}
		}
		if !known {
			return nil, fmt.Errorf("ENABLED_SCANNERS: unknown scanner %q (expected any of %s)", name, strings.Join(AllScanners, ","))
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, errors.New("ENABLED_SCANNERS must list at least one scanner")
	}
	return out, nil
}

func parseBool(raw string) (bool, error) {
	return strconv.ParseBool(raw)
}

func parseRegexp(raw string) (*regexp.Regexp, error) {
	if raw == "" {
		return nil, nil
	}
	return regexp.Compile(raw)
}
