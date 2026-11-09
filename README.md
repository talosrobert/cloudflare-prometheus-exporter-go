# cloudflare-prometheus-exporter-go

A Prometheus exporter for Cloudflare zone analytics. It follows the structure of [YACE](https://github.com/nerdswords/yet-another-cloudwatch-exporter). It is a plain Go binary with YAML-configured discovery jobs and tag-based resource filtering. It does not use the Cloudflare Workers runtime. You can deploy it as an ordinary Kubernetes pod.

This project is a rewrite of [cloudflare-prometheus-exporter](https://github.com/talosrobert/cloudflare-prometheus-exporter). It covers a core subset of the metrics of that project. The subset includes zone and account discovery, HTTP request analytics, and Cloudflare Resource Tagging. Resource Tagging filters the zones that are scraped. These metric families are not yet ported: firewall, SSL certs, Magic Transit, Workers, Logpush, Stream, Images, and network analytics.

## Features

The exporter discovers accounts and zones through the Cloudflare REST API. It uses the `cloudflare-go` SDK. It reads HTTP request, DNS query, firewall and WAF event, and error and latency analytics. It uses the Cloudflare GraphQL Analytics API for this. These analytics include per-host, bot-decision, and WAF-attack-score breakdowns.

The exporter filters the scraped zones with the [Resource Tagging API](https://developers.cloudflare.com/resource-tagging/). The `searchTags` job setting uses YACE-style rules. It supports AND logic, key-only matches, key=value matches, and negated matches. The exporter exposes the tags of each matched zone as Prometheus labels. The metric is `cloudflare_zone_tags_info`.

The exporter stores no state. Every scrape queries Cloudflare directly. Replicas need no coordination, so you can run the exporter as a normal Kubernetes Deployment. Run exactly one replica for each configuration, as with any pull-based exporter.

## Configuration

Discovery jobs are defined in a YAML file. The default path is `/etc/cloudflare-exporter/config.yaml`. To use another path, set `-config`. See [`config.example.yaml`](config.example.yaml).

```yaml
discovery:
  jobs:
    - name: production-zones
      accounts:            # optional; omit to scan all accounts that the token can see
        - "0123456789abcdef0123456789abcdef"
      searchTags:          # optional; omit to scrape every zone in scope
        - key: env
          value: production
        - key: archived
          negate: true      # tag=!archived
      metricGroups:        # optional; omit for all four: zone, dns, firewall, errors
        - zone
        - firewall
server:
  listenAddress: ":9199"
  metricsPath: "/metrics"
  scrapeTimeout: 120s        # bounds one scrape across all jobs, accounts, and zones
  accountConcurrency: 4      # accounts in a job that are scraped in parallel and share the budget
  requestTimeout: 30s        # bounds one HTTP, WAF, or Errors analytics API call
  dnsRequestTimeout: 120s    # bounds the DNS analytics call. This call is heavier (see below)
```

A job `metricGroups` setting also skips the related Cloudflare API calls. It does not skip only the metrics.

Zones are deduplicated across jobs. When two jobs select the same zone, the first job in config order claims it. The `metricGroups` of that job apply. The `metricGroups` of a later job are ignored for that zone, even if they differ.

All accounts in all jobs share one `server.scrapeTimeout` budget for each scrape. `server.accountConcurrency` sets how many accounts in one job run in parallel. These accounts use the same budget. They do not run one after another.

Set the external Prometheus scrape timeout to at least `server.scrapeTimeout`. The external timeout is the `prometheus.io/scrape-timeout` annotation, or the `scrapeTimeout` of a ServiceMonitor. If it is shorter, Prometheus closes the connection before the exporter finishes.

Each account makes four analytics calls: HTTP, WAF, Errors, and DNS. Each call has its own timeout. `server.requestTimeout` bounds the HTTP, WAF, and Errors calls. `server.dnsRequestTimeout` bounds the DNS call. The four calls run at the same time, so one slow call does not delay the others.

DNS analytics queries an account-wide dataset, `dnsAnalyticsAdaptiveGroups`. The other three calls query per-zone datasets. The DNS query is therefore heavier. For some accounts, it is much slower. This is why it has its own, longer timeout. Do not set either timeout above `server.scrapeTimeout`. To tune the timeouts, use `cloudflare_exporter_api_call_duration_seconds` (see below). It reports the latency of each call.

The exporter does not read the Cloudflare API token from this file. Set the `CLOUDFLARE_API_TOKEN` environment variable instead. In Kubernetes, take the value from a Secret. See the [Helm chart `apiToken` values](charts/cloudflare-exporter/README.md#values).

Command-line flags. All flags are optional.

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `/etc/cloudflare-exporter/config.yaml` | Path to the YAML configuration file |
| `-analytics-window` | `1m` | Time range that each analytics metric sums over for each scrape. Use whole minutes only. The range ends on a minute boundary |
| `-analytics-lag` | `5m` | The time between now and the end of the window. Use it to allow for Cloudflare ingestion delay. It applies to the adaptive datasets: host, error and latency, bot, WAF, and DNS metrics. This includes `cloudflare_zone_bandwidth_host_bytes` and `cloudflare_zone_bandwidth_host_cached_bytes`. It does not apply to the exact `cloudflare_zone_bandwidth_bytes` |
| `-zone-analytics-lag` | `0` | Lag for the metrics that come from `httpRequests1mGroups`. These include requests, bandwidth, threats, page views, uniques, cache hit ratio, and their breakdowns. Cloudflare finalises this dataset later than the adaptive datasets. Other `cloudflare_zone_*` metrics use `-analytics-lag`. The value `0` means the same as `-analytics-lag` |
| `-analytics-timestamps` | `false` | Stamp each sample with the start of its own dataset's window. Each minute is then stored once, however often it is scraped. A minute is not corrected after it is published. Use a scrape interval of at most `1m`. Instant queries see only series that are younger than the Prometheus 5m lookback. Keep `-analytics-lag` + `-analytics-window` under 5m for the series that you alert on. In one 6-minute local sample, the adaptive datasets were final at about 4m. `httpRequests1mGroups` took longer, for example with lag 4m and zone lag 8m. Cloudflare does not document a guarantee, so measure for your zones |
| `-query-limit` | `10000` | Maximum number of GraphQL result rows requested per zone. For DNS analytics, the limit applies per account |
| `-version` | `false` | Print the exporter version and exit |

### Scrape interval and Cloudflare rate limits

The exporter has no separate fetch or poll interval. It has no background cache. Each scrape of `/metrics` queries Cloudflare directly. The scrape interval therefore sets how often the exporter calls the Cloudflare GraphQL Analytics API. The interval comes from a plain Prometheus `scrape_interval`, a ServiceMonitor `interval`, or a `prometheus.io/interval` annotation. The annotation applies to discovery without the Prometheus Operator.

If Cloudflare rate limits the exporter, widen the scrape interval. Then raise `-analytics-window` to match. Otherwise the exporter under-counts. The `httpRequests1mGroups` dataset returns at most one 1-minute bucket for each query. This dataset backs metrics such as `cloudflare_zone_requests`. If the scrape interval is wider than the window, the traffic between scrapes is silently skipped.

## Metrics

All `cloudflare_zone_*` analytics metrics are gauges. Each gauge holds the sum over the analytics window for that scrape. Cloudflare's GraphQL API returns window aggregates, not running totals. So these metrics cannot be counters. To get a per-second rate, divide by `cloudflare_exporter_analytics_window_seconds`:

```promql
cloudflare_zone_requests / on() group_left cloudflare_exporter_analytics_window_seconds
```

Every zone metric has the labels `zone_id` and `zone`.

| Metric | Extra labels | Description |
|--------|--------------|-------------|
| `cloudflare_zone_info` | `account_id`, `account`, `status` | Discovered zone. The value is always 1. Only zones with the status `active` get analytics metrics, because Cloudflare refuses analytics for other zones |
| `cloudflare_zone_tags_info` | `tag_key`, `tag_value` | One series for each resource tag on the zone. The value is always 1 |
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
| `cloudflare_zone_cache_hit_ratio` | | Cached requests divided by requests |
| `cloudflare_zone_dns_queries` | | DNS queries |
| `cloudflare_zone_dns_queries_type` | `query_type` | DNS queries by query type, for example A, AAAA, and MX |
| `cloudflare_zone_dns_queries_response_code` | `response_code` | DNS queries by response code, for example NOERROR and NXDOMAIN |
| `cloudflare_zone_firewall_events` | | Firewall and WAF events |
| `cloudflare_zone_firewall_events_action` | `action` | Firewall and WAF events by action, for example `block`, `challenge`, `log`, and `skip` |
| `cloudflare_zone_firewall_events_source` | `source` | Firewall and WAF events by triggering product, for example `waf`, `botManagement`, and `rateLimit` |
| `cloudflare_zone_firewall_events_rule` | `rule_id` | Firewall and WAF events by rule ID. **High cardinality**: one series for each rule seen in the window |
| `cloudflare_zone_firewall_events_country` | `country` | Firewall and WAF events by client country |
| `cloudflare_zone_firewall_result_truncated` | (zone labels) | 1 if the WAF event result for the zone reached `-query-limit` in this scrape |
| `cloudflare_zone_requests_customer_error` | `status`, `country`, `host` | Requests with an edge 4xx or 5xx response. **High cardinality**: one series for each (status, country, host) combination seen in the window |
| `cloudflare_zone_requests_host_status` | `host`, `status` | Requests by host and edge status. The rows are the same as in `cloudflare_zone_requests_host`. So the errors of a host always have a matching total. Use this metric for per-host availability SLIs. The metric is sampled. There is one series for each (host, status) combination seen in the window. Rows past `-query-limit` are dropped. `cloudflare_zone_error_result_truncated` flags this |
| `cloudflare_zone_requests_host` | `host` | Requests by host or subdomain. This is a **sampled estimate** from `httpRequestsAdaptiveGroups`. The exact value is `cloudflare_zone_requests`. **High cardinality**: one series for each host seen in the window |
| `cloudflare_zone_bandwidth_host_bytes` | `host` | Bandwidth by host or subdomain. This is a **sampled estimate**. The exact value is `cloudflare_zone_bandwidth_bytes`. **High cardinality** |
| `cloudflare_zone_bandwidth_host_cached_bytes` | `host` | Bandwidth served from cache, with the `hit` and `revalidated` cache statuses, by host or subdomain. This is a **sampled estimate**. The exact value is `cloudflare_zone_bandwidth_cached_bytes`. **High cardinality**. The `cacheStatus` dimension increases the number of query rows. Watch `cloudflare_zone_error_result_truncated` |
| `cloudflare_zone_waf_attack_score_class` | `class` | Requests by WAF attack score class. The classes are `clean`, `likely_clean`, `likely_attack`, and `attack`. This is a sampled estimate |
| `cloudflare_zone_bot_management_decision` | `decision`, `bot_category` | Requests by Bot Management decision. The decisions are `likely_human`, `automated`, `likely_automated`, `verified_bot`, and `other`. The verified bot category is included when known, for example `Search Engine Crawler`. This is a sampled estimate |
| `cloudflare_zone_error_ratio` | `side` | 4xx and 5xx error ratio, by side. `edge` is the edge 4xx and 5xx responses divided by all requests. `origin` is the origin 4xx and 5xx responses divided by the requests that reached the origin. The `origin` side excludes edge cache hits, edge blocks, and all other requests that the origin never receives |
| `cloudflare_zone_origin_response_duration_seconds` | | Average origin response duration. Requests that reached the origin are weighted by request count |
| `cloudflare_zone_error_result_truncated` | (zone labels) | 1 if the error and latency analytics result for the zone reached `-query-limit` in this scrape |
| `cloudflare_zone_low_confidence_rows` | `dataset` | The number of sampled rows in this scrape that have too few records for a steady extrapolation, for each Cloudflare dataset. On the datasets that this exporter scales, these rows are published unscaled. See [Sampling](#sampling) |

Exporter self-metrics:

| Metric | Labels | Description |
|--------|--------|-------------|
| `cloudflare_exporter_analytics_window_seconds` | | Configured `-analytics-window` |
| `cloudflare_exporter_job_success` | `job` | 1 if the last scrape of the job succeeded. Otherwise 0 |
| `cloudflare_exporter_scrape_errors_total` | `job` | Cumulative failed scrapes for each job |
| `cloudflare_exporter_api_errors_total` | `account_id`, `api` | Failed calls to an optional API that needs its own permission (`dns_analytics`, `resource_tagging`). The failure does not stop the scrape |
| `cloudflare_exporter_api_call_duration_seconds` | `account_id`, `api` | Duration of one analytics call for one account. `api` is `http`, `waf`, `errors`, or `dns_analytics`. The duration is recorded for successful calls and for calls that timed out |
| `cloudflare_exporter_dns_result_truncated` | `account_id` | 1 if DNS results reached `-query-limit`. The DNS metrics are then undercounted |
| `cloudflare_exporter_dns_unmatched_groups` | `account_id` | DNS rows skipped because their zone is not in scope. See [Internal zones](#internal-zones-workersdev) |

### WAF/firewall event labels

The `source` label shows which Cloudflare security product made the event. Examples are WAF managed or custom rules, Bot Management, and rate limiting. The label is on `cloudflare_zone_firewall_events_source`. The exporter does not query the `kind` dimension. The exporter does not filter or hardcode the `source` values. During development, the exporter could not sample real events with a non-empty `source`. A guessed filter value could silently return zero rows. Use `source` as a label in PromQL. Do not expect the exporter to filter by product.

### Origin vs. edge errors

The `origin` side of `cloudflare_zone_error_ratio` and `cloudflare_zone_origin_response_duration_seconds` counts only requests that reached the origin. Cloudflare reports origin status `0` for requests that it never forwarded. Examples are edge cache hits, edge or WAF blocks, and redirects. The exporter excludes these rows. If it counted them as origin successes, they would lower both metrics. The `edge` side of `cloudflare_zone_error_ratio` has no exclusion. It shows edge 4xx and 5xx responses divided by all requests. It uses the same data as `cloudflare_zone_requests_status`, so it has no extra API cost.

### Sampling

Cloudflare's `*Adaptive*` datasets are [adaptively sampled](https://developers.cloudflare.com/analytics/sampling/). Under load, a dataset stores only a fraction of its events. Each stored event has a `sampleInterval`. This is the reciprocal of its inclusion probability. Three of these datasets are used here. They do not all need the same treatment.

`firewallEventsAdaptiveGroups` and `dnsAnalyticsAdaptiveGroups` report `count` as the number of sampled records. So this exporter reports `count × avg(sampleInterval)` for each group. This is the same estimate that the Cloudflare dashboard shows. `httpRequestsAdaptiveGroups` supplies the error and latency, host, bot, and WAF-score metrics. It works differently. Cloudflare already extrapolates its `count` and `sum.*` fields. This was verified against `httpRequests1mGroups`. The exporter publishes these rows as returned. Scaling them again would count the traffic twice. The HTTP request metrics come from `httpRequests1mGroups`. This is a non-sampled rollup, so these metrics are exact. The `*_result_truncated` gauges count raw rows against `-query-limit`. Sampling does not affect them.

An extrapolation is only as good as its sample. Suppose a group holds two sampled records and a `sampleInterval` in the millions. The group then stands for millions of events that never happened. This affects one scrape. The next scrape returns to normal.

The exporter scales two datasets itself: `firewallEventsAdaptiveGroups` and `dnsAnalyticsAdaptiveGroups`. On these datasets, a row with fewer than 10 records is not scaled. The exporter publishes its count as the raw sampled sum. This undercounts the row. The error is limited by the sampling rate. The extrapolation that it replaces has no such limit. A row with a `sampleInterval` of 1 is exact, not estimated. The exporter always publishes these rows as they are, however few records they hold.

`cloudflare_zone_low_confidence_rows` counts these thin rows for each zone. The counts are split by `dataset`. On `firewallEventsAdaptiveGroups` and `dnsAnalyticsAdaptiveGroups`, the counted rows are the rows left unscaled. The exporter withholds nothing on `httpRequestsAdaptiveGroups`. For that dataset, the label marks rows whose Cloudflare extrapolation is based on few records. A high value there is a warning about the number. It does not change the number. In all cases, a value that stays high means the window is too short for that zone's sampling rate.

### Partial-permission behaviour

DNS Analytics and Resource Tagging each need their own token permission. Both are optional. A token with narrow permissions still returns useful metrics.

If DNS Analytics is unreadable, `cloudflare_exporter_api_errors_total{api="dns_analytics"}` increases. All other metrics are exported normally.

If Resource Tagging is unreadable and the job has no `searchTags`, the tags only decorate the metrics. Then `cloudflare_exporter_api_errors_total{api="resource_tagging"}` increases. The scrape continues without `cloudflare_zone_tags_info`. If the job has `searchTags`, the tags decide which zones are scraped. In that case the job fails. This prevents a silent scrape of the wrong zones.

### Internal zones (workers.dev)

The Cloudflare zone list excludes zones with `type=internal`. `*.workers.dev` is one example. DNS analytics still reports the query volume of these zones. The exporter has no discovered zone for these rows. It skips them and counts them in `cloudflare_exporter_dns_unmatched_groups`. It does not drop them silently. A value above zero means that DNS traffic exists for zones that this exporter does not scrape.

Overlapping jobs are safe. For example, an "all zones" job and a "prod only" job can overlap. The exporter emits each zone once for each scrape. The first job that selects the zone emits it.

### Creating an API Token

The token needs at least these permissions:

| Permission | Access |
|------------|--------|
| Zone > Zone | Read |
| Zone > Analytics | Read |
| Account > Account Settings | Read |
| Account Resource Tags (Resource Tagging) | Read |
| Account > DNS Analytics (or similar, see below) | Read |

Cloudflare can change the names of permission groups in the dashboard. Check the names in the token creation page of your account. The DNS Analytics permission is not clearly named in the Cloudflare documentation. A token without it returns a `"not authorized for that account"` GraphQL error. The error applies to `dnsAnalyticsAdaptiveGroups` only. The other metrics keep working.

## Endpoints

| Path | Description |
|------|-------------|
| `/metrics` | Prometheus metrics. Set the path with `metricsPath` |
| `/healthz` | Liveness and readiness check |

## Running locally

Set the token, start the exporter, and read the metrics:

```sh
export CLOUDFLARE_API_TOKEN=...
go run ./cmd/exporter -config=config.example.yaml
curl localhost:9199/metrics
```

## Running in Kubernetes

Deploy the exporter with the Helm chart in [`charts/cloudflare-exporter`](charts/cloudflare-exporter/README.md). Create a Secret that holds the token. Then install the chart:

```sh
kubectl create secret generic cloudflare-exporter-token \
  --from-literal=CLOUDFLARE_API_TOKEN=<your-token>

helm install cloudflare-exporter charts/cloudflare-exporter \
  --set apiToken.existingSecret=cloudflare-exporter-token
```

See the chart README for the full list of values. The list includes discovery jobs, `serviceMonitor`, resources, and tolerations.

Each tagged release publishes a pre-built image to `ghcr.io/willhaben/cloudflare-prometheus-exporter-go:vX.Y.Z`. The default `image.repository` of the chart already points to this image.

To build your own image, use the `Containerfile` that the repository provides:

```sh
podman build -t <your-registry>/cloudflare-prometheus-exporter-go:latest -f Containerfile .
podman push <your-registry>/cloudflare-prometheus-exporter-go:latest
```

Then set the chart values `image.repository` and `image.tag` to your registry.

## Prometheus scrape config

If you do not use the Prometheus Operator `ServiceMonitor`, add this `scrape_config`:

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

GoReleaser creates the binary releases when you push a `v*` tag. The Helm chart `appVersion` must match that tag. It is the default `image.tag`. The release workflow fails if they do not match. Bump the chart first:

```sh
make bump VERSION=0.2.0          # sets version and appVersion in charts/cloudflare-exporter/Chart.yaml
git commit -am "release v0.2.0"
git tag v0.2.0 && git push origin main v0.2.0
```
