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

// windowHelp phrases help text for metrics that are sums over the analytics
// window of a single scrape, not cumulative totals.
func windowHelp(what string) string {
	return what + " in the analytics window (see cloudflare_exporter_analytics_window_seconds)"
}

// cloudflareClient is the subset of *cloudflareapi.Client the collector
// needs, so tests can supply a fake instead of hitting the real API.
type cloudflareClient interface {
	ListAccounts(ctx context.Context) ([]cloudflareapi.Account, error)
	ListZones(ctx context.Context, accountID string) ([]cloudflareapi.Zone, error)
	ZoneTags(ctx context.Context, accountID string, filters []cloudflareapi.TagFilter) (map[string]map[string]string, error)
	FetchHTTPMetrics(ctx context.Context, zoneIDs []string, mintime, maxtime time.Time, limit int) ([]cloudflareapi.ZoneHTTPMetrics, error)
}

// Options tune how a Collector queries Cloudflare.
type Options struct {
	// Window is the trailing time range of analytics pulled per scrape.
	Window time.Duration
	// Lag shifts the window into the past to allow for Cloudflare's analytics
	// ingestion delay; querying up to "now" routinely returns empty data.
	Lag time.Duration
	// QueryLimit caps GraphQL result rows requested per zone.
	QueryLimit int
	// ScrapeTimeout bounds one whole Collect call across all jobs.
	ScrapeTimeout time.Duration
}

// Collector orchestrates discovery jobs and turns their results into
// Prometheus metrics. It queries Cloudflare on every scrape (no background
// cache), matching how a stateless Kubernetes pod behind promhttp is expected
// to behave.
//
// Every analytics metric is a gauge holding the sum over Options.Window for
// that scrape: Cloudflare's GraphQL API returns per-window aggregates, not
// running totals, so exposing them as counters would break rate()/increase().
type Collector struct {
	cf     cloudflareClient
	jobs   []config.DiscoveryJob
	opts   Options
	logger *slog.Logger

	scrapeErrors *prometheus.CounterVec
	jobSuccess   *prometheus.GaugeVec

	windowDesc   *prometheus.Desc
	zoneInfoDesc *prometheus.Desc
	tagInfoDesc  *prometheus.Desc

	requestsDesc             *prometheus.Desc
	requestsCachedDesc       *prometheus.Desc
	requestsSSLDesc          *prometheus.Desc
	requestsContentTypeDesc  *prometheus.Desc
	requestsCountryDesc      *prometheus.Desc
	requestsStatusDesc       *prometheus.Desc
	requestsBrowserDesc      *prometheus.Desc
	requestsIPClassDesc      *prometheus.Desc
	requestsSSLProtocolDesc  *prometheus.Desc
	requestsHTTPVersionDesc  *prometheus.Desc
	bandwidthDesc            *prometheus.Desc
	bandwidthCachedDesc      *prometheus.Desc
	bandwidthSSLDesc         *prometheus.Desc
	bandwidthContentTypeDesc *prometheus.Desc
	bandwidthCountryDesc     *prometheus.Desc
	threatsDesc              *prometheus.Desc
	threatsCountryDesc       *prometheus.Desc
	threatsTypeDesc          *prometheus.Desc
	pageviewsDesc            *prometheus.Desc
	uniquesDesc              *prometheus.Desc
	cacheHitRatioDesc        *prometheus.Desc
}

