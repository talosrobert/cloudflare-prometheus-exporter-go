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
- Pulls HTTP request, DNS query, and firewall/WAF event analytics via Cloudflare's GraphQL Analytics API.
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
`CLOUDFLARE_API_TOKEN` environment variable (in Kubernetes, from a Secret; see the
[Helm chart's `apiToken`](charts/cloudflare-exporter/README.md#values) values).

Command-line flags (all optional):

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `/etc/cloudflare-exporter/config.yaml` | Path to the YAML config file |
| `-analytics-window` | `1m` | Time range each analytics metric is summed over per scrape |
| `-analytics-lag` | `5m` | How far behind "now" the window ends, allowing for Cloudflare's ingestion delay |
| `-query-limit` | `10000` | Max GraphQL result rows requested per zone |
| `-exclude-host` | `false` | Drop the `host` label from `cloudflare_zone_requests_customer_error` — trades detail for lower cardinality on multi-hostname zones |

## Metrics

All `cloudflare_zone_*` analytics metrics are **gauges holding the sum over
the analytics window** for that scrape (Cloudflare's GraphQL API returns
per-window aggregates, not running totals, so they cannot be counters). To
get a per-second rate, divide by `cloudflare_exporter_analytics_window_seconds`:

```promql
cloudflare_zone_requests / on() group_left cloudflare_exporter_analytics_window_seconds
```

Labels on every zone metric: `zone_id`, `zone`.

| Metric | Extra labels | Description |
|--------|--------------|-------------|
| `cloudflare_zone_info` | `account_id`, `account`, `status` | Discovered zone, always 1 |
| `cloudflare_zone_tags_info` | `tag_key`, `tag_value` | One series per resource tag on the zone, always 1 |
| `cloudflare_zone_requests` | | Requests |
| `cloudflare_zone_requests_cached` | | Cached requests |
| `cloudflare_zone_requests_ssl_encrypted` | | SSL-encrypted requests |
| `cloudflare_zone_requests_content_type` | `content_type` | Requests by content type |
| `cloudflare_zone_requests_country` | `country` | Requests by country |
| `cloudflare_zone_requests_status` | `status` | Requests by HTTP status code |
| `cloudflare_zone_requests_browser_map_page_views` | `family` | Page views by browser family |
| `cloudflare_zone_requests_ip_class` | `ip_type` | Requests by IP classification |
| `cloudflare_zone_requests_ssl_protocol` | `ssl_protocol` | Requests by TLS version |
| `cloudflare_zone_requests_http_version` | `http_version` | Requests by HTTP version |
| `cloudflare_zone_bandwidth_bytes` | | Bandwidth |
| `cloudflare_zone_bandwidth_cached_bytes` | | Cached bandwidth |
| `cloudflare_zone_bandwidth_ssl_encrypted_bytes` | | SSL-encrypted bandwidth |
| `cloudflare_zone_bandwidth_content_type_bytes` | `content_type` | Bandwidth by content type |
| `cloudflare_zone_bandwidth_country_bytes` | `country` | Bandwidth by country |
| `cloudflare_zone_threats` | | Threats |
| `cloudflare_zone_threats_country` | `country` | Threats by country |
| `cloudflare_zone_threats_type` | `type` | Threats by type |
| `cloudflare_zone_pageviews` | | Page views |
| `cloudflare_zone_uniques` | | Unique visitors |
| `cloudflare_zone_cache_hit_ratio` | | Cached requests / requests |
| `cloudflare_zone_dns_queries` | | DNS queries |
| `cloudflare_zone_dns_queries_type` | `query_type` | DNS queries by query type (A, AAAA, MX, ...) |
| `cloudflare_zone_dns_queries_response_code` | `response_code` | DNS queries by response code (NOERROR, NXDOMAIN, ...) |
| `cloudflare_zone_firewall_events` | | Firewall/WAF events |
| `cloudflare_zone_firewall_events_action` | `action` | Firewall/WAF events by action (block, challenge, log, skip, ...) |
| `cloudflare_zone_firewall_events_source` | `source` | Firewall/WAF events by triggering product (waf, botManagement, rateLimit, ...) |
| `cloudflare_zone_firewall_events_rule` | `rule_id` | Firewall/WAF events by rule ID — **high cardinality**, one series per distinct rule seen in the window |
| `cloudflare_zone_firewall_events_country` | `country` | Firewall/WAF events by client country |
| `cloudflare_zone_firewall_result_truncated` | (zone labels) | 1 if that zone's WAF event result hit `-query-limit` in this scrape |
| `cloudflare_zone_requests_customer_error` | `status`, `country`, `host`* | Requests with an edge 4xx/5xx response — **high cardinality**, one series per distinct (status, country, host) combination seen in the window; drop the `host` label with `-exclude-host` |
| `cloudflare_zone_error_ratio` | `side` | 4xx/5xx error ratio, by side: `edge` (edge 4xx/5xx responses / total requests) or `origin` (origin 4xx/5xx responses / requests that reached the origin, excludes edge cache hits, edge blocks, and anything else never sent to the origin) |
| `cloudflare_zone_origin_response_duration_seconds` | | Average origin response duration, weighted by request count, across requests that reached the origin |
| `cloudflare_zone_error_result_truncated` | (zone labels) | 1 if that zone's error/latency analytics result hit `-query-limit` in this scrape |

\* `host` is omitted when `-exclude-host` is set; matching rows are merged instead of dropped.

Exporter self-metrics:

| Metric | Labels | Description |
|--------|--------|-------------|
| `cloudflare_exporter_analytics_window_seconds` | | Configured `-analytics-window` |
| `cloudflare_exporter_job_success` | `job` | 1 if the job's last scrape succeeded, else 0 |
| `cloudflare_exporter_scrape_errors_total` | `job` | Cumulative failed scrapes per job |
| `cloudflare_exporter_api_errors_total` | `account_id`, `api` | Failed calls to an optional, separately permissioned API (`dns_analytics`, `resource_tagging`); non-fatal |
| `cloudflare_exporter_dns_result_truncated` | `account_id` | 1 if DNS results hit `-query-limit`, meaning DNS metrics are undercounted |
| `cloudflare_exporter_dns_unmatched_groups` | `account_id` | DNS rows skipped because their zone was not in scope (see internal zones below) |

### WAF/firewall event labels

`source` and `kind` (not currently exposed as a label) classify which Cloudflare
security product generated the event (WAF managed/custom rules, Bot
Management, rate limiting, etc.). Their exact string values are not filtered
or hardcoded here — this exporter could not sample real event data with
non-empty `source` values during development, so guessing a filter value
risked silently returning zero rows. Use `source` as a label in PromQL instead
of expecting this exporter to pre-filter by product.

### Origin vs. edge errors

`cloudflare_zone_error_ratio{side="origin"}` and `cloudflare_zone_origin_response_duration_seconds`
only consider requests that actually reached the origin. Cloudflare reports an origin status
of `0` for requests it never forwarded — edge cache hits, edge/WAF blocks, redirects, and the
like — and those rows are excluded rather than counted as origin successes, which would
otherwise dilute both metrics with traffic the origin never saw. `cloudflare_zone_error_ratio{side="edge"}`
has no such exclusion: it's edge 4xx/5xx over all requests, from the same data already used
for `cloudflare_zone_requests_status`, at no extra API cost.

### Sampling

Cloudflare's `*Adaptive*` datasets — behind the WAF, DNS, and error/latency metrics here —
are [adaptively sampled](https://developers.cloudflare.com/analytics/sampling/): under load,
only a fraction of events is stored, and each stored event carries a `sampleInterval` (the
reciprocal of its inclusion probability). The raw `count` in those datasets is the number of
*sampled records*, so this exporter reports `count × avg(sampleInterval)` per group — the
same estimate Cloudflare's own dashboard shows. Verified live: a low-traffic zone already
returned a group with `count=44502` and `sampleInterval≈1.18`, i.e. ~18% more real requests
than the raw count. The HTTP request metrics come from `httpRequests1mGroups`, a
non-sampled rollup, and are exact. The `*_result_truncated` gauges count raw rows against
`-query-limit`, unaffected by sampling.

### Partial-permission behaviour

DNS Analytics and Resource Tagging each need their own token permission, and
both are treated as optional so a narrow token still yields useful metrics:

- **DNS Analytics unreadable** → `cloudflare_exporter_api_errors_total{api="dns_analytics"}`
  increments; every other metric is exported normally.
- **Resource Tagging unreadable** → if the job has no `searchTags`, tags are
  only decoration, so `cloudflare_exporter_api_errors_total{api="resource_tagging"}`
  increments and the scrape continues without `cloudflare_zone_tags_info`. If the
  job *does* have `searchTags`, the tags decide which zones to scrape, so the
  job fails loudly instead of silently scraping the wrong set of zones.

### Internal zones (workers.dev)

Cloudflare's zone list excludes `type=internal` zones — `*.workers.dev` among
them — while DNS analytics still reports their query volume. Those rows have no
discovered zone to attach to, so they are skipped and counted in
`cloudflare_exporter_dns_unmatched_groups` rather than dropped silently. A
non-zero value there means DNS traffic exists for zones this exporter does not
scrape.

Overlapping jobs (e.g. an "all zones" job plus a "prod only" job) are safe:
each zone is emitted once per scrape, by the first job that selects it.

### Creating an API Token

The token needs, at minimum:

| Permission | Access |
|------------|--------|
| Zone > Zone | Read |
| Zone > Analytics | Read |
| Account > Account Settings | Read |
| Account Resource Tags (Resource Tagging) | Read |
| Account > DNS Analytics (or similar — see below) | Read |

Exact permission-group labels can shift in the Cloudflare dashboard — verify
against your account's token creation UI. The DNS Analytics permission in
particular isn't clearly named in Cloudflare's docs; a token without it gets
a `"not authorized for that account"` GraphQL error scoped to
`dnsAnalyticsAdaptiveGroups` specifically, while other metrics keep working.

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

Deploy with the Helm chart in [`charts/cloudflare-exporter`](charts/cloudflare-exporter/README.md):

```sh
kubectl create secret generic cloudflare-exporter-token \
  --from-literal=CLOUDFLARE_API_TOKEN=<your-token>

helm install cloudflare-exporter charts/cloudflare-exporter \
  --set apiToken.existingSecret=cloudflare-exporter-token
```

See the chart's README for the full values reference (discovery jobs, `serviceMonitor`,
resources, tolerations, ...).

Build and push the image with the provided `Containerfile`:

```sh
podman build -t <your-registry>/cloudflare-prometheus-exporter-go:latest -f Containerfile .
podman push <your-registry>/cloudflare-prometheus-exporter-go:latest
```

Then point the chart's `image.repository`/`image.tag` values at your registry.

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

## Releasing

Binary releases are cut by GoReleaser when a `v*` tag is pushed. The Helm chart's
`appVersion` must match that tag (it's the default `image.tag`), and the release workflow
fails if it doesn't, so bump the chart first:

```sh
make bump VERSION=0.2.0          # sets version and appVersion in charts/cloudflare-exporter/Chart.yaml
git commit -am "release v0.2.0"
git tag v0.2.0 && git push origin main v0.2.0
```
