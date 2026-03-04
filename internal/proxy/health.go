package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

func (p *Proxy) Healthz(w http.ResponseWriter, _ *http.Request) {
	if err := p.redis.Ping(); err != nil {
		slog.Error("health check failed: redis down", "error", err)
		http.Error(w, "redis down", http.StatusServiceUnavailable)
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
