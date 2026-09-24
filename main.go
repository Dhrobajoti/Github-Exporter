package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/robfig/cron/v3"
	log "github.com/sirupsen/logrus"

	"github-exporter/internal/config"
	"github-exporter/internal/exporter"
	"github-exporter/internal/ghclient"
	"github-exporter/internal/scanners"
	"github-exporter/internal/server"
)

const (
	initialRefreshRetryDelay = time.Minute
	shutdownTimeout          = 10 * time.Second
	healthcheckTimeout       = 3 * time.Second
)

func main() {
	if len(os.Args) > 1 {
		os.Exit(runCommand(os.Args[1:]))
	}
	run()
}

// runCommand handles the helper sub-commands and returns the exit code.
func runCommand(args []string) int {
	switch args[0] {
	case "hash-password":
		if err := hashPassword(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		return 0
	case "healthcheck":
		port := os.Getenv("LISTEN_PORT")
		if port == "" {
			port = config.DefaultListenPort
		}
		if err := healthcheck(port, healthcheckTimeout); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\nUsage:\n  github-exporter                run the exporter (configured through environment variables)\n  github-exporter hash-password  read a password from stdin and print its bcrypt hash\n  github-exporter healthcheck    exit 0 if the exporter accepts connections on LISTEN_PORT\n", args[0])
		return 2
	}
}

func run() {
	log.SetFormatter(&log.JSONFormatter{})

	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}
	level, err := log.ParseLevel(cfg.LogLevel)
	if err != nil {
		log.Warnf("Invalid LOG_LEVEL %q, defaulting to info", cfg.LogLevel)
		level = log.InfoLevel
	}
	log.SetLevel(level)

	if err := web.Validate(cfg.WebConfigFile); err != nil {
		log.Fatalf("Invalid WEB_CONFIG_FILE %q: %v", cfg.WebConfigFile, err)
	}

	client, err := ghclient.New(cfg)
	if err != nil {
		log.Fatalf("GitHub client setup failed: %v", err)
	}
	scanList, err := scanners.New(cfg.Scanners, client, scanners.Options{IncludeClosed: cfg.IncludeClosed})
	if err != nil {
		log.Fatalf("Scanner setup failed: %v", err)
	}

	filter := ghclient.RepoFilter{
		IncludeArchived: cfg.IncludeArchived,
		Include:         cfg.RepoInclude,
		Exclude:         cfg.RepoExclude,
	}
	exp := exporter.New(cfg.Org, func(ctx context.Context) ([]string, error) {
		return ghclient.ListRepos(ctx, client, cfg.Org, filter)
	}, scanList, cfg.Concurrency)
	prometheus.MustRegister(exp)

	handler, cache := server.New(promhttp.Handler(), exp.Ready, cfg.MetricsCacheTTL)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// New data must not wait for the cache window to pass.
	refresh := func() error {
		err := exp.Refresh(ctx)
		if err == nil {
			cache.Invalidate()
		}
		return err
	}

	// Fetch immediately, and keep trying until it works: with a daily schedule
	// a single failed start-up fetch would otherwise leave the exporter empty
	// for a day.
	go refreshUntilSuccess(ctx, refresh)

	c := cron.New()
	if _, err := c.AddFunc(cfg.Schedule, func() {
		log.Info("Refreshing metrics")
		if err := refresh(); err != nil && !errors.Is(err, exporter.ErrRefreshInProgress) {
			log.Errorf("Refresh failed: %v", err)
		}
	}); err != nil {
		log.Fatalf("Invalid CRON_SCHEDULE %q: %v", cfg.Schedule, err)
	}
	c.Start()
	log.Infof("Refreshing %v for org %s on schedule %q", cfg.Scanners, cfg.Org, cfg.Schedule)

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          server.NewErrorLog(func(message string) { log.Warn(message) }),
	}
	go func() {
		<-ctx.Done()
		log.Info("Shutting down")
		<-c.Stop().Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	listenAddresses := []string{":" + cfg.ListenPort}
	systemdSocket := false
	flags := &web.FlagConfig{
		WebListenAddresses: &listenAddresses,
		WebSystemdSocket:   &systemdSocket,
		WebConfigFile:      &cfg.WebConfigFile,
	}

	if cfg.WebConfigFile == "" {
		log.Infof("Exporter running on %s/metrics (plain HTTP, no authentication)", listenAddresses[0])
	} else {
		log.Infof("Exporter running on %s/metrics (web config %s)", listenAddresses[0], cfg.WebConfigFile)
	}
	if err := web.ListenAndServe(srv, flags, slogger(level)); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP server failed: %v", err)
	}
}

// slogger adapts the log level for the web package, which logs through slog.
func slogger(level log.Level) *slog.Logger {
	slogLevel := slog.LevelInfo
	switch {
	case level >= log.DebugLevel:
		slogLevel = slog.LevelDebug
	case level == log.ErrorLevel || level == log.FatalLevel || level == log.PanicLevel:
		slogLevel = slog.LevelError
	case level == log.WarnLevel:
		slogLevel = slog.LevelWarn
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slogLevel}))
}

func refreshUntilSuccess(ctx context.Context, refresh func() error) {
	for {
		err := refresh()
		if err == nil || errors.Is(err, exporter.ErrRefreshInProgress) || ctx.Err() != nil {
			return
		}
		log.Errorf("Initial refresh failed, retrying in %s: %v", initialRefreshRetryDelay, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(initialRefreshRetryDelay):
		}
	}
}
