package collector

import (
	"context"
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
	tags    map[string]map[string]map[string]string
	metrics map[string]cloudflareapi.ZoneHTTPMetrics

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

type failingZones struct{ *fakeClient }

func (f *failingZones) ListZones(_ context.Context, _ string) ([]cloudflareapi.Zone, error) {
	return nil, context.DeadlineExceeded
}
