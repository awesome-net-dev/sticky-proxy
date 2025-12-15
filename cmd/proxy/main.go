package main

import (
	"log"
	"net/http"

	"sticky-proxy/internal/proxy"
)

func main() {
	p, err := proxy.New()
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", p)
	mux.HandleFunc("/healthz", p.Healthz)
	mux.HandleFunc("/metrics", proxy.Metrics)

	log.Println("proxy listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
