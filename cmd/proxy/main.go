package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sticky-proxy/internal/config"
	"sticky-proxy/internal/proxy"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	p, err := proxy.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", p)
	mux.HandleFunc("/healthz", p.Healthz)
	mux.HandleFunc("/metrics", proxy.Metrics)

	srv := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	// Start server in a goroutine so it doesn't block signal handling.
	go func() {
		log.Printf("proxy listening on %s", cfg.ProxyPort)
		if err := http.ListenAndServe(cfg.ProxyPort, mux); err != nil && err != http.ErrServerClosed {
			log.Fatalf("proxy listen error: %v", err)
		}
	}()

	// Wait for SIGTERM or SIGINT.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit
	log.Printf("received signal %s, shutting down proxy...", sig)

	// Give in-flight requests up to 30 seconds to complete.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("proxy forced shutdown: %v", err)
	}

	log.Println("proxy shutdown complete")
}
