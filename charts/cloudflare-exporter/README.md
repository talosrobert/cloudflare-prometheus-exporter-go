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
| `service.annotations` | `prometheus.io/scrape` etc. | Set for non-Operator Prometheus discovery. |
| `serviceMonitor.enabled` | `false` | Requires the Prometheus Operator CRDs. |
| `serviceMonitor.interval` / `.scrapeTimeout` | `60s` / `30s` | |
| `resources` | `50m`/`64Mi` requests, `256Mi` limit | |
| `podSecurityContext`, `securityContext` | non-root, read-only rootfs, all capabilities dropped | |
| `nodeSelector`, `tolerations`, `affinity` | `{}` / `[]` / `{}` | |
| `podAnnotations`, `podLabels` | `{}` | |

## Notes

- No `serviceAccount` is created — the Deployment runs under the namespace's default one.
- The Pod won't scrape successfully without a token: set `apiToken.existingSecret` (or
  `apiToken.value` for a quick try) before installing, or see the post-install NOTES output.
- Changing `exporter.configPath` moves both the ConfigMap key and the volume mount path with
  it, but keep its directory writable-free (mounted read-only) and its filename `.yaml`.
