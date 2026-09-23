package collector

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/cloudflareapi"
	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/config"
)

type fakeClient struct {
	accounts []cloudflareapi.Account
	zones    map[string][]cloudflareapi.Zone
	tags     map[string]map[string]map[string]string
	metrics  map[string][]cloudflareapi.ZoneHTTPMetrics
}

func (f *fakeClient) ListAccounts(ctx context.Context) ([]cloudflareapi.Account, error) {
	return f.accounts, nil
}

func (f *fakeClient) ListZones(ctx context.Context, accountID string) ([]cloudflareapi.Zone, error) {
	return f.zones[accountID], nil
}

func (f *fakeClient) ZoneTags(ctx context.Context, accountID string, filters []cloudflareapi.TagFilter) (map[string]map[string]string, error) {
	return f.tags[accountID], nil
}

func (f *fakeClient) FetchHTTPMetrics(ctx context.Context, zoneIDs []string, mintime, maxtime time.Time, limit int) ([]cloudflareapi.ZoneHTTPMetrics, error) {
	if len(zoneIDs) == 0 {
		return nil, nil
	}
	return f.metrics[zoneIDs[0]], nil
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCollector_TagFiltering(t *testing.T) {
	fc := &fakeClient{
		accounts: []cloudflareapi.Account{{ID: "acct1", Name: "Account One"}},
		zones: map[string][]cloudflareapi.Zone{
			"acct1": {
				{ID: "zone-prod", Name: "prod.example.com", Status: "active", Account: cloudflareapi.Account{ID: "acct1", Name: "Account One"}},
				{ID: "zone-dev", Name: "dev.example.com", Status: "active", Account: cloudflareapi.Account{ID: "acct1", Name: "Account One"}},
			},
		},
		tags: map[string]map[string]map[string]string{
			"acct1": {"zone-prod": {"env": "production"}},
		},
		metrics: map[string][]cloudflareapi.ZoneHTTPMetrics{},
	}

	job := config.DiscoveryJob{
		Name:       "prod-only",
		SearchTags: []config.TagFilter{{Key: "env", Value: "production"}},
	}

	c := New(fc, []config.DiscoveryJob{job}, time.Minute, 1000, newTestLogger())

	metricCh := make(chan prometheus.Metric, 100)
	c.Collect(metricCh)
	close(metricCh)

	var sawProd, sawDev bool
	for m := range metricCh {
		var d dto.Metric
		if err := m.Write(&d); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if strings.Contains(m.Desc().String(), "zone_info") {
			for _, l := range d.GetLabel() {
				if l.GetName() == "zone" && l.GetValue() == "prod.example.com" {
					sawProd = true
				}
				if l.GetName() == "zone" && l.GetValue() == "dev.example.com" {
					sawDev = true
				}
			}
		}
	}

	if !sawProd {
		t.Error("expected zone-prod (matches searchTags) to be included")
	}
	if sawDev {
		t.Error("expected zone-dev (does not match searchTags) to be excluded")
	}
}