// New builds a Collector for the given discovery jobs.
func New(cf cloudflareClient, jobs []config.DiscoveryJob, opts Options, logger *slog.Logger) *Collector {
	return &Collector{
		cf:     cf,
		jobs:   jobs,
		opts:   opts,
		logger: logger,

		scrapeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "exporter_scrape_errors_total",
			Help:      "Errors encountered while scraping the Cloudflare API, by discovery job",
		}, []string{"job"}),
		jobSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "exporter_job_success",
			Help:      "1 if the discovery job's last scrape completed without error, 0 otherwise",
		}, []string{"job"}),

		windowDesc:   desc("exporter_analytics_window_seconds", "Length of the analytics window each zone metric is summed over", nil),
		zoneInfoDesc: desc("zone_info", "Zone discovery info, value is always 1", withLabel(zoneLabels, "account_id", "account", "status")),
		tagInfoDesc:  desc("zone_tags_info", "Cloudflare resource tags attached to the zone, value is always 1", withLabel(zoneLabels, "tag_key", "tag_value")),

		requestsDesc:             desc("zone_requests", windowHelp("Requests"), zoneLabels),
		requestsCachedDesc:       desc("zone_requests_cached", windowHelp("Cached requests"), zoneLabels),
		requestsSSLDesc:          desc("zone_requests_ssl_encrypted", windowHelp("SSL encrypted requests"), zoneLabels),
		requestsContentTypeDesc:  desc("zone_requests_content_type", windowHelp("Requests by content type"), withLabel(zoneLabels, "content_type")),
		requestsCountryDesc:      desc("zone_requests_country", windowHelp("Requests by country"), withLabel(zoneLabels, "country")),
		requestsStatusDesc:       desc("zone_requests_status", windowHelp("Requests by status code"), withLabel(zoneLabels, "status")),
		requestsBrowserDesc:      desc("zone_requests_browser_map_page_views", windowHelp("Page views by browser family"), withLabel(zoneLabels, "family")),
		requestsIPClassDesc:      desc("zone_requests_ip_class", windowHelp("Requests by IP classification"), withLabel(zoneLabels, "ip_type")),
		requestsSSLProtocolDesc:  desc("zone_requests_ssl_protocol", windowHelp("Requests by SSL/TLS protocol version"), withLabel(zoneLabels, "ssl_protocol")),
		requestsHTTPVersionDesc:  desc("zone_requests_http_version", windowHelp("Requests by HTTP protocol version"), withLabel(zoneLabels, "http_version")),
		bandwidthDesc:            desc("zone_bandwidth_bytes", windowHelp("Bandwidth"), zoneLabels),
		bandwidthCachedDesc:      desc("zone_bandwidth_cached_bytes", windowHelp("Cached bandwidth"), zoneLabels),
		bandwidthSSLDesc:         desc("zone_bandwidth_ssl_encrypted_bytes", windowHelp("SSL encrypted bandwidth"), zoneLabels),
		bandwidthContentTypeDesc: desc("zone_bandwidth_content_type_bytes", windowHelp("Bandwidth by content type"), withLabel(zoneLabels, "content_type")),
		bandwidthCountryDesc:     desc("zone_bandwidth_country_bytes", windowHelp("Bandwidth by country"), withLabel(zoneLabels, "country")),
		threatsDesc:              desc("zone_threats", windowHelp("Threats"), zoneLabels),
		threatsCountryDesc:       desc("zone_threats_country", windowHelp("Threats by country"), withLabel(zoneLabels, "country")),
		threatsTypeDesc:          desc("zone_threats_type", windowHelp("Threats by type"), withLabel(zoneLabels, "type")),
		pageviewsDesc:            desc("zone_pageviews", windowHelp("Page views"), zoneLabels),
		uniquesDesc:              desc("zone_uniques", windowHelp("Unique visitors"), zoneLabels),
		cacheHitRatioDesc:        desc("zone_cache_hit_ratio", windowHelp("Cache hit ratio (cached requests / requests)"), zoneLabels),
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	c.scrapeErrors.Describe(ch)
	c.jobSuccess.Describe(ch)
	for _, d := range c.allDescs() {
		ch <- d
	}
}

