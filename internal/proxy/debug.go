package proxy

import (
	"encoding/json"
	"net/http"
	"time"
)

type debugRoutingResponse struct {
	User       string `json:"user"`
	Backend    string `json:"backend"`
	AssignedAt string `json:"assigned_at,omitempty"`
	Source     string `json:"source"`
	CacheLayer string `json:"cache_layer"`
}

// DebugRoutingHandler returns a handler for GET /debug/routing?user=<key>.
// It checks local cache and Redis to report the current routing state.
func (p *Proxy) DebugRoutingHandler(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user")
	if user == "" {
		http.Error(w, "user query parameter required", http.StatusBadRequest)
		return
	}

	resp := debugRoutingResponse{User: user, CacheLayer: "none", Source: "unknown"}

	// Check local cache.
	if backend, err := p.cache.Get(user); err == nil {
		resp.Backend = backend
		resp.CacheLayer = "local"
		resp.Source = "local_cache"
	} else if p.routingMode == "assignment" {
		// Check assignment store.
		if a, err := p.store.GetAssignment(r.Context(), user); err == nil {
			resp.Backend = a.Backend
			resp.AssignedAt = a.AssignedAt.Format(time.RFC3339)
			resp.Source = a.Source
			resp.CacheLayer = "store"
		}
	} else if p.redis != nil {
		// Hash mode: check sticky:{user} key (Redis only).
		if val, err := p.redis.client.Get(r.Context(), "sticky:"+user).Result(); err == nil {
			resp.Backend = val
			resp.CacheLayer = "redis"
			resp.Source = "hash"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
