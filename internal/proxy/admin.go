package proxy

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// AdminAuth returns middleware that protects endpoints with a Bearer token.
// If token is empty, all requests are rejected with 403.
func AdminAuth(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			http.Error(w, "admin endpoints disabled (ADMIN_TOKEN not set)", http.StatusForbidden)
			return
		}
		auth := r.Header.Get("Authorization")
		provided := strings.TrimPrefix(auth, "Bearer ")
		if provided == auth || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

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
	w.Header().Set("Content-Type", "application/json")
	if p.drain == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"draining": []string{}})
		return
	}

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
