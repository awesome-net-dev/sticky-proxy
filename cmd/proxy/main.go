package main

import (
	"log"
	"net/http"

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

	log.Printf("proxy listening on %s", cfg.ProxyPort)
	log.Fatal(http.ListenAndServe(cfg.ProxyPort, mux))
}
