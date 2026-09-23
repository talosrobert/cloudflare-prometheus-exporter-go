// Package config loads the exporter's YAML configuration file.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// TagFilter matches Cloudflare's resource-tagging query syntax: Value empty means
// a key-only match ("tag=<key>"); Negate flips it to a negative match ("tag=!<key>"
// or "tag=<key>!=<value>").
type TagFilter struct {
	Key    string `yaml:"key"`
	Value  string `yaml:"value,omitempty"`
	Negate bool   `yaml:"negate,omitempty"`
}

// QueryString renders the filter in Cloudflare's `tag` query-parameter syntax.
func (f TagFilter) QueryString() string {
	switch {
	case f.Value == "" && f.Negate:
		return "!" + f.Key
	case f.Value == "":
		return f.Key
	case f.Negate:
		return f.Key + "!=" + f.Value
	default:
		return f.Key + "=" + f.Value
	}
}

// DiscoveryJob selects which accounts to scan and which zones within them to
// scrape, mirroring YACE's discovery-job model but filtered by Cloudflare
// resource tags instead of AWS tags.
type DiscoveryJob struct {
	Name string `yaml:"name"`
	// Accounts to scan. Empty means every account the API token can see.
	Accounts []string `yaml:"accounts,omitempty"`
	// SearchTags restrict scraped zones to those matching ALL filters (AND logic).
	// Empty means every zone in the selected accounts is scraped.
	SearchTags []TagFilter `yaml:"searchTags,omitempty"`
}

// ServerConfig controls the exporter's own HTTP listener.
type ServerConfig struct {
	ListenAddress string        `yaml:"listenAddress"`
	MetricsPath   string        `yaml:"metricsPath"`
	ScrapeTimeout time.Duration `yaml:"scrapeTimeout"`
}

// Config is the fully loaded exporter configuration.
type Config struct {
	Discovery struct {
		Jobs []DiscoveryJob `yaml:"jobs"`
	} `yaml:"discovery"`
	Server ServerConfig `yaml:"server"`

	// APIToken is never read from the YAML file — it comes from the
	// CLOUDFLARE_API_TOKEN environment variable so it can be injected via a
	// Kubernetes Secret without landing in a ConfigMap.
	APIToken string `yaml:"-"`
}

const apiTokenEnvVar = "CLOUDFLARE_API_TOKEN" //nolint:gosec // variable name, not a credential

func defaults() Config {
	var c Config
	c.Server.ListenAddress = ":9199"
	c.Server.MetricsPath = "/metrics"
	c.Server.ScrapeTimeout = 30 * time.Second
	return c
}

// Load reads and validates the exporter configuration from path.
func Load(path string) (*Config, error) {
	cfg := defaults()

	data, err := os.ReadFile(path) //nolint:gosec // path is the operator-supplied -config flag
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	cfg.APIToken = os.Getenv(apiTokenEnvVar)
	if cfg.APIToken == "" {
		return nil, fmt.Errorf("%s environment variable is required", apiTokenEnvVar)
	}
	if cfg.Server.ScrapeTimeout <= 0 {
		return nil, fmt.Errorf("server.scrapeTimeout must be positive, got %s", cfg.Server.ScrapeTimeout)
	}
	if len(cfg.Discovery.Jobs) == 0 {
		return nil, fmt.Errorf("config must define at least one discovery.jobs entry")
	}
	for i, job := range cfg.Discovery.Jobs {
		if job.Name == "" {
			return nil, fmt.Errorf("discovery.jobs[%d]: name is required", i)
		}
		for j, tf := range job.SearchTags {
			if tf.Key == "" {
				return nil, fmt.Errorf("discovery.jobs[%d].searchTags[%d]: key is required", i, j)
			}
		}
	}

	return &cfg, nil
}
