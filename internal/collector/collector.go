// Package collector implements a prometheus.Collector that discovers
// Cloudflare accounts/zones (optionally filtered by resource tags) and
// exposes their HTTP analytics as Prometheus metrics.
package collector

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/cloudflareapi"
	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/config"
)

// formatStatus renders an HTTP status code as its metric label value.
func formatStatus(code int) string {
	return strconv.Itoa(code)
}

const namespace = "cloudflare"

// zoneLabels is the base label set every zone-scoped metric carries.
var zoneLabels = []string{"zone_id", "zone"}

func withLabel(base []string, extra ...string) []string {
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	out = append(out, extra...)
	return out
}

func desc(name, help string, labels []string) *prometheus.Desc {
	return prometheus.NewDesc(namespace+"_"+name, help, labels, nil)
}

// cloudflareClient is the subset of *cloudflareapi.Client the collector
// needs, so tests can supply a fake instead of hitting the real API.
type cloudflareClient interface {
	ListAccounts(ctx context.Context) ([]cloudflareapi.Account, error)
	ListZones(ctx context.Context, accountID string) ([]cloudflareapi.Zone, error)
	ZoneTags(ctx context.Context, accountID string, filters []cloudflareapi.TagFilter) (map[string]map[string]string, error)
	FetchHTTPMetrics(ctx context.Context, zoneIDs []string, mintime, maxtime time.Time, limit int) ([]cloudflareapi.ZoneHTTPMetrics, error)
}

// Collector orchestrates discovery jobs and turns their results into
// Prometheus metrics. It queries Cloudflare on every scrape (no background
// cache), matching how a stateless Kubernetes pod behind promhttp is expected
// to behave; ScrapeTimeout in the server config should be set generously
// enough for the account/zone counts involved.
type Collector struct {
	cf         cloudflareClient
	jobs       []config.DiscoveryJob
	queryLimit int
	window     time.Duration
	logger     *slog.Logger

	zoneInfoDesc  *prometheus.Desc
	tagInfoDesc   *prometheus.Desc
	scrapeErrDesc *prometheus.Desc

	requestsTotalDesc        *prometheus.Desc
	requestsCachedDesc       *prometheus.Desc
	requestsSSLDesc          *prometheus.Desc
	requestsContentTypeDesc  *prometheus.Desc
	requestsCountryDesc      *prometheus.Desc
	requestsStatusDesc       *prometheus.Desc
	requestsBrowserDesc      *prometheus.Desc
	requestsIPClassDesc      *prometheus.Desc
	requestsSSLProtocolDesc  *prometheus.Desc
	requestsHTTPVersionDesc  *prometheus.Desc
	bandwidthTotalDesc       *prometheus.Desc
	bandwidthCachedDesc      *prometheus.Desc
	bandwidthSSLDesc         *prometheus.Desc
	bandwidthContentTypeDesc *prometheus.Desc
	bandwidthCountryDesc     *prometheus.Desc
	threatsTotalDesc         *prometheus.Desc
	threatsCountryDesc       *prometheus.Desc
	threatsTypeDesc          *prometheus.Desc
	pageviewsTotalDesc       *prometheus.Desc
	uniquesTotalDesc         *prometheus.Desc
	cacheHitRatioDesc        *prometheus.Desc
}

