# cloudflare-exporter

Helm chart for [cloudflare-prometheus-exporter-go](https://github.com/talosrobert/cloudflare-prometheus-exporter-go).

## Install

```sh
kubectl create secret generic cloudflare-exporter-token \
  --from-literal=CLOUDFLARE_API_TOKEN=<your-token>

helm install cloudflare-exporter charts/cloudflare-exporter \
  --set apiToken.existingSecret=cloudflare-exporter-token
```

Or point `image.repository`/`image.tag` at a registry you pushed the `Containerfile` image to
first — the chart does not build or push images itself.

## Values

| Key | Default | Description |
|-----|---------|--------------|
| `replicaCount` | `1` | Stateless exporter; run exactly one replica per config. |
| `image.repository` | `cloudflare-prometheus-exporter-go` | Image repository, without tag. |
| `image.tag` | `""` | Defaults to `Chart.AppVersion`. |
| `image.pullPolicy` | `IfNotPresent` | |
| `apiToken.existingSecret` | `""` | Name of a Secret you created yourself holding the token. Recommended. |
| `apiToken.existingSecretKey` | `CLOUDFLARE_API_TOKEN` | Key inside that Secret. |
| `apiToken.value` | `""` | Plaintext token — the chart creates the Secret for you. Only for quick local testing; never commit a values file with this set. Ignored when `apiToken.existingSecret` is set. |
| `exporter.configPath` | `/etc/cloudflare-exporter/config.yaml` | Passed as `-config`; also drives the ConfigMap mount path and key. |
| `exporter.analyticsWindow` | `1m` | `-analytics-window` |
| `exporter.analyticsLag` | `5m` | `-analytics-lag` |
| `exporter.queryLimit` | `10000` | `-query-limit` |
| `exporter.metricsPath` | `/metrics` | Written into `config.yaml`'s `server.metricsPath`. |
| `exporter.scrapeTimeout` | `30s` | Written into `config.yaml`'s `server.scrapeTimeout`. |
| `exporter.discovery.jobs` | one `production-zones` job, `env=production` | Rendered verbatim into `config.yaml`'s `discovery.jobs` — see the main [README](../../README.md#configuration) for the `searchTags`/`accounts` shape. |
| `service.type` | `ClusterIP` | |
| `service.port` | `9199` | Also the container port and `config.yaml`'s `listenAddress`. |
| `service.annotations` | `prometheus.io/scrape`, `/port`, `/path`, `/interval` (`60s`), `/scrape-timeout` (`30s`) | Set for non-Operator Prometheus discovery. `interval`/`scrape-timeout` are honored only if your scrape config maps them to `__scrape_interval__`/`__scrape_timeout__` — a common but not universal convention. |
| `serviceAccount.create` | `true` | Create a dedicated ServiceAccount for the Deployment. |
| `serviceAccount.annotations` | `{}` | e.g. for IRSA/Workload Identity, if ever needed. |
| `serviceAccount.name` | `""` | Defaults to the chart's fullname when empty; ignored (uses `default`) if `create` is `false`. |
| `serviceAccount.automountServiceAccountToken` | `false` | The exporter only calls the Cloudflare API, never the Kubernetes API. |
| `serviceMonitor.enabled` | `false` | Requires the Prometheus Operator CRDs. |
| `serviceMonitor.interval` / `.scrapeTimeout` | `60s` / `30s` | |
| `resources` | `50m`/`64Mi` requests, `256Mi` limit | |
| `podSecurityContext`, `securityContext` | non-root, read-only rootfs, all capabilities dropped | |
| `nodeSelector`, `tolerations`, `affinity` | `{}` / `[]` / `{}` | |
| `podAnnotations`, `podLabels` | `{}` | |

## Notes

- This exporter has no independent poll loop: every scrape queries Cloudflare
  directly, so the scrape interval **is** the Cloudflare API call rate.
  Raise `service.annotations`' `prometheus.io/interval` (annotation-based
  discovery) or `serviceMonitor.interval` (Operator discovery) — whichever
  your Prometheus uses — to reduce Cloudflare API pressure or avoid
  rate-limiting. Keep `exporter.analyticsWindow` matched to it, or you'll
  reopen the under-counting gap described in the main README.
- The Deployment runs under its own ServiceAccount (`serviceAccount.create: true`), with
  its token not automounted since the exporter never talks to the Kubernetes API. Set
  `serviceAccount.create: false` to run under an existing/default ServiceAccount instead.
- The Pod won't scrape successfully without a token: set `apiToken.existingSecret` (or
  `apiToken.value` for a quick try) before installing, or see the post-install NOTES output.
- Changing `exporter.configPath` moves both the ConfigMap key and the volume mount path with
  it, but keep its directory writable-free (mounted read-only) and its filename `.yaml`.
