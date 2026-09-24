# Project conventions

## Metrics
- Prometheus metric names must follow https://prometheus.io/docs/practices/naming/#metric-names (namespace prefix, base units, `_total`/`_info`/`_ratio` suffixes, no label values baked into the name).
- Cloudflare `*Adaptive*` GraphQL datasets are sampled: always request `avg { sampleInterval }` and scale `count` with `estimatedCount` (internal/cloudflareapi/sampling.go). Confirm new dataset/field names by live schema introspection before using them.

## Releasing
Every release is a `v*` git tag; GoReleaser builds the linux/amd64 binary, publishes the GitHub Release, and pushes the container image to `ghcr.io/talosrobert/cloudflare-prometheus-exporter-go` (via the `dockers:` block in `.goreleaser.yaml`) — no extra manual step. The Helm chart's `appVersion` is the default `image.tag`, so it must match the tag — `.github/workflows/release.yml` fails otherwise. Order matters:

1. `make bump VERSION=x.y.z` — sets `version` and `appVersion` in `charts/cloudflare-exporter/Chart.yaml` (no leading `v`).
2. Commit that change together with the release content.
3. `git tag vx.y.z && git push origin main vx.y.z`.

Never tag without step 1; never reuse a chart version. The user commits, tags, and pushes themselves unless they explicitly ask otherwise.
