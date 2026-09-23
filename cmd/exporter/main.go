// Command exporter runs the Cloudflare Prometheus exporter as a plain HTTP
// server, suitable for a Kubernetes Deployment — no Cloudflare Workers runtime
// involved.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/cloudflareapi"
	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/collector"
	"github.com/talosrobert/cloudflare-prometheus-exporter-go/internal/config"
)

const shutdownGracePeriod = 15 * time.Second

// version is overwritten at build time via -ldflags "-X main.version=...";
// GoReleaser sets it to the release tag.
var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("exporter exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configPath := flag.String("config", "/etc/cloudflare-exporter/config.yaml", "path to the exporter config file")
	analyticsWindow := flag.Duration("analytics-window", time.Minute, "trailing time range of HTTP analytics summed per scrape")
	analyticsLag := flag.Duration("analytics-lag", 5*time.Minute, "how far behind now the analytics window ends, to allow for Cloudflare ingestion delay")
	queryLimit := flag.Int("query-limit", 10000, "max GraphQL result rows requested per zone")
	excludeHost := flag.Bool("exclude-host", false, "drop the host label from cloudflare_zone_requests_customer_error, trading detail for lower cardinality")
	showVersion := flag.Bool("version", false, "print the exporter version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}

	if *analyticsWindow <= 0 {
		return errors.New("-analytics-window must be positive")
	}
	if *analyticsLag < 0 {
		return errors.New("-analytics-lag must not be negative")
	}
	if *queryLimit <= 0 {
		return errors.New("-query-limit must be positive")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	cf := cloudflareapi.NewClient(cfg.APIToken)
	coll := collector.New(cf, cfg.Discovery.Jobs, collector.Options{
		Window:        *analyticsWindow,
		Lag:           *analyticsLag,
		QueryLimit:    *queryLimit,
		ScrapeTimeout: cfg.Server.ScrapeTimeout,
		ExcludeHost:   *excludeHost,
	}, logger)

	registry := prometheus.NewRegistry()
	registry.MustRegister(coll)

	mux := http.NewServeMux()
	mux.Handle("GET "+cfg.Server.MetricsPath, promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorLog: slogErrorLogger{logger}}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:              cfg.Server.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.Server.ScrapeTimeout,
		// Collect() is bounded by ScrapeTimeout itself; the write deadline
		// only needs headroom to flush the response afterwards.
		WriteTimeout: cfg.Server.ScrapeTimeout + 10*time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting cloudflare-prometheus-exporter", "address", cfg.Server.ListenAddress, "metrics_path", cfg.Server.MetricsPath, "jobs", len(cfg.Discovery.Jobs))
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connections")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http server shutdown: %w", err)
		}
		return nil
	}
}

// slogErrorLogger adapts *slog.Logger to promhttp.HandlerOpts.ErrorLog, which
// expects the stdlib's minimal Logger(Println(...) string) interface.
type slogErrorLogger struct{ logger *slog.Logger }

func (l slogErrorLogger) Println(v ...any) {
	l.logger.Error("promhttp handler error", "detail", v)
}
