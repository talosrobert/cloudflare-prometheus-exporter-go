package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/cloudflareapi"
	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/config"
)

type fakeClient struct {
	accounts []cloudflareapi.Account
	zones    map[string][]cloudflareapi.Zone
	// tags is keyed by account ID; the fake applies filters by requiring
	// every filter key to be present with the given value.
	tags      map[string]map[string]map[string]string
	metrics   map[string]cloudflareapi.ZoneHTTPMetrics
	dnsGroups []cloudflareapi.DNSQueryGroup
	wafGroups []cloudflareapi.WAFEventGroup

	listZonesCalls int
}

func (f *fakeClient) ListAccounts(_ context.Context) ([]cloudflareapi.Account, error) {
	return f.accounts, nil
}

func (f *fakeClient) ListZones(_ context.Context, accountID string) ([]cloudflareapi.Zone, error) {
	f.listZonesCalls++
	return f.zones[accountID], nil
}

func (f *fakeClient) ZoneTags(_ context.Context, accountID string, filters []cloudflareapi.TagFilter) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string)
	for zoneID, tags := range f.tags[accountID] {
		if matches(tags, filters) {
			out[zoneID] = tags
		}
	}
	return out, nil
}

func matches(tags map[string]string, filters []cloudflareapi.TagFilter) bool {
	for _, f := range filters {
		v, ok := tags[f.Key]
		if !ok || (f.Value != "" && v != f.Value) {
			return false
		}
	}
	return true
}

