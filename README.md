# cloudflare-prometheus-exporter-go

A Prometheus exporter for Cloudflare zone analytics, structured after
[YACE](https://github.com/nerdswords/yet-another-cloudwatch-exporter): a plain
Go binary, YAML-configured discovery jobs, and tag-based resource filtering —
no Cloudflare Workers runtime, deployable as an ordinary Kubernetes pod.

It is a rewrite of [cloudflare-prometheus-exporter](https://github.com/talosrobert/cloudflare-prometheus-exporter)
covering a core subset of that project's metrics: zone/account discovery, HTTP
request analytics, and (new) Cloudflare Resource Tagging support for filtering
which zones get scraped. Other metric families from the original (firewall,
SSL certs, Magic Transit, Workers, Logpush, Stream, Images, network analytics)
are not yet ported.

## Features

- Discovers accounts/zones via the Cloudflare REST API (`cloudflare-go` SDK).
- Pulls HTTP request analytics via Cloudflare's GraphQL Analytics API.
- Filters which zones are scraped using Cloudflare's
  [Resource Tagging API](https://developers.cloudflare.com/resource-tagging/),
  with YACE-style `searchTags` job configuration (AND logic, key-only,
  key=value, and negated matches).
- Exposes matched zones' tags as Prometheus labels (`cloudflare_zone_tags_info`).
- Stateless: every scrape queries Cloudflare directly, so it runs as a normal
  Kubernetes Deployment with no coordination between replicas required (run
  exactly one replica per config, same as any pull-based exporter).

## Configuration

Discovery jobs live in a YAML file (default path `/etc/cloudflare-exporter/config.yaml`,
override with `-config`); see [`config.example.yaml`](config.example.yaml).

```yaml
discovery:
  jobs:
    - name: production-zones
      accounts:            # optional; omit to scan every account the token can see
        - "0123456789abcdef0123456789abcdef"
      searchTags:          # optional; omit to scrape every zone in scope
        - key: env
          value: production
        - key: archived
          negate: true      # tag=!archived
server:
  listenAddress: ":9199"
  metricsPath: "/metrics"
  scrapeTimeout: 30s
```

The Cloudflare API token is **not** read from this file — set it via the
`CLOUDFLARE_API_TOKEN` environment variable (in Kubernetes, from a Secret; see
[`k8s/secret.example.yaml`](k8s/secret.example.yaml)).

Command-line flags (all optional):

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `/etc/cloudflare-exporter/config.yaml` | Path to the YAML config file |
| `-analytics-window` | `1m` | Trailing time range of HTTP analytics pulled per scrape |
| `-query-limit` | `10000` | Max GraphQL result rows requested per zone |

### Creating an API Token

The token needs, at minimum:

| Permission | Access |
|------------|--------|
| Zone > Zone | Read |
| Zone > Analytics | Read |
| Account > Account Settings | Read |
| Account Resource Tags (Resource Tagging) | Read |

Exact permission-group labels can shift in the Cloudflare dashboard — verify
against your account's token creation UI.

## Endpoints

| Path | Description |
|------|-------------|
| `/metrics` | Prometheus metrics |
| `/healthz` | Liveness/readiness check |

## Running locally

```sh
export CLOUDFLARE_API_TOKEN=...
go run ./cmd/exporter -config=config.example.yaml
curl localhost:9199/metrics
```

## Running in Kubernetes

```sh
kubectl apply -f k8s/secret.example.yaml   # edit the token first
kubectl apply -f k8s/configmap.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
# optional, requires the Prometheus Operator CRDs:
kubectl apply -f k8s/servicemonitor.example.yaml
```

Build and push the image with the provided `Containerfile`:

```sh
podman build -t <your-registry>/cloudflare-prometheus-exporter-go:latest -f Containerfile .
podman push <your-registry>/cloudflare-prometheus-exporter-go:latest
```

Then point `k8s/deployment.yaml`'s `image:` at your registry.

## Prometheus scrape config

If not using the Prometheus Operator `ServiceMonitor`, a plain `scrape_config`:

```yaml
scrape_configs:
  - job_name: cloudflare-exporter
    static_configs:
      - targets: ["cloudflare-exporter:9199"]
```

## Development

```sh
make build   # go build
make test    # go test ./...
make lint    # gofmt, go vet, golangci-lint
```
