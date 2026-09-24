# GitHub Security Alerts Exporter

A Prometheus exporter that reports the state of GitHub's built-in application security tooling across an entire organization:

- **Dependabot alerts**: vulnerable dependencies
- **Code scanning alerts**: static analysis findings (CodeQL and third-party SARIF tools)
- **Secret scanning alerts**: credentials committed to repositories

Alert counts are exposed per repository, so security posture can be dashboarded, trended and alerted on with the same tooling used for the rest of your infrastructure.

## Features

- Covers every repository in an organization with a single deployment; scanners can be enabled individually.
- Authenticates as a **GitHub App** (recommended; short-lived tokens that refresh automatically) or with a **Personal Access Token**.
- Decoupled from scraping: GitHub is queried on a configurable schedule and `/metrics` is served from memory, so scrape frequency never affects API usage.
- Built for multi-Prometheus environments: rendered responses are cached for a configurable window, so several Prometheus servers scraping at slightly different times cost a single rendering.
- Endpoint protection using the standard Prometheus web configuration file: TLS and bcrypt-hashed basic authentication.
- Consistent data: series disappear when their alerts are fixed or repositories are removed, and a temporary API failure for one repository keeps its last known counts instead of reporting zero.
- Complete results: cursor and page pagination are both followed, so repositories with hundreds of alerts are counted in full.
- Rate-limit aware: waits out GitHub primary and secondary rate limits.
- GitHub Enterprise Server supported.
- Single static binary; minimal distroless container image running as non-root; systemd unit, Docker Compose file and Helm chart provided.

## How it works

```
                 schedule (CRON_SCHEDULE)
                          |
GitHub REST API <---  refresh  --->  in-memory snapshot  --->  /metrics cache  <---  Prometheus (n)
 (repos, alerts)      (bounded            (replaced as a          (METRICS_CACHE_TTL)
                       concurrency)        whole each refresh)
```

Each refresh lists the organization's repositories (applying the archived, include and exclude filters), then runs every enabled scanner against every repository with at most `CONCURRENCY` requests in flight. The results are aggregated into an immutable snapshot that replaces the previous one.

- If fetching a single repository fails, its previous counts are retained and `github_exporter_errors_total` is incremented.
- If the repository list cannot be fetched, or the refresh is interrupted, the previous snapshot is kept as a whole.
- The first refresh runs at start-up and is retried every minute until it succeeds; `/readyz` reports ready once it has.

API usage per refresh is approximately one request per repository per scanner, plus one more for closed alerts on code and secret scanning when `INCLUDE_CLOSED_ALERTS=true`, plus additional pages for repositories with more than 100 alerts. An organization with 500 repositories and all scanners enabled therefore uses roughly 2,500 requests per refresh, against the 5,000 per hour that a token or GitHub App installation is allowed.

## Metrics

| Metric | Labels | Description |
|---|---|---|
| `github_dependabot_alerts` | `repo`, `severity`, `state` | Dependabot alerts |
| `github_code_scanning_alerts` | `repo`, `severity`, `state`, `tool` | Code scanning alerts |
| `github_secret_scanning_alerts` | `repo`, `secret_type`, `state` | Secret scanning alerts |
| `github_exporter_last_refresh_timestamp_seconds` | | Unix time of the last completed refresh |
| `github_exporter_last_refresh_duration_seconds` | | Duration of the last completed refresh |
| `github_exporter_repositories` | | Repositories scanned in the last completed refresh |
| `github_exporter_errors_total` | `scanner` | Failed API fetches (`repos` = repository listing failed) |

Label values:

| Label | Values |
|---|---|
| `severity` (Dependabot) | `critical`, `high`, `medium`, `low` |
| `severity` (code scanning) | the security severity (`critical`, `high`, `medium`, `low`) when GitHub assigns one, otherwise the rule severity (`error`, `warning`, `note`) |
| `state` (Dependabot) | `open`, `fixed`, `dismissed`, `auto_dismissed` |
| `state` (code scanning) | `open`, `fixed`, `dismissed` |
| `state` (secret scanning) | `open`, `resolved` |
| `tool` | the code scanning tool, for example `CodeQL` |
| `secret_type` | the detected secret type, for example `github_personal_access_token`, `aws_access_key_id` |

Example output:

```
# HELP github_dependabot_alerts Number of Dependabot alerts per repository, severity, and state
# TYPE github_dependabot_alerts gauge
github_dependabot_alerts{repo="api",severity="critical",state="open"} 2
github_dependabot_alerts{repo="api",severity="high",state="fixed"} 1
github_code_scanning_alerts{repo="api",severity="high",state="open",tool="CodeQL"} 1
github_secret_scanning_alerts{repo="api",secret_type="github_personal_access_token",state="open"} 1
```

