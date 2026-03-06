package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

func (p *Proxy) Healthz(w http.ResponseWriter, r *http.Request) {
	if err := p.store.Ping(r.Context()); err != nil {
		slog.Error("health check failed: store down", "error", err)
		http.Error(w, "store down", http.StatusServiceUnavailable)
		return
	}

	healthy := p.HealthChecker.HealthyCount()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":           "ok",
		"healthy_backends": healthy,
	})
}