func (c *Collector) allDescs() []*prometheus.Desc {
	return []*prometheus.Desc{
		c.windowDesc, c.zoneInfoDesc, c.tagInfoDesc,
		c.requestsDesc, c.requestsCachedDesc, c.requestsSSLDesc, c.requestsContentTypeDesc,
		c.requestsCountryDesc, c.requestsStatusDesc, c.requestsBrowserDesc, c.requestsIPClassDesc,
		c.requestsSSLProtocolDesc, c.requestsHTTPVersionDesc, c.bandwidthDesc, c.bandwidthCachedDesc,
		c.bandwidthSSLDesc, c.bandwidthContentTypeDesc, c.bandwidthCountryDesc, c.threatsDesc,
		c.threatsCountryDesc, c.threatsTypeDesc, c.pageviewsDesc, c.uniquesDesc, c.cacheHitRatioDesc,
	}
}

// scrape holds per-Collect state: the time window, memoised per-account API
// results, and the set of zones already emitted so that overlapping jobs
// (e.g. "all zones" plus "prod only") never produce duplicate series, which
// would make the registry reject the entire scrape.
type scrape struct {
	mintime, maxtime time.Time
	zonesByAccount   map[string][]cloudflareapi.Zone
	emitted          map[string]bool
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()
	if c.opts.ScrapeTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.opts.ScrapeTimeout)
		defer cancel()
	}

	now := time.Now()
	s := &scrape{
		mintime:        now.Add(-c.opts.Lag - c.opts.Window),
		maxtime:        now.Add(-c.opts.Lag),
		zonesByAccount: make(map[string][]cloudflareapi.Zone),
		emitted:        make(map[string]bool),
	}

	ch <- prometheus.MustNewConstMetric(c.windowDesc, prometheus.GaugeValue, c.opts.Window.Seconds())

	for _, job := range c.jobs {
		if err := c.collectJob(ctx, ch, s, job); err != nil {
			c.logger.Error("discovery job failed", "job", job.Name, "error", err)
			c.scrapeErrors.WithLabelValues(job.Name).Inc()
			c.jobSuccess.WithLabelValues(job.Name).Set(0)
			continue
		}
		c.jobSuccess.WithLabelValues(job.Name).Set(1)
	}

	c.scrapeErrors.Collect(ch)
	c.jobSuccess.Collect(ch)
}