Behavior to be aware of:

- A repository with no alerts of a given type exports **no series** for it rather than `0`. Write queries so that the absence of data means zero, for example `sum(github_secret_scanning_alerts{state="open"}) or vector(0)`.
- A repository on which a feature is not enabled is skipped for that scanner without raising an error.
- Only the type and state of a secret scanning alert are used. The detected secret itself is never stored, logged or exported.

## Configuration

The exporter is configured entirely through environment variables. A commented template is provided in [.env.example](.env.example); it can be used unchanged as a systemd `EnvironmentFile` and as the Docker Compose `.env` file.

### General

| Variable | Default | Description |
|---|---|---|
| `GITHUB_ORG` | *required* | The GitHub organization to scan. |
| `GITHUB_AUTH_MODE` | *required* | `app` or `pat`. |
| `CRON_SCHEDULE` | `0 0 * * *` | When to refresh, in standard 5-field cron syntax (UTC). |
| `LISTEN_PORT` | `8080` | Port for `/metrics`, `/healthz` and `/readyz`. |
| `METRICS_CACHE_TTL` | `1m` | How long a rendered `/metrics` response is reused. `0` disables the cache. See [Response cache](#response-cache). |
| `WEB_CONFIG_FILE` | *(unset)* | Path to a web configuration file enabling TLS and basic authentication. See [TLS and authentication](#tls-and-authentication). |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. Logs are JSON. |
| `GITHUB_API_URL` | github.com | API URL of a GitHub Enterprise Server instance, for example `https://ghe.example.com/api/v3`. |

### Scope

| Variable | Default | Description |
|---|---|---|
| `ENABLED_SCANNERS` | `dependabot,code_scanning,secret_scanning` | Comma-separated list of scanners to run. |
| `INCLUDE_CLOSED_ALERTS` | `true` | Also count fixed, dismissed and resolved alerts. `false` counts open alerts only and needs fewer API calls. |
| `INCLUDE_ARCHIVED_REPOS` | `false` | Scan archived repositories. |
| `REPO_INCLUDE_REGEX` | *(all)* | Only scan repositories whose name matches this regular expression. |
| `REPO_EXCLUDE_REGEX` | *(none)* | Skip repositories whose name matches this regular expression. |
| `CONCURRENCY` | `5` | Maximum number of parallel GitHub API requests. Lower it if secondary rate limits are hit. |

### GitHub App authentication (`GITHUB_AUTH_MODE=app`)

| Variable | Description |
|---|---|
| `GITHUB_APP_ID` | Numeric App ID. |
| `GITHUB_APP_INSTALLATION_ID` | Numeric ID of the App's installation on the organization. |
| `GITHUB_APP_PRIVATE_KEY_PATH` | Path to the App's private key (PEM). |

### Personal Access Token authentication (`GITHUB_AUTH_MODE=pat`)

| Variable | Description |
|---|---|
| `GITHUB_TOKEN` | The token. |

### Required permissions

Read-only access is sufficient for all scanners.

| Scanner | Fine-grained token / GitHub App (repository permission) | Classic token scope |
|---|---|---|
| all | **Metadata**: read | `repo` (or `public_repo` for public repositories only) |
| Dependabot | **Dependabot alerts**: read | `security_events` |
| Code scanning | **Code scanning alerts**: read | `security_events` |
| Secret scanning | **Secret scanning alerts**: read | `security_events` |

The identity must also be allowed to view security alerts in the organization (organization owner, security manager, or a custom role that grants it). Code scanning and secret scanning on private repositories require the corresponding GitHub Code Security and Secret Protection licenses; where a feature is not enabled the repository is skipped for that scanner.

### TLS and authentication

Setting `WEB_CONFIG_FILE` serves all endpoints over HTTPS and requires basic authentication. The file uses the standard Prometheus web configuration format, the same as node_exporter and other exporters:

```yaml
tls_server_config:
  cert_file: /etc/github-exporter/tls.crt
  key_file: /etc/github-exporter/tls.key

basic_auth_users:
  prometheus: $2a$10$...   # bcrypt hash
```

Generate the bcrypt hash with the built-in helper. It reads the password from standard input:

```sh
printf '%s' 'a-long-random-password' | github-exporter hash-password
```

(`htpasswd -nBC 10 "" | tr -d ':\n'` produces an equivalent hash.) The exporter validates the file at start-up and exits with a descriptive error if it is unreadable or contains an invalid hash.

Prometheus then scrapes with the plain-text password. With a self-signed certificate, certificate verification is skipped on the Prometheus side:

```yaml
scrape_configs:
  - job_name: github-exporter
    scheme: https
    metrics_path: /metrics
    scrape_interval: 60s
    tls_config:
      insecure_skip_verify: true
    basic_auth:
      username: prometheus
      password_file: /etc/prometheus/secrets/github-exporter-password
    static_configs:
      - targets: ['exporter.example.com:8080']
```

Without `WEB_CONFIG_FILE` the exporter serves plain HTTP without authentication, which is suitable only for local testing or a trusted network. When it is enabled, `/healthz` and `/readyz` require credentials as well.

### Response cache

Every `/metrics` response is cached in memory for `METRICS_CACHE_TTL` (default one minute). Requests arriving within that window receive the cached copy, and requests arriving at the same moment share one rendering. This protects the exporter when several Prometheus servers, for example one per availability zone, scrape it a few seconds apart.

- The cache is separate from the GitHub refresh: it never causes GitHub API calls. It is cleared automatically whenever a refresh completes, so new data is never held back by the window.
- Cached responses carry `X-Cache: HIT` (or `MISS`) and an `Age` header, which makes the behavior easy to verify with `curl -i`.
- The negotiated format (plain text, OpenMetrics, gzip) is part of the cache key, so every client receives the encoding it asked for.
- Set `METRICS_CACHE_TTL=0` to disable the cache.

## Deployment

| Method | Files |
|---|---|
| systemd | [deploy/systemd/github-exporter.service](deploy/systemd/github-exporter.service), [.env.example](.env.example) |
| Docker Compose | [docker-compose.yml](docker-compose.yml), [.env.example](.env.example) |
| Kubernetes | [Helm chart](charts/github-exporter/README.md) |

All methods use the same configuration: the settings in [.env.example](.env.example), and optional files (GitHub App private key, TLS certificate and key, `web-config.yml`) placed under `/etc/github-exporter/`. Keep these files, and the environment file, out of version control and readable only by the account that runs the exporter.

### Local run

```sh
go build -o github-exporter .

export GITHUB_ORG=<org> GITHUB_AUTH_MODE=pat GITHUB_TOKEN=<token>
./github-exporter

curl -s http://localhost:8080/metrics | grep ^github_
```

### Docker Compose

```sh
cp .env.example .env                                      # edit: organization and credentials
mkdir config                                              # mounted read-only at /etc/github-exporter
cp deploy/web-config.example.yml config/web-config.yml    # edit: bcrypt hash; add tls.crt, tls.key, github-app.pem
sudo chown -R 65532:65532 config && sudo chmod 600 config/*   # the container runs as UID 65532

docker compose up -d --build
docker compose logs -f
```

### systemd

```sh
sudo install -m 0755 github-exporter /usr/local/bin/github-exporter
sudo useradd --system --no-create-home --shell /usr/sbin/nologin github-exporter
sudo install -d -m 0750 -o root -g github-exporter /etc/github-exporter
sudo install -m 0640 -o root -g github-exporter .env.example /etc/github-exporter/github-exporter.env
# place web-config.yml, tls.crt, tls.key and github-app.pem in /etc/github-exporter (root:github-exporter, mode 0640)
sudo install -m 0644 deploy/systemd/github-exporter.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now github-exporter
```

The unit reads its settings from `EnvironmentFile=/etc/github-exporter/github-exporter.env`; change that line (or override it with `systemctl edit`) to use another location.

## Endpoints

| Path | Description |
|---|---|
| `/metrics` | Prometheus metrics (cached, see above). |
| `/healthz` | Returns 200 while the process is running. |
| `/readyz` | Returns 503 until the first refresh has completed, then 200. |

The binary also provides two helper commands:

| Command | Description |
|---|---|
| `github-exporter hash-password` | Reads a password from standard input and prints its bcrypt hash for `web-config.yml`. |
| `github-exporter healthcheck` | Exits 0 if the exporter accepts connections on `LISTEN_PORT`. Used as the container `HEALTHCHECK`; it needs no credentials, so it works with TLS and authentication enabled. |

## Development

Requires Go 1.26 or later.

```sh
go vet ./...
go test ./...
```

Source layout:

```
main.go                     process wiring: configuration, scheduler, HTTP server, shutdown
internal/config             environment variable parsing and validation
internal/ghclient           GitHub client, authentication, pagination, rate-limit retry, repository listing
internal/scanners           one file per alert type behind a common Scanner interface
internal/exporter           snapshot-based Prometheus collector and refresh loop
internal/server             /metrics response cache and health endpoints
internal/ghtest             test helper: GitHub client backed by an httptest server
deploy/                     systemd unit and web configuration template
charts/github-exporter      Helm chart
```

To add another alert type, implement `scanners.Scanner` (`Name`, `Metric`, `Scan`), register it in `scanners.New` and add its name to `config.AllScanners`.