func (f *fakeClient) FetchHTTPMetrics(_ context.Context, zoneIDs []string, _, _ time.Time, _ int) ([]cloudflareapi.ZoneHTTPMetrics, error) {
	var out []cloudflareapi.ZoneHTTPMetrics
	for _, id := range zoneIDs {
		if m, ok := f.metrics[id]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeClient) FetchWAFMetrics(_ context.Context, zoneIDs []string, _, _ time.Time, _ int) ([]cloudflareapi.WAFEventGroup, error) {
	wanted := make(map[string]bool, len(zoneIDs))
	for _, id := range zoneIDs {
		wanted[id] = true
	}
	var out []cloudflareapi.WAFEventGroup
	for _, g := range f.wafGroups {
		if wanted[g.ZoneTag] {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeClient) FetchDNSMetrics(_ context.Context, _ string, zoneIDs []string, _, _ time.Time, _ int) ([]cloudflareapi.DNSQueryGroup, error) {
	wanted := make(map[string]bool, len(zoneIDs))
	for _, id := range zoneIDs {
		wanted[id] = true
	}
	var out []cloudflareapi.DNSQueryGroup
	for _, g := range f.dnsGroups {
		if wanted[g.ZoneTag] {
			out = append(out, g)
		}
	}
	return out, nil
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testOptions() Options {
	return Options{Window: time.Minute, Lag: 5 * time.Minute, QueryLimit: 1000, ScrapeTimeout: 10 * time.Second}
}

func newFake() *fakeClient {
	acct := cloudflareapi.Account{ID: "acct1", Name: "Account One"}
	prod := cloudflareapi.ZoneHTTPMetrics{ZoneTag: "zone-prod", HasData: true}
	prod.Group.Sum.Requests = 100
	prod.Group.Sum.CachedRequests = 25
	return &fakeClient{
		accounts: []cloudflareapi.Account{acct},
		zones: map[string][]cloudflareapi.Zone{
			"acct1": {
				{ID: "zone-prod", Name: "prod.example.com", Status: "active", Account: acct},
				{ID: "zone-dev", Name: "dev.example.com", Status: "active", Account: acct},
			},
		},
		tags: map[string]map[string]map[string]string{
			"acct1": {"zone-prod": {"env": "production"}},
		},
		metrics: map[string]cloudflareapi.ZoneHTTPMetrics{"zone-prod": prod},
		dnsGroups: []cloudflareapi.DNSQueryGroup{
			{ZoneTag: "zone-prod", QueryType: "A", ResponseCode: "NOERROR", Count: 10},
			{ZoneTag: "zone-prod", QueryType: "AAAA", ResponseCode: "NOERROR", Count: 4},
			{ZoneTag: "zone-prod", QueryType: "A", ResponseCode: "NXDOMAIN", Count: 1},
		},
		wafGroups: []cloudflareapi.WAFEventGroup{
			{ZoneTag: "zone-prod", Action: "block", Source: "waf", RuleID: "rule-1", Country: "US", Count: 5},
			{ZoneTag: "zone-prod", Action: "challenge", Source: "botManagement", RuleID: "", Country: "DE", Count: 2},
		},
	}
}

// gather registers c in a pedantic registry so duplicate or inconsistent
// series fail the test the same way they would fail a real scrape.
func gather(t *testing.T, c *Collector) []*dto.MetricFamily {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	if err := reg.Register(c); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	return families
}

// hasSeries reports whether metric family name has a series carrying every
// label in want.
func hasSeries(families []*dto.MetricFamily, name string, want map[string]string) bool {
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			for k, v := range want {
				if !hasLabel(m, k, v) {
					continue next
				}
			}
			return true
		}
	}
	return false
}

func hasLabel(m *dto.Metric, name, value string) bool {
	for _, l := range m.GetLabel() {
		if l.GetName() == name && l.GetValue() == value {
			return true
		}
	}
	return false
}

func TestCollector_TagFiltering(t *testing.T) {
	job := config.DiscoveryJob{
		Name:       "prod-only",
		SearchTags: []config.TagFilter{{Key: "env", Value: "production"}},
	}
	c := New(newFake(), []config.DiscoveryJob{job}, testOptions(), newTestLogger())

	out := gather(t, c)
	if !hasSeries(out, "cloudflare_zone_info", map[string]string{"zone": "prod.example.com"}) {
		t.Error("expected zone-prod (matches searchTags) to be included")
	}
	if hasSeries(out, "cloudflare_zone_info", map[string]string{"zone": "dev.example.com"}) {
		t.Error("expected zone-dev (does not match searchTags) to be excluded")
	}
	if !hasSeries(out, "cloudflare_zone_tags_info", map[string]string{"zone": "prod.example.com", "tag_key": "env", "tag_value": "production"}) {
		t.Error("expected zone_tags_info series carrying env=production")
	}
}

func TestCollector_NoFiltersScrapesAllZones(t *testing.T) {
	job := config.DiscoveryJob{Name: "all"}
	c := New(newFake(), []config.DiscoveryJob{job}, testOptions(), newTestLogger())

	out := gather(t, c)
	for _, want := range []string{"prod.example.com", "dev.example.com"} {
		if !hasSeries(out, "cloudflare_zone_info", map[string]string{"zone": want}) {
			t.Errorf("expected %s to be included with no searchTags", want)
		}
	}
}

func TestCollector_OverlappingJobsNoDuplicateSeries(t *testing.T) {
	fc := newFake()
	jobs := []config.DiscoveryJob{
		{Name: "all"},
		{Name: "prod-only", SearchTags: []config.TagFilter{{Key: "env", Value: "production"}}},
	}
	c := New(fc, jobs, testOptions(), newTestLogger())

	// A pedantic registry's Gather() fails on duplicate series, so reaching
	// here with both jobs succeeding is the assertion.
	out := gather(t, c)
	if !hasSeries(out, "cloudflare_zone_info", map[string]string{"zone": "prod.example.com"}) {
		t.Fatal("expected zone-prod series to be present")
	}
	for _, job := range []string{"all", "prod-only"} {
		if !hasSeries(out, "cloudflare_exporter_job_success", map[string]string{"job": job}) {
			t.Errorf("expected cloudflare_exporter_job_success for job %q", job)
		}
	}
	if fc.listZonesCalls != 1 {
		t.Errorf("ListZones called %d times, want 1 (memoised across jobs)", fc.listZonesCalls)
	}
}

func TestCollector_AnalyticsAreGauges(t *testing.T) {
	job := config.DiscoveryJob{Name: "all"}
	c := New(newFake(), []config.DiscoveryJob{job}, testOptions(), newTestLogger())

	if err := testutil.CollectAndCompare(c, strings.NewReader(`
# HELP cloudflare_zone_requests Requests in the analytics window (see cloudflare_exporter_analytics_window_seconds)
# TYPE cloudflare_zone_requests gauge
cloudflare_zone_requests{zone="prod.example.com",zone_id="zone-prod"} 100
# HELP cloudflare_zone_cache_hit_ratio Cache hit ratio (cached requests / requests) in the analytics window (see cloudflare_exporter_analytics_window_seconds)
# TYPE cloudflare_zone_cache_hit_ratio gauge
cloudflare_zone_cache_hit_ratio{zone="prod.example.com",zone_id="zone-prod"} 0.25
`), "cloudflare_zone_requests", "cloudflare_zone_cache_hit_ratio"); err != nil {
		t.Fatal(err)
	}
}

func TestCollector_DNSMetrics(t *testing.T) {
	job := config.DiscoveryJob{Name: "all"}
	c := New(newFake(), []config.DiscoveryJob{job}, testOptions(), newTestLogger())

	if err := testutil.CollectAndCompare(c, strings.NewReader(`
# HELP cloudflare_zone_dns_queries DNS queries in the analytics window (see cloudflare_exporter_analytics_window_seconds)
# TYPE cloudflare_zone_dns_queries gauge
cloudflare_zone_dns_queries{zone="prod.example.com",zone_id="zone-prod"} 15
# HELP cloudflare_zone_dns_queries_type DNS queries by query type in the analytics window (see cloudflare_exporter_analytics_window_seconds)
# TYPE cloudflare_zone_dns_queries_type gauge
cloudflare_zone_dns_queries_type{query_type="A",zone="prod.example.com",zone_id="zone-prod"} 11
cloudflare_zone_dns_queries_type{query_type="AAAA",zone="prod.example.com",zone_id="zone-prod"} 4
# HELP cloudflare_zone_dns_queries_response_code DNS queries by response code in the analytics window (see cloudflare_exporter_analytics_window_seconds)
# TYPE cloudflare_zone_dns_queries_response_code gauge
cloudflare_zone_dns_queries_response_code{response_code="NOERROR",zone="prod.example.com",zone_id="zone-prod"} 14
cloudflare_zone_dns_queries_response_code{response_code="NXDOMAIN",zone="prod.example.com",zone_id="zone-prod"} 1
`), "cloudflare_zone_dns_queries", "cloudflare_zone_dns_queries_type", "cloudflare_zone_dns_queries_response_code"); err != nil {
		t.Fatal(err)
	}
}

// A token lacking Cloudflare's (undocumented) DNS Analytics permission fails
// only this dataset; every other metric must survive and the job must not be
// marked failed.
func TestCollector_DNSErrorIsNonFatal(t *testing.T) {
	fc := &failingDNS{fakeClient: newFake()}
	c := New(fc, []config.DiscoveryJob{{Name: "all"}}, testOptions(), newTestLogger())

	out := gather(t, c)
	if !hasSeries(out, "cloudflare_zone_requests", map[string]string{"zone": "prod.example.com"}) {
		t.Error("HTTP metrics must survive a DNS failure")
	}
	if hasSeries(out, "cloudflare_zone_dns_queries", map[string]string{"zone": "prod.example.com"}) {
		t.Error("no DNS metrics expected when the DNS query failed")
	}
	if got := testutil.ToFloat64(c.jobSuccess.WithLabelValues("all")); got != 1 {
		t.Errorf("job_success = %v, want 1 (DNS failure is non-fatal)", got)
	}
	if got := testutil.ToFloat64(c.apiErrors.WithLabelValues("acct1", "dns_analytics")); got != 1 {
		t.Errorf("api_errors_total{api=dns_analytics} = %v, want 1", got)
	}
}

// Cloudflare truncates group results at the limit with no indication, which
// would undercount DNS metrics, so the exporter must flag it.
func TestCollector_DNSTruncationFlagged(t *testing.T) {
	fc := newFake()
	opts := testOptions()
	opts.QueryLimit = len(fc.dnsGroups)
	c := New(fc, []config.DiscoveryJob{{Name: "all"}}, opts, newTestLogger())

	if err := testutil.CollectAndCompare(c, strings.NewReader(`
# HELP cloudflare_exporter_dns_result_truncated 1 if the account's DNS analytics result hit the query limit, meaning DNS metrics are undercounted
# TYPE cloudflare_exporter_dns_result_truncated gauge
cloudflare_exporter_dns_result_truncated{account_id="acct1"} 1
`), "cloudflare_exporter_dns_result_truncated"); err != nil {
		t.Fatal(err)
	}

	opts.QueryLimit = len(fc.dnsGroups) + 1
	c = New(newFake(), []config.DiscoveryJob{{Name: "all"}}, opts, newTestLogger())
	if err := testutil.CollectAndCompare(c, strings.NewReader(`
# HELP cloudflare_exporter_dns_result_truncated 1 if the account's DNS analytics result hit the query limit, meaning DNS metrics are undercounted
# TYPE cloudflare_exporter_dns_result_truncated gauge
cloudflare_exporter_dns_result_truncated{account_id="acct1"} 0
`), "cloudflare_exporter_dns_result_truncated"); err != nil {
		t.Fatal(err)
	}
}

// Two jobs can each select a different subset of the same account's zones, so
// every account-scoped series must still be emitted exactly once per scrape.
func TestCollector_DisjointJobsSameAccountNoDuplicateSeries(t *testing.T) {
	fc := newFake()
	fc.tags["acct1"]["zone-dev"] = map[string]string{"env": "dev"}
	jobs := []config.DiscoveryJob{
		{Name: "prod", SearchTags: []config.TagFilter{{Key: "env", Value: "production"}}},
		{Name: "dev", SearchTags: []config.TagFilter{{Key: "env", Value: "dev"}}},
	}
	c := New(fc, jobs, testOptions(), newTestLogger())

	// Pedantic Gather() rejects duplicate series, so a clean gather is the assertion.
	out := gather(t, c)
	for _, zone := range []string{"prod.example.com", "dev.example.com"} {
		if !hasSeries(out, "cloudflare_zone_info", map[string]string{"zone": zone}) {
			t.Errorf("expected %s to be scraped by its job", zone)
		}
	}
	var truncatedSeries int
	for _, mf := range out {
		if mf.GetName() == "cloudflare_exporter_dns_result_truncated" {
			truncatedSeries = len(mf.GetMetric())
		}
	}
	if truncatedSeries != 1 {
		t.Errorf("dns_result_truncated series = %d, want 1 per account", truncatedSeries)
	}
}

// Resource Tagging is separately permissioned. Without searchTags the tags are
// only decoration, so a failure must not cost the account every other metric;
// with searchTags they select the zones, so it must fail loudly instead.
func TestCollector_TagErrorFatalOnlyWhenFiltering(t *testing.T) {
	t.Run("no filters: non-fatal", func(t *testing.T) {
		c := New(&failingTags{fakeClient: newFake()}, []config.DiscoveryJob{{Name: "all"}}, testOptions(), newTestLogger())

		out := gather(t, c)
		if !hasSeries(out, "cloudflare_zone_info", map[string]string{"zone": "prod.example.com"}) {
			t.Error("zone discovery must survive a resource-tagging failure")
		}
		if !hasSeries(out, "cloudflare_zone_requests", map[string]string{"zone": "prod.example.com"}) {
			t.Error("HTTP metrics must survive a resource-tagging failure")
		}
		if !hasSeries(out, "cloudflare_zone_dns_queries", map[string]string{"zone": "prod.example.com"}) {
			t.Error("DNS metrics must survive a resource-tagging failure")
		}
		if hasSeries(out, "cloudflare_zone_tags_info", map[string]string{"zone": "prod.example.com"}) {
			t.Error("no tag labels expected when the tag query failed")
		}
		if got := testutil.ToFloat64(c.jobSuccess.WithLabelValues("all")); got != 1 {
			t.Errorf("job_success = %v, want 1", got)
		}
		if got := testutil.ToFloat64(c.apiErrors.WithLabelValues("acct1", "resource_tagging")); got != 1 {
			t.Errorf("api_errors_total{api=resource_tagging} = %v, want 1", got)
		}
	})

	t.Run("with filters: fatal", func(t *testing.T) {
		job := config.DiscoveryJob{Name: "prod", SearchTags: []config.TagFilter{{Key: "env", Value: "production"}}}
		c := New(&failingTags{fakeClient: newFake()}, []config.DiscoveryJob{job}, testOptions(), newTestLogger())

		out := gather(t, c)
		if hasSeries(out, "cloudflare_zone_info", map[string]string{"zone": "prod.example.com"}) {
			t.Error("must not scrape zones when the tag filter could not be evaluated")
		}
		if got := testutil.ToFloat64(c.jobSuccess.WithLabelValues("prod")); got != 0 {
			t.Errorf("job_success = %v, want 0", got)
		}
	})
}

// An uninitialised Desc field panics prometheus.Registry on the first scrape,
// so catch a forgotten initialiser here instead of in production.
func TestCollector_AllDescsInitialised(t *testing.T) {
	c := New(newFake(), []config.DiscoveryJob{{Name: "all"}}, testOptions(), newTestLogger())
	for i, d := range c.allDescs() {
		if d == nil {
			t.Fatalf("allDescs()[%d] is nil — a Desc field is declared but never initialised in New", i)
		}
	}
}

// Cloudflare's zone list omits type=internal zones (workers.dev), yet DNS
// analytics still reports their traffic, so those rows cannot be attributed to
// a discovered zone. They must be counted, not dropped in silence.
func TestCollector_DNSUnmatchedZonesCounted(t *testing.T) {
	fc := newFake()
	fc.dnsGroups = append(fc.dnsGroups, cloudflareapi.DNSQueryGroup{
		ZoneTag: "zone-workers-dev", QueryType: "A", ResponseCode: "NOERROR", Count: 99,
	})
	// The fake filters by requested zone IDs, so return the stray row regardless.
	c := New(&unscopedDNS{fakeClient: fc}, []config.DiscoveryJob{{Name: "all"}}, testOptions(), newTestLogger())

	if err := testutil.CollectAndCompare(c, strings.NewReader(`
# HELP cloudflare_exporter_dns_unmatched_groups DNS analytics rows skipped because their zone is not in scope; Cloudflare's zone list omits type=internal zones such as workers.dev
# TYPE cloudflare_exporter_dns_unmatched_groups gauge
cloudflare_exporter_dns_unmatched_groups{account_id="acct1"} 1
`), "cloudflare_exporter_dns_unmatched_groups"); err != nil {
		t.Fatal(err)
	}

	// The in-scope zone's totals must exclude the unmatched row's 99 queries.
	c = New(&unscopedDNS{fakeClient: newFakeWithStray()}, []config.DiscoveryJob{{Name: "all"}}, testOptions(), newTestLogger())
	if err := testutil.CollectAndCompare(c, strings.NewReader(`
# HELP cloudflare_zone_dns_queries DNS queries in the analytics window (see cloudflare_exporter_analytics_window_seconds)
# TYPE cloudflare_zone_dns_queries gauge
cloudflare_zone_dns_queries{zone="prod.example.com",zone_id="zone-prod"} 15
`), "cloudflare_zone_dns_queries"); err != nil {
		t.Fatal(err)
	}
}

func newFakeWithStray() *fakeClient {
	fc := newFake()
	fc.dnsGroups = append(fc.dnsGroups, cloudflareapi.DNSQueryGroup{
		ZoneTag: "zone-workers-dev", QueryType: "A", ResponseCode: "NOERROR", Count: 99,
	})
	return fc
}

// unscopedDNS mimics Cloudflare returning rows for zones that were not asked
// for (and that zone discovery never surfaced).
type unscopedDNS struct{ *fakeClient }

func (f *unscopedDNS) FetchDNSMetrics(_ context.Context, _ string, _ []string, _, _ time.Time, _ int) ([]cloudflareapi.DNSQueryGroup, error) {
	return f.dnsGroups, nil
}

func TestCollector_WAFMetrics(t *testing.T) {
	job := config.DiscoveryJob{Name: "all"}
	c := New(newFake(), []config.DiscoveryJob{job}, testOptions(), newTestLogger())

	if err := testutil.CollectAndCompare(c, strings.NewReader(`
# HELP cloudflare_zone_firewall_events Firewall/WAF events in the analytics window (see cloudflare_exporter_analytics_window_seconds)
# TYPE cloudflare_zone_firewall_events gauge
cloudflare_zone_firewall_events{zone="prod.example.com",zone_id="zone-prod"} 7
# HELP cloudflare_zone_firewall_events_action Firewall/WAF events by action in the analytics window (see cloudflare_exporter_analytics_window_seconds)
# TYPE cloudflare_zone_firewall_events_action gauge
cloudflare_zone_firewall_events_action{action="block",zone="prod.example.com",zone_id="zone-prod"} 5
cloudflare_zone_firewall_events_action{action="challenge",zone="prod.example.com",zone_id="zone-prod"} 2
# HELP cloudflare_zone_firewall_events_source Firewall/WAF events by triggering product (waf, botManagement, rateLimit, ...) in the analytics window (see cloudflare_exporter_analytics_window_seconds)
# TYPE cloudflare_zone_firewall_events_source gauge
cloudflare_zone_firewall_events_source{source="botManagement",zone="prod.example.com",zone_id="zone-prod"} 2
cloudflare_zone_firewall_events_source{source="waf",zone="prod.example.com",zone_id="zone-prod"} 5
`), "cloudflare_zone_firewall_events", "cloudflare_zone_firewall_events_action", "cloudflare_zone_firewall_events_source"); err != nil {
		t.Fatal(err)
	}
}

// queryLimit applies per zone (each zone's firewallEventsAdaptiveGroups call
// gets its own limit budget inside the shared GraphQL query), so truncation
// must be detected and reported per zone.
func TestCollector_WAFTruncationIsPerZone(t *testing.T) {
	fc := newFake()
	opts := testOptions()
	opts.QueryLimit = 2 // exactly the number of WAF rows for zone-prod in newFake()
	c := New(fc, []config.DiscoveryJob{{Name: "all"}}, opts, newTestLogger())

	out := gather(t, c)
	if !hasSeries(out, "cloudflare_zone_firewall_result_truncated", map[string]string{"zone": "prod.example.com"}) {
		t.Fatal("expected a truncation series for zone-prod")
	}
	for _, mf := range out {
		if mf.GetName() != "cloudflare_zone_firewall_result_truncated" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if hasLabel(m, "zone", "prod.example.com") && m.GetGauge().GetValue() != 1 {
				t.Errorf("zone-prod truncated = %v, want 1 (row count %d >= limit %d)", m.GetGauge().GetValue(), 2, opts.QueryLimit)
			}
			if hasLabel(m, "zone", "dev.example.com") && m.GetGauge().GetValue() != 0 {
				t.Errorf("zone-dev truncated = %v, want 0 (no WAF rows at all)", m.GetGauge().GetValue())
			}
		}
	}
}

func TestCollector_ScrapeErrorsAccumulate(t *testing.T) {
	fc := newFake()
	job := config.DiscoveryJob{Name: "broken", Accounts: []string{"missing"}}
	c := New(&failingZones{fakeClient: fc}, []config.DiscoveryJob{job}, testOptions(), newTestLogger())

	for range 3 {
		ch := make(chan prometheus.Metric, 64)
		c.Collect(ch)
		close(ch)
	}
	if got := testutil.ToFloat64(c.scrapeErrors.WithLabelValues("broken")); got != 3 {
		t.Errorf("scrape_errors_total = %v, want 3 after three failing scrapes", got)
	}
	if got := testutil.ToFloat64(c.jobSuccess.WithLabelValues("broken")); got != 0 {
		t.Errorf("job_success = %v, want 0", got)
	}
}

// Unlike DNS/tags, WAF analytics needs no extra permission, so a failure here
// must behave like an HTTP-metrics failure: fatal for the account.
type failingWAF struct{ *fakeClient }

func (f *failingWAF) FetchWAFMetrics(_ context.Context, _ []string, _, _ time.Time, _ int) ([]cloudflareapi.WAFEventGroup, error) {
	return nil, errors.New("graphql error")
}

// A WAF fetch failure must behave like an HTTP-metrics failure (fatal for the
// job), not like DNS/tags (fatal only for the current account, job keeps
// going). Two accounts in one job make the difference observable: account
// metrics already queued on the channel before the failure cannot be
// retracted, but a fatal error must stop the job before it ever reaches the
// second account.
func TestCollector_WAFErrorIsFatal(t *testing.T) {
	acct2 := cloudflareapi.Account{ID: "acct2", Name: "Account Two"}
	fc := newFake()
	fc.accounts = append(fc.accounts, acct2)
	fc.zones["acct2"] = []cloudflareapi.Zone{
		{ID: "zone-other", Name: "other.example.com", Status: "active", Account: acct2},
	}
	c := New(&failingWAF{fakeClient: fc}, []config.DiscoveryJob{{Name: "all"}}, testOptions(), newTestLogger())

	out := gather(t, c)
	if hasSeries(out, "cloudflare_zone_info", map[string]string{"zone": "other.example.com"}) {
		t.Error("acct2 must never be reached once acct1's WAF fetch fails fatally")
	}
	if got := testutil.ToFloat64(c.jobSuccess.WithLabelValues("all")); got != 0 {
		t.Errorf("job_success = %v, want 0", got)
	}
	if got := testutil.ToFloat64(c.scrapeErrors.WithLabelValues("all")); got != 1 {
		t.Errorf("scrape_errors_total = %v, want 1", got)
	}
}

type failingDNS struct{ *fakeClient }

func (f *failingDNS) FetchDNSMetrics(_ context.Context, _ string, _ []string, _, _ time.Time, _ int) ([]cloudflareapi.DNSQueryGroup, error) {
	return nil, errors.New("not authorized for that account")
}

type failingTags struct{ *fakeClient }

func (f *failingTags) ZoneTags(_ context.Context, _ string, _ []cloudflareapi.TagFilter) (map[string]map[string]string, error) {
	return nil, errors.New("403 Forbidden: Authentication error")
}

type failingZones struct{ *fakeClient }

func (f *failingZones) ListZones(_ context.Context, _ string) ([]cloudflareapi.Zone, error) {
	return nil, context.DeadlineExceeded
}
