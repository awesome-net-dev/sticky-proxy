package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sticky-proxy/internal/config"
	"sticky-proxy/internal/proxy"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	p, err := proxy.New(cfg)
	if err != nil {
		slog.Error("failed to initialize proxy", "error", err)
		os.Exit(1)
	}

	// Start the active health checker in the background.
	healthCtx, healthCancel := context.WithCancel(context.Background())
	go p.HealthChecker.Start(healthCtx)

	mux := http.NewServeMux()
	mux.Handle("/", p)
	mux.HandleFunc("/healthz", p.Healthz)
	mux.HandleFunc("/metrics", proxy.Metrics)

	srv := &http.Server{
		Addr:    cfg.ProxyPort,
		Handler: mux,
	}

	// Start server in a goroutine so it doesn't block signal handling.
	go func() {
		slog.Info("proxy listening", "addr", cfg.ProxyPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("proxy listen error", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for SIGTERM or SIGINT.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit
	slog.Info("received signal, shutting down proxy", "signal", sig.String())

	// Stop the health checker.
	healthCancel()

	// Give in-flight requests up to 30 seconds to complete.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("proxy forced shutdown", "error", err)
		os.Exit(1)
	}

	slog.Info("proxy shutdown complete")
}