// New builds a Collector. window is the trailing time range analytics are
// pulled for on each scrape (e.g. one minute matches the underlying
// httpRequests1mGroups granularity); queryLimit caps GraphQL result rows per
// zone, mirroring the original exporter's QUERY_LIMIT.
func New(cf cloudflareClient, jobs []config.DiscoveryJob, window time.Duration, queryLimit int, logger *slog.Logger) *Collector {
	return &Collector{
		cf:         cf,
		jobs:       jobs,
		queryLimit: queryLimit,
		window:     window,
		logger:     logger,

		zoneInfoDesc:  desc("zone_info", "Zone discovery info, value is always 1", withLabel(zoneLabels, "account_id", "account", "status")),
		tagInfoDesc:   desc("zone_tags_info", "Cloudflare resource tags attached to the zone, value is always 1", withLabel(zoneLabels, "tag_key", "tag_value")),
		scrapeErrDesc: desc("exporter_scrape_errors_total", "Errors encountered while scraping the Cloudflare API, by discovery job", []string{"job"}),

		requestsTotalDesc:        desc("zone_requests_total", "Total requests", zoneLabels),
		requestsCachedDesc:       desc("zone_requests_cached", "Cached requests", zoneLabels),
		requestsSSLDesc:          desc("zone_requests_ssl_encrypted_total", "SSL encrypted requests", zoneLabels),
		requestsContentTypeDesc:  desc("zone_requests_content_type_total", "Requests by content type", withLabel(zoneLabels, "content_type")),
		requestsCountryDesc:      desc("zone_requests_country_total", "Requests by country", withLabel(zoneLabels, "country")),
		requestsStatusDesc:       desc("zone_requests_status_total", "Requests by status code", withLabel(zoneLabels, "status")),
		requestsBrowserDesc:      desc("zone_requests_browser_map_page_views_total", "Page views by browser family", withLabel(zoneLabels, "family")),
		requestsIPClassDesc:      desc("zone_requests_ip_class_total", "Requests by IP classification", withLabel(zoneLabels, "ip_type")),
		requestsSSLProtocolDesc:  desc("zone_requests_ssl_protocol_total", "Requests by SSL/TLS protocol version", withLabel(zoneLabels, "ssl_protocol")),
		requestsHTTPVersionDesc:  desc("zone_requests_http_version_total", "Requests by HTTP protocol version", withLabel(zoneLabels, "http_version")),
		bandwidthTotalDesc:       desc("zone_bandwidth_total", "Total bandwidth bytes", zoneLabels),
		bandwidthCachedDesc:      desc("zone_bandwidth_cached_total", "Cached bandwidth bytes", zoneLabels),
		bandwidthSSLDesc:         desc("zone_bandwidth_ssl_encrypted_total", "SSL encrypted bandwidth bytes", zoneLabels),
		bandwidthContentTypeDesc: desc("zone_bandwidth_content_type_total", "Bandwidth by content type", withLabel(zoneLabels, "content_type")),
		bandwidthCountryDesc:     desc("zone_bandwidth_country_total", "Bandwidth by country", withLabel(zoneLabels, "country")),
		threatsTotalDesc:         desc("zone_threats_total", "Total threats", zoneLabels),
		threatsCountryDesc:       desc("zone_threats_country_total", "Threats by country", withLabel(zoneLabels, "country")),
		threatsTypeDesc:          desc("zone_threats_type_total", "Threats by type", withLabel(zoneLabels, "type")),
		pageviewsTotalDesc:       desc("zone_pageviews_total", "Total pageviews", zoneLabels),
		uniquesTotalDesc:         desc("zone_uniques_total", "Unique visitors", zoneLabels),
		cacheHitRatioDesc:        desc("zone_cache_hit_ratio", "Cache hit ratio", zoneLabels),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range c.allDescs() {
		ch <- d
	}
}

func (c *Collector) allDescs() []*prometheus.Desc {
	return []*prometheus.Desc{
		c.zoneInfoDesc, c.tagInfoDesc, c.scrapeErrDesc,
		c.requestsTotalDesc, c.requestsCachedDesc, c.requestsSSLDesc, c.requestsContentTypeDesc,
		c.requestsCountryDesc, c.requestsStatusDesc, c.requestsBrowserDesc, c.requestsIPClassDesc,
		c.requestsSSLProtocolDesc, c.requestsHTTPVersionDesc, c.bandwidthTotalDesc, c.bandwidthCachedDesc,
		c.bandwidthSSLDesc, c.bandwidthContentTypeDesc, c.bandwidthCountryDesc, c.threatsTotalDesc,
		c.threatsCountryDesc, c.threatsTypeDesc, c.pageviewsTotalDesc, c.uniquesTotalDesc, c.cacheHitRatioDesc,
	}
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()
	now := time.Now()

	for _, job := range c.jobs {
		if err := c.collectJob(ctx, ch, job, now); err != nil {
			c.logger.Error("discovery job failed", "job", job.Name, "error", err)
			ch <- prometheus.MustNewConstMetric(c.scrapeErrDesc, prometheus.CounterValue, 1, job.Name)
		}
	}
}

func (c *Collector) collectJob(ctx context.Context, ch chan<- prometheus.Metric, job config.DiscoveryJob, now time.Time) error {
	accountIDs := job.Accounts
	if len(accountIDs) == 0 {
		accounts, err := c.cf.ListAccounts(ctx)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			accountIDs = append(accountIDs, a.ID)
		}
	}

	filters := make([]cloudflareapi.TagFilter, 0, len(job.SearchTags))
	for _, f := range job.SearchTags {
		filters = append(filters, cloudflareapi.TagFilter{Key: f.Key, Value: f.Value, Negate: f.Negate})
	}

	for _, accountID := range accountIDs {
		if err := c.collectAccount(ctx, ch, accountID, filters, now); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) collectAccount(ctx context.Context, ch chan<- prometheus.Metric, accountID string, filters []cloudflareapi.TagFilter, now time.Time) error {
	zones, err := c.cf.ListZones(ctx, accountID)
	if err != nil {
		return err
	}

	tags, err := c.cf.ZoneTags(ctx, accountID, filters)
	if err != nil {
		return err
	}

	// With search-tag filters configured, only zones the tag query matched are
	// in scope; with none configured every zone in the account is in scope
	// (tags map may still be sparse — not every zone need be tagged).
	filtered := zones
	if len(filters) > 0 {
		filtered = filtered[:0]
		for _, z := range zones {
			if _, ok := tags[z.ID]; ok {
				filtered = append(filtered, z)
			}
		}
	}
	if len(filtered) == 0 {
		return nil
	}

	zoneIDs := make([]string, len(filtered))
	zoneByID := make(map[string]cloudflareapi.Zone, len(filtered))
	for i, z := range filtered {
		zoneIDs[i] = z.ID
		zoneByID[z.ID] = z

		ch <- prometheus.MustNewConstMetric(c.zoneInfoDesc, prometheus.GaugeValue, 1, z.ID, z.Name, z.Account.ID, z.Account.Name, z.Status)
		for k, v := range tags[z.ID] {
			ch <- prometheus.MustNewConstMetric(c.tagInfoDesc, prometheus.GaugeValue, 1, z.ID, z.Name, k, v)
		}
	}

	metrics, err := c.cf.FetchHTTPMetrics(ctx, zoneIDs, now.Add(-c.window), now, c.queryLimit)
	if err != nil {
		return err
	}

	for _, m := range metrics {
		if !m.HasData {
			continue
		}
		zone, ok := zoneByID[m.ZoneTag]
		if !ok {
			continue
		}
		c.emitZoneMetrics(ch, zone, m)
	}
	return nil
}

func (c *Collector) emitZoneMetrics(ch chan<- prometheus.Metric, zone cloudflareapi.Zone, m cloudflareapi.ZoneHTTPMetrics) {
	labels := []string{zone.ID, zone.Name}
	sum := m.Group.Sum

	ch <- prometheus.MustNewConstMetric(c.requestsTotalDesc, prometheus.CounterValue, sum.Requests, labels...)
	ch <- prometheus.MustNewConstMetric(c.requestsCachedDesc, prometheus.GaugeValue, sum.CachedRequests, labels...)
	ch <- prometheus.MustNewConstMetric(c.requestsSSLDesc, prometheus.CounterValue, sum.EncryptedRequests, labels...)
	ch <- prometheus.MustNewConstMetric(c.bandwidthTotalDesc, prometheus.CounterValue, sum.Bytes, labels...)
	ch <- prometheus.MustNewConstMetric(c.bandwidthCachedDesc, prometheus.CounterValue, sum.CachedBytes, labels...)
	ch <- prometheus.MustNewConstMetric(c.bandwidthSSLDesc, prometheus.CounterValue, sum.EncryptedBytes, labels...)
	ch <- prometheus.MustNewConstMetric(c.threatsTotalDesc, prometheus.CounterValue, sum.Threats, labels...)
	ch <- prometheus.MustNewConstMetric(c.pageviewsTotalDesc, prometheus.CounterValue, sum.PageViews, labels...)
	ch <- prometheus.MustNewConstMetric(c.uniquesTotalDesc, prometheus.CounterValue, m.Group.Uniq.Uniques, labels...)

	if sum.Requests > 0 {
		ch <- prometheus.MustNewConstMetric(c.cacheHitRatioDesc, prometheus.GaugeValue, sum.CachedRequests/sum.Requests, labels...)
	}

	for _, ct := range sum.ContentTypeMap {
		l := withLabel(labels, ct.EdgeResponseContentTypeName)
		ch <- prometheus.MustNewConstMetric(c.requestsContentTypeDesc, prometheus.CounterValue, ct.Requests, l...)
		ch <- prometheus.MustNewConstMetric(c.bandwidthContentTypeDesc, prometheus.CounterValue, ct.Bytes, l...)
	}

	for _, cn := range sum.CountryMap {
		l := withLabel(labels, cn.ClientCountryName)
		ch <- prometheus.MustNewConstMetric(c.requestsCountryDesc, prometheus.CounterValue, cn.Requests, l...)
		ch <- prometheus.MustNewConstMetric(c.bandwidthCountryDesc, prometheus.CounterValue, cn.Bytes, l...)
		if cn.Threats > 0 {
			ch <- prometheus.MustNewConstMetric(c.threatsCountryDesc, prometheus.CounterValue, cn.Threats, l...)
		}
	}

	statusTotals := make(map[string]float64)
	for _, s := range sum.ResponseStatusMap {
		key := formatStatus(s.EdgeResponseStatus)
		statusTotals[key] += s.Requests
	}
	for status, count := range statusTotals {
		ch <- prometheus.MustNewConstMetric(c.requestsStatusDesc, prometheus.CounterValue, count, withLabel(labels, status)...)
	}

	for _, b := range sum.BrowserMap {
		if b.PageViews > 0 {
			ch <- prometheus.MustNewConstMetric(c.requestsBrowserDesc, prometheus.CounterValue, b.PageViews, withLabel(labels, b.UaBrowserFamily)...)
		}
	}

	for _, t := range sum.ThreatPathingMap {
		if t.Requests > 0 {
			ch <- prometheus.MustNewConstMetric(c.threatsTypeDesc, prometheus.CounterValue, t.Requests, withLabel(labels, t.ThreatPathingName)...)
		}
	}

	for _, ip := range sum.IPClassMap {
		if ip.Requests > 0 {
			ch <- prometheus.MustNewConstMetric(c.requestsIPClassDesc, prometheus.CounterValue, ip.Requests, withLabel(labels, ip.IPType)...)
		}
	}

	for _, ssl := range sum.ClientSSLMap {
		if ssl.Requests > 0 {
			ch <- prometheus.MustNewConstMetric(c.requestsSSLProtocolDesc, prometheus.CounterValue, ssl.Requests, withLabel(labels, ssl.ClientSSLProtocol)...)
		}
	}

	for _, hv := range sum.ClientHTTPVersionMap {
		if hv.Requests > 0 {
			ch <- prometheus.MustNewConstMetric(c.requestsHTTPVersionDesc, prometheus.CounterValue, hv.Requests, withLabel(labels, hv.ClientHTTPProtocol)...)
		}
	}
}
