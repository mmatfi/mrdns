// Command mrdns runs the DNS zone editing and deployment service.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mmatfi/mrdns/internal/config"
	"github.com/mmatfi/mrdns/internal/deploy"
	"github.com/mmatfi/mrdns/internal/store"
	"github.com/mmatfi/mrdns/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", config.DefaultConfigPath, "path to mrdns.yaml")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if cfg.CookieKeyEphemeral {
		logger.Warn("no cookie key configured; using an ephemeral key (sessions drop on restart)",
			"env", cfg.CookieKeyEnv)
	}
	if !cfg.SecureCookiesEnabled() {
		logger.Warn("secure_cookies is disabled; only acceptable behind TLS or on localhost")
	}

	st, err := store.New(cfg.LiveDir(), cfg.DraftDir(), cfg.BackupDir(), cfg.LockDir(), cfg.BackupKeep)
	if err != nil {
		return err
	}
	pipeline := deploy.New(cfg, st, logger)

	srv, err := web.New(cfg, st, pipeline, logger)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Listen, "zones", len(cfg.Zones), "servers", len(cfg.Servers))
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}
