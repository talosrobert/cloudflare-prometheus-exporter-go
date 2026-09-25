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
	FetchWAFMetrics(ctx context.Context, zoneIDs []string, mintime, maxtime time.Time, limit int) ([]cloudflareapi.WAFEventGroup, error)
	FetchDNSMetrics(ctx context.Context, accountID string, zoneIDs []string, mintime, maxtime time.Time, limit int) ([]cloudflareapi.DNSQueryGroup, error)
	FetchErrorMetrics(ctx context.Context, zoneIDs []string, mintime, maxtime time.Time, limit int) ([]cloudflareapi.ErrorGroup, error)
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
	apiErrors    *prometheus.CounterVec

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

	dnsQueriesDesc             *prometheus.Desc
	dnsQueriesTypeDesc         *prometheus.Desc
	dnsQueriesResponseCodeDesc *prometheus.Desc
	dnsTruncatedDesc           *prometheus.Desc
	dnsUnmatchedDesc           *prometheus.Desc

	wafEventsDesc        *prometheus.Desc
	wafEventsActionDesc  *prometheus.Desc
	wafEventsSourceDesc  *prometheus.Desc
	wafEventsRuleDesc    *prometheus.Desc
	wafEventsCountryDesc *prometheus.Desc
	wafTruncatedDesc     *prometheus.Desc

	customerErrorDesc          *prometheus.Desc
	errorRatioDesc             *prometheus.Desc
	originResponseDurationDesc *prometheus.Desc
	errorTruncatedDesc         *prometheus.Desc
	requestsHostDesc           *prometheus.Desc
	bandwidthHostDesc          *prometheus.Desc
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
		apiErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "exporter_api_errors_total",
			Help:      "Failed calls to an optional, separately permissioned Cloudflare API, by account; the remaining metrics are still exported",
		}, []string{"account_id", "api"}),

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

		dnsQueriesDesc:             desc("zone_dns_queries", windowHelp("DNS queries"), zoneLabels),
		dnsQueriesTypeDesc:         desc("zone_dns_queries_type", windowHelp("DNS queries by query type"), withLabel(zoneLabels, "query_type")),
		dnsQueriesResponseCodeDesc: desc("zone_dns_queries_response_code", windowHelp("DNS queries by response code"), withLabel(zoneLabels, "response_code")),
		dnsTruncatedDesc:           desc("exporter_dns_result_truncated", "1 if the account's DNS analytics result hit the query limit, meaning DNS metrics are undercounted", []string{"account_id"}),
		dnsUnmatchedDesc:           desc("exporter_dns_unmatched_groups", "DNS analytics rows skipped because their zone is not in scope; Cloudflare's zone list omits type=internal zones such as workers.dev", []string{"account_id"}),

		wafEventsDesc:        desc("zone_firewall_events", windowHelp("Firewall/WAF events"), zoneLabels),
		wafEventsActionDesc:  desc("zone_firewall_events_action", windowHelp("Firewall/WAF events by action"), withLabel(zoneLabels, "action")),
		wafEventsSourceDesc:  desc("zone_firewall_events_source", windowHelp("Firewall/WAF events by triggering product (waf, botManagement, rateLimit, ...)"), withLabel(zoneLabels, "source")),
		wafEventsRuleDesc:    desc("zone_firewall_events_rule", windowHelp("Firewall/WAF events by rule ID — high cardinality, one series per distinct rule seen in the window"), withLabel(zoneLabels, "rule_id")),
		wafEventsCountryDesc: desc("zone_firewall_events_country", windowHelp("Firewall/WAF events by client country"), withLabel(zoneLabels, "country")),
		wafTruncatedDesc:     desc("zone_firewall_result_truncated", "1 if the zone's WAF event result hit the query limit, meaning WAF metrics are undercounted", zoneLabels),

		customerErrorDesc:          desc("zone_requests_customer_error", windowHelp("Requests with an edge 4xx/5xx response, by status, country, and host — high cardinality, one series per distinct (status, country, host) combination seen in the window"), withLabel(zoneLabels, "status", "country", "host")),
		errorRatioDesc:             desc("zone_error_ratio", windowHelp("4xx/5xx error ratio, by side: edge (errors / total requests) or origin (errors / requests that reached the origin, excluding edge cache hits, edge blocks, and other requests never sent to the origin)"), withLabel(zoneLabels, "side")),
		originResponseDurationDesc: desc("zone_origin_response_duration_seconds", windowHelp("Average origin response duration, weighted by request count, across requests that reached the origin"), zoneLabels),
		errorTruncatedDesc:         desc("zone_error_result_truncated", "1 if the zone's error/latency analytics result hit the query limit, meaning error metrics are undercounted", zoneLabels),
		requestsHostDesc:           desc("zone_requests_host", windowHelp("Requests by host/subdomain — sampling-corrected estimate from httpRequestsAdaptiveGroups, unlike the exact zone_requests; high cardinality, one series per distinct host seen in the window"), withLabel(zoneLabels, "host")),
		bandwidthHostDesc:          desc("zone_bandwidth_host_bytes", windowHelp("Bandwidth by host/subdomain — sampling-corrected estimate from httpRequestsAdaptiveGroups, unlike the exact zone_bandwidth_bytes; high cardinality, one series per distinct host seen in the window"), withLabel(zoneLabels, "host")),
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	c.scrapeErrors.Describe(ch)
	c.jobSuccess.Describe(ch)
	c.apiErrors.Describe(ch)
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
		c.dnsQueriesDesc, c.dnsQueriesTypeDesc, c.dnsQueriesResponseCodeDesc, c.dnsTruncatedDesc, c.dnsUnmatchedDesc,
		c.wafEventsDesc, c.wafEventsActionDesc, c.wafEventsSourceDesc, c.wafEventsRuleDesc, c.wafEventsCountryDesc, c.wafTruncatedDesc,
		c.customerErrorDesc, c.errorRatioDesc, c.originResponseDurationDesc, c.errorTruncatedDesc,
		c.requestsHostDesc, c.bandwidthHostDesc,
	}
}

