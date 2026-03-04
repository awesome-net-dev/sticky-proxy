package main

import (
	"log/slog"
	"net/http"
	"os"

	"sticky-proxy/internal/proxy"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	p, err := proxy.New()
	if err != nil {
		slog.Error("failed to initialize proxy", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/", p)
	mux.HandleFunc("/healthz", p.Healthz)
	mux.HandleFunc("/metrics", proxy.Metrics)

	slog.Info("proxy listening", "addr", ":8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		slog.Error("http server failed", "error", err)
		os.Exit(1)
	}
}
