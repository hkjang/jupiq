package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hkjang/jupiq/internal/api"
	"github.com/hkjang/jupiq/internal/auth"
	"github.com/hkjang/jupiq/internal/collector"
	"github.com/hkjang/jupiq/internal/config"
	"github.com/hkjang/jupiq/internal/secure"
	"github.com/hkjang/jupiq/internal/store"
	"github.com/hkjang/jupiq/internal/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration error", "error", err)
		os.Exit(1)
	}
	cipher, err := secure.NewCipher(cfg.EncryptionKey)
	if err != nil {
		logger.Error("encryption initialization failed", "error", err)
		os.Exit(1)
	}
	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	startupCtx, cancel := context.WithTimeout(rootCtx, 30*time.Second)
	database, err := store.Open(startupCtx, cfg.PostgresDSN, cipher)
	cancel()
	if err != nil {
		logger.Error("database initialization failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	seedCtx, seedCancel := context.WithTimeout(rootCtx, 20*time.Second)
	err = database.Seed(seedCtx, cfg.BootstrapAdmin, cfg.BootstrapPassword)
	seedCancel()
	if err != nil {
		logger.Error("bootstrap seed failed", "error", err)
		os.Exit(1)
	}
	authService := auth.NewService(database, cipher)
	handler := api.New(database, authService, logger).Handler()
	httpServer := &http.Server{Addr: config.ListenAddress, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20}
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		collector.New(database, logger).Run(rootCtx)
	}()
	go func() {
		logger.Info("jupiq started", "address", config.ListenAddress, "version", version.Version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			stop()
		}
	}()
	<-rootCtx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
	// The collector bounds its own wait, so this only holds shutdown while
	// in-flight hub probes finish writing their health status before the
	// deferred database.Close closes the pool underneath them.
	<-collectorDone
}
