package proxy

import (
	"log/slog"
	"net/http"
)

func (p *Proxy) Healthz(w http.ResponseWriter, _ *http.Request) {
	if err := p.redis.Ping(); err != nil {
		slog.Error("health check failed: redis down", "error", err)
		http.Error(w, "redis down", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