// scrape holds per-Collect state: the time window, memoised per-account API
// results, and the set of zones already emitted so that overlapping jobs
// (e.g. "all zones" plus "prod only") never produce duplicate series, which
// would make the registry reject the entire scrape.
//
// The dedup is per zone, not per (zone, metric group): when two jobs select
// the same zone, the first job in config order claims it, and its
// metricGroups decide what gets collected for it — a later job's differing
// metricGroups are silently ignored for that zone. Give overlapping jobs the
// same metricGroups, or keep their zone sets disjoint.
type scrape struct {
	mintime, maxtime time.Time
	zonesByAccount   map[string][]cloudflareapi.Zone
	emitted          map[string]bool
	// dns accumulates the account-scoped DNS gauges across jobs: two jobs can
	// each select a different subset of the same account's zones, so the
	// series is emitted once per account after every job has contributed.
	dns map[string]*dnsAccountState
}

// dnsAccountState merges the DNS analytics outcome of every job that queried
// one account during a scrape.
type dnsAccountState struct {
	truncated bool
	// unmatched is keyed by row so the same out-of-scope row returned to two
	// jobs' queries is counted once.
	unmatched map[cloudflareapi.DNSQueryGroup]struct{}
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
		dns:            make(map[string]*dnsAccountState),
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

	for accountID, st := range s.dns {
		truncated := 0.0
		if st.truncated {
			truncated = 1
		}
		ch <- prometheus.MustNewConstMetric(c.dnsTruncatedDesc, prometheus.GaugeValue, truncated, accountID)
		ch <- prometheus.MustNewConstMetric(c.dnsUnmatchedDesc, prometheus.GaugeValue, float64(len(st.unmatched)), accountID)
	}

	c.scrapeErrors.Collect(ch)
	c.jobSuccess.Collect(ch)
	c.apiErrors.Collect(ch)
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
		if err := c.collectAccount(ctx, ch, s, accountID, filters, job.Groups); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) collectAccount(ctx context.Context, ch chan<- prometheus.Metric, s *scrape, accountID string, filters []cloudflareapi.TagFilter, groups config.MetricGroups) error {
	zones, ok := s.zonesByAccount[accountID]
	if !ok {
		var err error
		zones, err = c.cf.ListZones(ctx, accountID)
		if err != nil {
			return err
		}
		s.zonesByAccount[accountID] = zones
	}

	// Resource Tagging needs its own token permission. When searchTags are
	// configured the tags decide which zones to scrape, so a failure has to be
	// fatal — guessing would either drop or over-report zones. With no filters
	// the tags are only decoration for zone_tags_info, so keep going without
	// them rather than losing every metric for the account.
	tags, err := c.cf.ZoneTags(ctx, accountID, filters)
	if err != nil {
		if len(filters) > 0 {
			return err
		}
		c.logger.Warn("resource tags unavailable, continuing without tag labels",
			"account_id", accountID, "error", err)
		c.apiErrors.WithLabelValues(accountID, "resource_tagging").Inc()
		tags = nil
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

	if !groups.DisableZone {
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
	}

	if !groups.DisableFirewall {
		// firewallEventsAdaptiveGroups needs no permission beyond what HTTP
		// analytics already requires (verified live), so a failure here is fatal
		// for the account just like FetchHTTPMetrics — unlike DNS/tags below.
		wafGroups, err := c.cf.FetchWAFMetrics(ctx, zoneIDs, s.mintime, s.maxtime, c.opts.QueryLimit)
		if err != nil {
			return err
		}
		c.emitWAFMetrics(ch, zoneByID, wafGroups, c.opts.QueryLimit)
	}

	if !groups.DisableErrors {
		// httpRequestsAdaptiveGroups needs no permission beyond what HTTP analytics
		// already requires (verified live), so a failure here is fatal for the
		// account just like FetchHTTPMetrics/FetchWAFMetrics.
		errorGroups, err := c.cf.FetchErrorMetrics(ctx, zoneIDs, s.mintime, s.maxtime, c.opts.QueryLimit)
		if err != nil {
			return err
		}
		c.emitErrorMetrics(ch, zoneByID, errorGroups, c.opts.QueryLimit)
	}

	if !groups.DisableDNS {
		// DNS analytics needs its own API token permission that Cloudflare does not
		// clearly document, so a token good enough for everything else still gets
		// "not authorized for that account" here. Treat it as non-fatal: record it
		// and keep the metrics that did work.
		dnsGroups, err := c.cf.FetchDNSMetrics(ctx, accountID, zoneIDs, s.mintime, s.maxtime, c.opts.QueryLimit)
		if err != nil {
			c.logger.Warn("dns analytics unavailable", "account_id", accountID, "error", err)
			c.apiErrors.WithLabelValues(accountID, "dns_analytics").Inc()
			return nil
		}
		// Groups are one row per (zone, query type, response code); Cloudflare
		// truncates silently at the limit, which would undercount every DNS metric
		// below, so surface it instead of reporting wrong numbers quietly.
		st := s.dns[accountID]
		if st == nil {
			st = &dnsAccountState{unmatched: make(map[cloudflareapi.DNSQueryGroup]struct{})}
			s.dns[accountID] = st
		}
		if len(dnsGroups) > 0 && len(dnsGroups) >= c.opts.QueryLimit {
			c.logger.Warn("dns analytics result hit query limit, metrics are undercounted",
				"account_id", accountID, "query_limit", c.opts.QueryLimit)
			st.truncated = true
		}
		unmatched := c.emitDNSMetrics(ch, zoneByID, dnsGroups)
		if len(unmatched) > 0 {
			// Cloudflare's zone list omits type=internal zones (workers.dev and
			// friends) while DNS analytics still reports their traffic, so those
			// rows have no zone to attach to. Report it rather than dropping the
			// data silently.
			c.logger.Warn("dns analytics rows skipped, zone not in scope for this scrape",
				"account_id", accountID, "rows", len(unmatched), "example_zone_id", unmatched[0].ZoneTag)
		}
		for _, g := range unmatched {
			st.unmatched[g] = struct{}{}
		}
	}

	return nil
}

// emitWAFMetrics aggregates firewall/WAF event rows by zone and by each
// breakdown dimension. queryLimit is per zone (each zone's
// firewallEventsAdaptiveGroups call inside the shared GraphQL query gets its
// own limit budget), so truncation is detected per zone, not per account.
func (c *Collector) emitWAFMetrics(ch chan<- prometheus.Metric, zoneByID map[string]cloudflareapi.Zone, groups []cloudflareapi.WAFEventGroup, queryLimit int) {
	totals := make(map[string]float64)
	rowCount := make(map[string]int)
	byAction := make(map[dnsZoneKey]float64)
	bySource := make(map[dnsZoneKey]float64)
	byRule := make(map[dnsZoneKey]float64)
	byCountry := make(map[dnsZoneKey]float64)

	for _, g := range groups {
		if _, inScope := zoneByID[g.ZoneTag]; !inScope {
			continue
		}
		totals[g.ZoneTag] += g.Count
		rowCount[g.ZoneTag]++
		byAction[dnsZoneKey{g.ZoneTag, g.Action}] += g.Count
		bySource[dnsZoneKey{g.ZoneTag, g.Source}] += g.Count
		byRule[dnsZoneKey{g.ZoneTag, g.RuleID}] += g.Count
		byCountry[dnsZoneKey{g.ZoneTag, g.Country}] += g.Count
	}

	for zoneID, zone := range zoneByID {
		truncated := 0.0
		if rowCount[zoneID] > 0 && rowCount[zoneID] >= queryLimit {
			c.logger.Warn("waf analytics result hit query limit, metrics are undercounted",
				"zone_id", zoneID, "query_limit", queryLimit)
			truncated = 1
		}
		ch <- prometheus.MustNewConstMetric(c.wafTruncatedDesc, prometheus.GaugeValue, truncated, zone.ID, zone.Name)
	}
	for zoneID, total := range totals {
		zone := zoneByID[zoneID]
		ch <- prometheus.MustNewConstMetric(c.wafEventsDesc, prometheus.GaugeValue, total, zone.ID, zone.Name)
	}
	for key, count := range byAction {
		zone := zoneByID[key.zoneID]
		ch <- prometheus.MustNewConstMetric(c.wafEventsActionDesc, prometheus.GaugeValue, count, zone.ID, zone.Name, key.label)
	}
	for key, count := range bySource {
		zone := zoneByID[key.zoneID]
		ch <- prometheus.MustNewConstMetric(c.wafEventsSourceDesc, prometheus.GaugeValue, count, zone.ID, zone.Name, key.label)
	}
	for key, count := range byRule {
		zone := zoneByID[key.zoneID]
		ch <- prometheus.MustNewConstMetric(c.wafEventsRuleDesc, prometheus.GaugeValue, count, zone.ID, zone.Name, key.label)
	}
	for key, count := range byCountry {
		zone := zoneByID[key.zoneID]
		ch <- prometheus.MustNewConstMetric(c.wafEventsCountryDesc, prometheus.GaugeValue, count, zone.ID, zone.Name, key.label)
	}
}

// customerErrorKey groups a customer-facing (edge) error count by zone,
// status, country, and host.
type customerErrorKey struct{ zoneID, status, country, host string }

// hostKey groups a per-host aggregate by zone and host.
type hostKey struct{ zoneID, host string }

// emitErrorMetrics aggregates httpRequestsAdaptiveGroups rows by zone.
// originResponseStatus is 0 for rows the origin was never contacted for, so
// those rows are excluded from every origin-side aggregate (error ratio,
// duration) rather than diluting them with non-origin traffic; the edge-side
// error ratio is computed separately in emitZoneMetrics from data already
// fetched via httpRequests1mGroups, at no extra API cost.
func (c *Collector) emitErrorMetrics(ch chan<- prometheus.Metric, zoneByID map[string]cloudflareapi.Zone, groups []cloudflareapi.ErrorGroup, queryLimit int) {
	rowCount := make(map[string]int)
	customerErrors := make(map[customerErrorKey]float64)
	originTotal := make(map[string]float64)
	originErrors := make(map[string]float64)
	durationWeightedSum := make(map[string]float64)
	durationWeightedCount := make(map[string]float64)
	hostRequests := make(map[hostKey]float64)
	hostBytes := make(map[hostKey]float64)

	for _, g := range groups {
		zoneID := g.ZoneTag
		if _, inScope := zoneByID[zoneID]; !inScope {
			continue
		}
		rowCount[zoneID]++

		if g.EdgeStatus >= 400 {
			customerErrors[customerErrorKey{zoneID, strconv.Itoa(g.EdgeStatus), g.Country, g.Host}] += g.Count
		}

		hk := hostKey{zoneID, g.Host}
		hostRequests[hk] += g.Count
		hostBytes[hk] += g.EdgeResponseBytes

		if g.OriginStatus > 0 {
			originTotal[zoneID] += g.Count
			if g.OriginStatus >= 400 {
				originErrors[zoneID] += g.Count
			}
			durationWeightedSum[zoneID] += g.AvgOriginDurationMs * g.Count
			durationWeightedCount[zoneID] += g.Count
		}
	}

	for zoneID, zone := range zoneByID {
		truncated := 0.0
		if rowCount[zoneID] > 0 && rowCount[zoneID] >= queryLimit {
			c.logger.Warn("error/latency analytics result hit query limit, metrics are undercounted",
				"zone_id", zoneID, "query_limit", queryLimit)
			truncated = 1
		}
		ch <- prometheus.MustNewConstMetric(c.errorTruncatedDesc, prometheus.GaugeValue, truncated, zone.ID, zone.Name)

		if total := originTotal[zoneID]; total > 0 {
			ch <- prometheus.MustNewConstMetric(c.errorRatioDesc, prometheus.GaugeValue, originErrors[zoneID]/total, zone.ID, zone.Name, "origin")
		}
		if weight := durationWeightedCount[zoneID]; weight > 0 {
			avgSeconds := durationWeightedSum[zoneID] / weight / 1000
			ch <- prometheus.MustNewConstMetric(c.originResponseDurationDesc, prometheus.GaugeValue, avgSeconds, zone.ID, zone.Name)
		}
	}

	for key, count := range customerErrors {
		zone := zoneByID[key.zoneID]
		ch <- prometheus.MustNewConstMetric(c.customerErrorDesc, prometheus.GaugeValue, count, zone.ID, zone.Name, key.status, key.country, key.host)
	}

	for key, count := range hostRequests {
		zone := zoneByID[key.zoneID]
		ch <- prometheus.MustNewConstMetric(c.requestsHostDesc, prometheus.GaugeValue, count, zone.ID, zone.Name, key.host)
	}
	for key, bytes := range hostBytes {
		zone := zoneByID[key.zoneID]
		ch <- prometheus.MustNewConstMetric(c.bandwidthHostDesc, prometheus.GaugeValue, bytes, zone.ID, zone.Name, key.host)
	}
}

type dnsZoneKey struct{ zoneID, label string }

// emitDNSMetrics aggregates DNS rows per in-scope zone and returns the rows
// whose zone is not in zoneByID so the caller can account for them.
func (c *Collector) emitDNSMetrics(ch chan<- prometheus.Metric, zoneByID map[string]cloudflareapi.Zone, groups []cloudflareapi.DNSQueryGroup) []cloudflareapi.DNSQueryGroup {
	totals := make(map[string]float64)
	byType := make(map[dnsZoneKey]float64)
	byResponseCode := make(map[dnsZoneKey]float64)

	var unmatched []cloudflareapi.DNSQueryGroup
	for _, g := range groups {
		if _, inScope := zoneByID[g.ZoneTag]; !inScope {
			unmatched = append(unmatched, g)
			continue
		}
		totals[g.ZoneTag] += g.Count
		byType[dnsZoneKey{g.ZoneTag, g.QueryType}] += g.Count
		byResponseCode[dnsZoneKey{g.ZoneTag, g.ResponseCode}] += g.Count
	}

	for zoneID, total := range totals {
		zone := zoneByID[zoneID]
		ch <- prometheus.MustNewConstMetric(c.dnsQueriesDesc, prometheus.GaugeValue, total, zone.ID, zone.Name)
	}
	for key, count := range byType {
		zone := zoneByID[key.zoneID]
		ch <- prometheus.MustNewConstMetric(c.dnsQueriesTypeDesc, prometheus.GaugeValue, count, zone.ID, zone.Name, key.label)
	}
	for key, count := range byResponseCode {
		zone := zoneByID[key.zoneID]
		ch <- prometheus.MustNewConstMetric(c.dnsQueriesResponseCodeDesc, prometheus.GaugeValue, count, zone.ID, zone.Name, key.label)
	}
	return unmatched
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
	edgeErrors := 0.0
	for _, st := range sum.ResponseStatusMap {
		statusTotals[strconv.Itoa(st.EdgeResponseStatus)] += st.Requests
		if st.EdgeResponseStatus >= 400 {
			edgeErrors += st.Requests
		}
	}
	for status, count := range statusTotals {
		gauge(c.requestsStatusDesc, count, status)
	}
	if sum.Requests > 0 {
		gauge(c.errorRatioDesc, edgeErrors/sum.Requests, "edge")
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
