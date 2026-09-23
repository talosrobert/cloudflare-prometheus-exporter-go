// Command exporter runs the Cloudflare Prometheus exporter as a plain HTTP
// server, suitable for a Kubernetes Deployment — no Cloudflare Workers runtime
// involved.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/cloudflareapi"
	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/collector"
	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/config"
)

func main() {
	configPath := flag.String("config", "/etc/cloudflare-exporter/config.yaml", "path to the exporter config file")
	analyticsWindow := flag.Duration("analytics-window", time.Minute, "trailing time range of HTTP analytics pulled on each scrape")
	queryLimit := flag.Int("query-limit", 10000, "max GraphQL result rows requested per zone")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("loading config", "error", err)
		os.Exit(1)
	}

	cf := cloudflareapi.NewClient(cfg.APIToken)
	coll := collector.New(cf, cfg.Discovery.Jobs, *analyticsWindow, *queryLimit, logger)

	registry := prometheus.NewRegistry()
	registry.MustRegister(coll)

	mux := http.NewServeMux()
	mux.Handle(cfg.Server.MetricsPath, promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorLog: slogErrorLogger{logger}}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:              cfg.Server.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.Server.ScrapeTimeout,
		WriteTimeout:      cfg.Server.ScrapeTimeout,
	}

	logger.Info("starting cloudflare-prometheus-exporter", "address", cfg.Server.ListenAddress, "metrics_path", cfg.Server.MetricsPath, "jobs", len(cfg.Discovery.Jobs))
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}

// slogErrorLogger adapts *slog.Logger to promhttp.HandlerOpts.ErrorLog, which
// expects the stdlib's minimal Logger(Println(...) string) interface.
type slogErrorLogger struct{ logger *slog.Logger }

func (l slogErrorLogger) Println(v ...any) {
	l.logger.Error("promhttp handler error", "detail", v)
}
