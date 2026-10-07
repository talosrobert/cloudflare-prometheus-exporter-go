package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	t.Setenv(apiTokenEnvVar, "test-token")

	path := writeConfig(t, `
discovery:
  jobs:
    - name: production
      searchTags:
        - key: env
          value: production
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.APIToken != "test-token" {
		t.Errorf("APIToken = %q, want %q", cfg.APIToken, "test-token")
	}
	if len(cfg.Discovery.Jobs) != 1 {
		t.Fatalf("len(Jobs) = %d, want 1", len(cfg.Discovery.Jobs))
	}
	if got := cfg.Discovery.Jobs[0].SearchTags[0].QueryString(); got != "env=production" {
		t.Errorf("QueryString() = %q, want %q", got, "env=production")
	}
	if cfg.Server.ListenAddress != ":9199" {
		t.Errorf("default ListenAddress = %q, want %q", cfg.Server.ListenAddress, ":9199")
	}
	if cfg.Server.AccountConcurrency != 4 {
		t.Errorf("default AccountConcurrency = %d, want 4", cfg.Server.AccountConcurrency)
	}
	if cfg.Server.RequestTimeout != 30*time.Second {
		t.Errorf("default RequestTimeout = %s, want 30s", cfg.Server.RequestTimeout)
	}
	if cfg.Server.DNSRequestTimeout != 120*time.Second {
		t.Errorf("default DNSRequestTimeout = %s, want 120s", cfg.Server.DNSRequestTimeout)
	}
}

func TestLoad_AccountConcurrencyMustBePositive(t *testing.T) {
	t.Setenv(apiTokenEnvVar, "test-token")
	path := writeConfig(t, `
discovery:
  jobs:
    - name: x
server:
  accountConcurrency: 0
`)

	if _, err := Load(path); err == nil {
		t.Fatal("expected error for non-positive server.accountConcurrency, got nil")
	}
}

func TestLoad_RequestTimeoutMustBePositive(t *testing.T) {
	t.Setenv(apiTokenEnvVar, "test-token")
	path := writeConfig(t, `
discovery:
  jobs:
    - name: x
server:
  requestTimeout: 0s
`)

	if _, err := Load(path); err == nil {
		t.Fatal("expected error for non-positive server.requestTimeout, got nil")
	}
}

func TestLoad_DNSRequestTimeoutMustBePositive(t *testing.T) {
	t.Setenv(apiTokenEnvVar, "test-token")
	path := writeConfig(t, `
discovery:
  jobs:
    - name: x
server:
  dnsRequestTimeout: 0s
`)

	if _, err := Load(path); err == nil {
		t.Fatal("expected error for non-positive server.dnsRequestTimeout, got nil")
	}
}

func TestLoad_RequestTimeoutMustNotExceedScrapeTimeout(t *testing.T) {
	t.Setenv(apiTokenEnvVar, "test-token")
	path := writeConfig(t, `
discovery:
  jobs:
    - name: x
server:
  scrapeTimeout: 10s
  requestTimeout: 20s
`)

	if _, err := Load(path); err == nil {
		t.Fatal("expected error for server.requestTimeout exceeding server.scrapeTimeout, got nil")
	}
}

func TestLoad_DNSRequestTimeoutMustNotExceedScrapeTimeout(t *testing.T) {
	t.Setenv(apiTokenEnvVar, "test-token")
	path := writeConfig(t, `
discovery:
  jobs:
    - name: x
server:
  scrapeTimeout: 10s
  dnsRequestTimeout: 20s
`)

	if _, err := Load(path); err == nil {
		t.Fatal("expected error for server.dnsRequestTimeout exceeding server.scrapeTimeout, got nil")
	}
}

func TestLoad_MissingAPIToken(t *testing.T) {
	t.Setenv(apiTokenEnvVar, "")
	path := writeConfig(t, "discovery:\n  jobs:\n    - name: x\n")

	if _, err := Load(path); err == nil {
		t.Fatal("expected error for missing API token, got nil")
	}
}

func TestLoad_NoJobs(t *testing.T) {
	t.Setenv(apiTokenEnvVar, "test-token")
	path := writeConfig(t, "discovery:\n  jobs: []\n")

	if _, err := Load(path); err == nil {
		t.Fatal("expected error for empty discovery.jobs, got nil")
	}
}

func TestLoad_MetricGroupsDefaultsToAllEnabled(t *testing.T) {
	t.Setenv(apiTokenEnvVar, "test-token")
	path := writeConfig(t, "discovery:\n  jobs:\n    - name: x\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Discovery.Jobs[0].Groups; got != (MetricGroups{}) {
		t.Errorf("Groups = %+v, want zero value (all enabled)", got)
	}
}

func TestLoad_MetricGroupsSubset(t *testing.T) {
	t.Setenv(apiTokenEnvVar, "test-token")
	path := writeConfig(t, `
discovery:
  jobs:
    - name: x
      metricGroups:
        - dns
        - firewall
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := MetricGroups{DisableZone: true, DisableErrors: true}
	if got := cfg.Discovery.Jobs[0].Groups; got != want {
		t.Errorf("Groups = %+v, want %+v", got, want)
	}
}

func TestLoad_MetricGroupsUnknownName(t *testing.T) {
	t.Setenv(apiTokenEnvVar, "test-token")
	path := writeConfig(t, "discovery:\n  jobs:\n    - name: x\n      metricGroups: [bogus]\n")

	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown metric group, got nil")
	}
}

func TestTagFilter_QueryString(t *testing.T) {
	cases := []struct {
		name string
		f    TagFilter
		want string
	}{
		{"key only", TagFilter{Key: "archived"}, "archived"},
		{"key value", TagFilter{Key: "env", Value: "prod"}, "env=prod"},
		{"negate key", TagFilter{Key: "archived", Negate: true}, "!archived"},
		{"negate key value", TagFilter{Key: "region", Value: "us-west-1", Negate: true}, "region!=us-west-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.QueryString(); got != tc.want {
				t.Errorf("QueryString() = %q, want %q", got, tc.want)
			}
		})
	}
}
