package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sticky-proxy/internal/proxy"
)

func main() {
	p, err := proxy.New()
	if err != nil {
		log.Fatal(err)
	}

	// Start the active health checker in the background.
	ctx, cancel := context.WithCancel(context.Background())
	go p.HealthChecker.Start(ctx)

	mux := http.NewServeMux()
	mux.Handle("/", p)
	mux.HandleFunc("/healthz", p.Healthz)
	mux.HandleFunc("/metrics", proxy.Metrics)

	srv := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	// Listen for OS signals for graceful shutdown.
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Println("proxy listening on :8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-done
	log.Println("shutting down...")

	// Stop the health checker.
	cancel()

	// Give in-flight requests time to finish.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	log.Println("proxy stopped")
}
