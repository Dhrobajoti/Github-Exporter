# GitHub Security Alerts Exporter Helm Chart

Deploys the GitHub Security Alerts Prometheus Exporter (Dependabot, Code Scanning, Secret Scanning) as a Kubernetes Deployment exposing `/metrics`.

## Configuration

| Parameter | Description | Default |
|---|---|---|
| `image.repository` | Container image repository | `ghcr.io/your-org/github-exporter` |
| `image.tag` | Image tag; empty uses the chart `appVersion` | `""` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `replicaCount` | Number of replicas. Each replica queries GitHub on its own, so more replicas only use more API rate limit. | `1` |
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service and container port (exported to the app as `LISTEN_PORT`) | `8080` |
| `env` | Environment variables (see the [main README](../../README.md#2-configure)) | `{}` |
| `envFrom` | Load environment variables from Secrets/ConfigMaps | `[]` |
| `extraVolumes` / `extraVolumeMounts` | Extra volumes, e.g. for the GitHub App private key | `[]` |
| `webConfig.enabled` | Serve HTTPS with basic authentication (see below) | `false` |
| `webConfig.existingSecret` | Secret holding `web-config.yml`, `tls.crt`, `tls.key` | `""` |
| `serviceMonitor.enabled` | Create a Prometheus Operator ServiceMonitor | `false` |
| `serviceMonitor.interval` / `.scrapeTimeout` | Scrape settings | `30s` / `10s` |
| `serviceMonitor.basicAuth.secretName` | Secret with `username` and `password` used to scrape when `webConfig.enabled` | `""` |
| `resources` | Resource requests and limits | 50m / 64Mi requests, 250m / 256Mi limits |
| `podSecurityContext` | Pod security context | non-root (65532), `RuntimeDefault` seccomp |
| `nodeSelector`, `tolerations`, `affinity` | Scheduling | empty |
| `annotations`, `podAnnotations` | Deployment / pod annotations | `{}` |

The pod runs as the image's non-root user (65532) with a read-only root filesystem, and has liveness (`/healthz`) and readiness (`/readyz`) probes. The pod only becomes ready after the first refresh completes. With `webConfig.enabled`, the probes check the TCP port instead, because the health endpoints then require credentials too.

## Authentication

Keep credentials in a Secret and load them with `envFrom` rather than putting them in values.

### Personal Access Token

```sh
kubectl create secret generic github-exporter-token --from-literal=GITHUB_TOKEN=<token>
```

```yaml
env:
  GITHUB_ORG: "my-org"
  GITHUB_AUTH_MODE: "pat"
envFrom:
  - secretRef:
      name: github-exporter-token
```

### GitHub App

```sh
kubectl create secret generic github-app-private-key --from-file=private-key.pem
```

```yaml
env:
  GITHUB_ORG: "my-org"
  GITHUB_AUTH_MODE: "app"
  GITHUB_APP_ID: "1234"
  GITHUB_APP_INSTALLATION_ID: "5678"
  GITHUB_APP_PRIVATE_KEY_PATH: "/etc/github-app/private-key.pem"
extraVolumeMounts:
  - name: github-app-key
    mountPath: /etc/github-app
    readOnly: true
extraVolumes:
  - name: github-app-key
    secret:
      secretName: github-app-private-key
```

## HTTPS and basic authentication

Create a Secret with the web configuration and certificate (see [TLS and authentication](../../README.md#tls-and-authentication) for the file format; refer to the certificate as `/etc/github-exporter/web/tls.crt`):

```sh
kubectl create secret generic github-exporter-web \
  --from-file=web-config.yml --from-file=tls.crt --from-file=tls.key
```

```yaml
webConfig:
  enabled: true
  existingSecret: github-exporter-web
```

## ServiceMonitor

If you use [Prometheus Operator](https://github.com/prometheus-operator/prometheus-operator), set `serviceMonitor.enabled=true`.

When `webConfig.enabled` is set, the ServiceMonitor scrapes over HTTPS with certificate verification disabled and authenticates with the credentials in `serviceMonitor.basicAuth.secretName` (keys `username` and `password`, the plain-text password matching the hash in `web-config.yml`):

```sh
kubectl create secret generic github-exporter-scrape \
  --from-literal=username=prometheus --from-literal=password='<password>'
```

## Usage

```sh
helm install github-exporter ./charts/github-exporter -f my-values.yaml
```

Once the chart is published from your repository's GitHub Pages:

```sh
helm repo add <name> https://<owner>.github.io/github-exporter/
helm install github-exporter <name>/github-exporter -f my-values.yaml
```
