package proxy

import (
	"encoding/json"
	"net/http"
)

// AdminDrainHandler handles POST /admin/drain?backend=<url>.
func (p *Proxy) AdminDrainHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	backend := r.URL.Query().Get("backend")
	if backend == "" {
		http.Error(w, "backend query parameter required", http.StatusBadRequest)
		return
	}

	if p.drain == nil {
		http.Error(w, "drain not configured", http.StatusServiceUnavailable)
		return
	}

	p.drain.StartDrain(backend)
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "draining", "backend": backend})
}

// AdminDrainStatusHandler handles GET /admin/drain.
func (p *Proxy) AdminDrainStatusHandler(w http.ResponseWriter, _ *http.Request) {
	if p.drain == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"draining": []string{}})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"draining": p.drain.DrainingBackends()})
}

// AdminCancelDrainHandler handles DELETE /admin/drain?backend=<url>.
func (p *Proxy) AdminCancelDrainHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	backend := r.URL.Query().Get("backend")
	if backend == "" {
		http.Error(w, "backend query parameter required", http.StatusBadRequest)
		return
	}

	if p.drain == nil {
		http.Error(w, "drain not configured", http.StatusServiceUnavailable)
		return
	}

	p.drain.CancelDrain(backend)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "cancelled", "backend": backend})
}
