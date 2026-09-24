package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnvDefaultsPAT(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{
		"GITHUB_ORG":       "acme",
		"GITHUB_AUTH_MODE": "pat",
		"GITHUB_TOKEN":     "tok",
	}))
	require.NoError(t, err)

	assert.Equal(t, "acme", cfg.Org)
	assert.Equal(t, "tok", cfg.Token)
	assert.Equal(t, "8080", cfg.ListenPort)
	assert.Equal(t, "0 0 * * *", cfg.Schedule)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, AllScanners, cfg.Scanners)
	assert.True(t, cfg.IncludeClosed)
	assert.False(t, cfg.IncludeArchived)
	assert.Equal(t, 5, cfg.Concurrency)
	assert.Equal(t, time.Minute, cfg.MetricsCacheTTL)
	assert.Empty(t, cfg.WebConfigFile)
	assert.Nil(t, cfg.RepoInclude)
	assert.Nil(t, cfg.RepoExclude)
}

func TestFromEnvGitHubApp(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{
		"GITHUB_ORG":                  "acme",
		"GITHUB_AUTH_MODE":            "APP",
		"GITHUB_APP_ID":               "12",
		"GITHUB_APP_INSTALLATION_ID":  "34",
		"GITHUB_APP_PRIVATE_KEY_PATH": "/etc/key.pem",
	}))
	require.NoError(t, err)
	assert.Equal(t, AuthGitHubApp, cfg.AuthMode)
	assert.EqualValues(t, 12, cfg.AppID)
	assert.EqualValues(t, 34, cfg.InstallationID)
	assert.Equal(t, "/etc/key.pem", cfg.PrivateKeyPath)
}

func TestFromEnvOptions(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{
		"GITHUB_ORG":             "acme",
		"GITHUB_AUTH_MODE":       "pat",
		"GITHUB_TOKEN":           "tok",
		"ENABLED_SCANNERS":       " code_scanning, secret_scanning,code_scanning",
		"INCLUDE_CLOSED_ALERTS":  "false",
		"INCLUDE_ARCHIVED_REPOS": "true",
		"REPO_INCLUDE_REGEX":     "^svc-",
		"REPO_EXCLUDE_REGEX":     "-sandbox$",
		"CONCURRENCY":            "9",
		"METRICS_CACHE_TTL":      "90s",
		"WEB_CONFIG_FILE":        "/etc/github-exporter/web-config.yml",
	}))
	require.NoError(t, err)
	assert.Equal(t, 90*time.Second, cfg.MetricsCacheTTL)
	assert.Equal(t, "/etc/github-exporter/web-config.yml", cfg.WebConfigFile)
	assert.Equal(t, []string{ScannerCodeScanning, ScannerSecretScanning}, cfg.Scanners)
	assert.False(t, cfg.IncludeClosed)
	assert.True(t, cfg.IncludeArchived)
	assert.True(t, cfg.RepoInclude.MatchString("svc-api"))
	assert.True(t, cfg.RepoExclude.MatchString("x-sandbox"))
	assert.Equal(t, 9, cfg.Concurrency)
}

func TestFromEnvReportsAllProblems(t *testing.T) {
	_, err := FromEnv(env(map[string]string{
		"GITHUB_AUTH_MODE":   "app",
		"GITHUB_APP_ID":      "abc",
		"ENABLED_SCANNERS":   "nope",
		"REPO_INCLUDE_REGEX": "(",
		"CONCURRENCY":        "0",
	}))
	require.Error(t, err)

	msg := err.Error()
	for _, want := range []string{
		"GITHUB_ORG must be set",
		"GITHUB_APP_ID must be a number",
		"GITHUB_APP_INSTALLATION_ID must be a number",
		"GITHUB_APP_PRIVATE_KEY_PATH must be set",
		`unknown scanner "nope"`,
		"REPO_INCLUDE_REGEX",
		"CONCURRENCY must be a positive integer",
	} {
		assert.Contains(t, msg, want)
	}
}

func TestFromEnvCacheTTL(t *testing.T) {
	base := map[string]string{"GITHUB_ORG": "acme", "GITHUB_AUTH_MODE": "pat", "GITHUB_TOKEN": "t"}
	with := func(ttl string) map[string]string {
		m := map[string]string{"METRICS_CACHE_TTL": ttl}
		for k, v := range base {
			m[k] = v
		}
		return m
	}

	cfg, err := FromEnv(env(with("0")))
	require.NoError(t, err)
	assert.Zero(t, cfg.MetricsCacheTTL, "0 disables the cache")

	for _, bad := range []string{"soon", "-5s", "90"} {
		_, err := FromEnv(env(with(bad)))
		assert.Error(t, err, bad)
	}
}

func TestFromEnvListenPort(t *testing.T) {
	base := map[string]string{"GITHUB_ORG": "acme", "GITHUB_AUTH_MODE": "pat", "GITHUB_TOKEN": "t"}
	with := func(port string) map[string]string {
		m := map[string]string{"LISTEN_PORT": port}
		for k, v := range base {
			m[k] = v
		}
		return m
	}

	cfg, err := FromEnv(env(with("9100")))
	require.NoError(t, err)
	assert.Equal(t, "9100", cfg.ListenPort)

	for _, bad := range []string{"0", "65536", "-1", "http", "127.0.0.1:8080"} {
		_, err := FromEnv(env(with(bad)))
		assert.Error(t, err, bad)
	}
}

func TestFromEnvAuthErrors(t *testing.T) {
	tests := map[string]map[string]string{
		"missing mode":  {"GITHUB_ORG": "acme"},
		"unknown mode":  {"GITHUB_ORG": "acme", "GITHUB_AUTH_MODE": "oauth"},
		"pat no token":  {"GITHUB_ORG": "acme", "GITHUB_AUTH_MODE": "pat"},
		"bad bool flag": {"GITHUB_ORG": "acme", "GITHUB_AUTH_MODE": "pat", "GITHUB_TOKEN": "t", "INCLUDE_CLOSED_ALERTS": "maybe"},
	}
	for name, m := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := FromEnv(env(m))
			assert.Error(t, err)
		})
	}
}