func (c *Collector) collectJob(ctx context.Context, ch chan<- prometheus.Metric, s *scrape, job config.DiscoveryJob) error {
	accountIDs := job.Accounts
	if len(accountIDs) == 0 {
		accounts, err := c.cf.ListAccounts(ctx)
		if err != nil {
			return err
		}
		accountIDs = make([]string, 0, len(accounts))
		for _, a := range accounts {
			accountIDs = append(accountIDs, a.ID)
		}
	}

	filters := make([]cloudflareapi.TagFilter, 0, len(job.SearchTags))
	for _, f := range job.SearchTags {
		filters = append(filters, cloudflareapi.TagFilter{Key: f.Key, Value: f.Value, Negate: f.Negate})
	}

	seen := make(map[string]bool, len(accountIDs))
	for _, accountID := range accountIDs {
		if seen[accountID] {
			continue
		}
		seen[accountID] = true
		if err := c.collectAccount(ctx, ch, s, accountID, filters); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) collectAccount(ctx context.Context, ch chan<- prometheus.Metric, s *scrape, accountID string, filters []cloudflareapi.TagFilter) error {
	zones, ok := s.zonesByAccount[accountID]
	if !ok {
		var err error
		zones, err = c.cf.ListZones(ctx, accountID)
		if err != nil {
			return err
		}
		s.zonesByAccount[accountID] = zones
	}

	tags, err := c.cf.ZoneTags(ctx, accountID, filters)
	if err != nil {
		return err
	}

	// With search-tag filters configured, only zones the tag query matched are
	// in scope; with none configured every zone in the account is in scope
	// (tags map may still be sparse — not every zone need be tagged). Zones
	// already emitted by an earlier job in this scrape are skipped.
	var selected []cloudflareapi.Zone
	for _, z := range zones {
		if s.emitted[z.ID] {
			continue
		}
		if _, tagged := tags[z.ID]; len(filters) > 0 && !tagged {
			continue
		}
		selected = append(selected, z)
	}
	if len(selected) == 0 {
		return nil
	}

	zoneIDs := make([]string, len(selected))
	zoneByID := make(map[string]cloudflareapi.Zone, len(selected))
	for i, z := range selected {
		zoneIDs[i] = z.ID
		zoneByID[z.ID] = z
		s.emitted[z.ID] = true

		ch <- prometheus.MustNewConstMetric(c.zoneInfoDesc, prometheus.GaugeValue, 1, z.ID, z.Name, z.Account.ID, z.Account.Name, z.Status)
		for k, v := range tags[z.ID] {
			ch <- prometheus.MustNewConstMetric(c.tagInfoDesc, prometheus.GaugeValue, 1, z.ID, z.Name, k, v)
		}
	}

	metrics, err := c.cf.FetchHTTPMetrics(ctx, zoneIDs, s.mintime, s.maxtime, c.opts.QueryLimit)
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
	gauge := func(d *prometheus.Desc, v float64, extra ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, withLabel(labels, extra...)...)
	}

	gauge(c.requestsDesc, sum.Requests)
	gauge(c.requestsCachedDesc, sum.CachedRequests)
	gauge(c.requestsSSLDesc, sum.EncryptedRequests)
	gauge(c.bandwidthDesc, sum.Bytes)
	gauge(c.bandwidthCachedDesc, sum.CachedBytes)
	gauge(c.bandwidthSSLDesc, sum.EncryptedBytes)
	gauge(c.threatsDesc, sum.Threats)
	gauge(c.pageviewsDesc, sum.PageViews)
	gauge(c.uniquesDesc, m.Group.Uniq.Uniques)

	if sum.Requests > 0 {
		gauge(c.cacheHitRatioDesc, sum.CachedRequests/sum.Requests)
	}

	for _, ct := range sum.ContentTypeMap {
		gauge(c.requestsContentTypeDesc, ct.Requests, ct.EdgeResponseContentTypeName)
		gauge(c.bandwidthContentTypeDesc, ct.Bytes, ct.EdgeResponseContentTypeName)
	}

	for _, cn := range sum.CountryMap {
		gauge(c.requestsCountryDesc, cn.Requests, cn.ClientCountryName)
		gauge(c.bandwidthCountryDesc, cn.Bytes, cn.ClientCountryName)
		if cn.Threats > 0 {
			gauge(c.threatsCountryDesc, cn.Threats, cn.ClientCountryName)
		}
	}

	statusTotals := make(map[string]float64)
	for _, st := range sum.ResponseStatusMap {
		statusTotals[strconv.Itoa(st.EdgeResponseStatus)] += st.Requests
	}
	for status, count := range statusTotals {
		gauge(c.requestsStatusDesc, count, status)
	}

	for _, b := range sum.BrowserMap {
		if b.PageViews > 0 {
			gauge(c.requestsBrowserDesc, b.PageViews, b.UaBrowserFamily)
		}
	}
	for _, t := range sum.ThreatPathingMap {
		if t.Requests > 0 {
			gauge(c.threatsTypeDesc, t.Requests, t.ThreatPathingName)
		}
	}
	for _, ip := range sum.IPClassMap {
		if ip.Requests > 0 {
			gauge(c.requestsIPClassDesc, ip.Requests, ip.IPType)
		}
	}
	for _, ssl := range sum.ClientSSLMap {
		if ssl.Requests > 0 {
			gauge(c.requestsSSLProtocolDesc, ssl.Requests, ssl.ClientSSLProtocol)
		}
	}
	for _, hv := range sum.ClientHTTPVersionMap {
		if hv.Requests > 0 {
			gauge(c.requestsHTTPVersionDesc, hv.Requests, hv.ClientHTTPProtocol)
		}
	}
}
