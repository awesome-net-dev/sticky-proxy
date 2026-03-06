package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"time"
)

// BackendDiscovery periodically resolves a DNS hostname and reconciles the
// resulting IPs against the backends:active set in Redis. This enables
// auto-discovery of backend pods via Kubernetes headless services or Docker
// Compose service names without requiring backends to self-register.
type BackendDiscovery struct {
	host     string
	port     string
	interval time.Duration
	redis    *Redis
	stopCh   chan struct{}
}

// NewBackendDiscovery creates a BackendDiscovery that resolves the given host
// and builds http://{ip}:{port} URLs for each resolved address.
func NewBackendDiscovery(host, port string, interval time.Duration, r *Redis) *BackendDiscovery {
	return &BackendDiscovery{
		host:     host,
		port:     port,
		interval: interval,
		redis:    r,
		stopCh:   make(chan struct{}),
	}
}

// Start runs the discovery loop. Call from a goroutine.
func (d *BackendDiscovery) Start(ctx context.Context) {
	d.reconcile(ctx)

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.reconcile(ctx)
		}
	}
}

// Stop terminates the discovery loop.
func (d *BackendDiscovery) Stop() {
	close(d.stopCh)
}

func (d *BackendDiscovery) reconcile(ctx context.Context) {
	if d.redis == nil {
		return
	}

	ips, err := net.DefaultResolver.LookupHost(ctx, d.host)
	if err != nil {
		slog.Error("backend discovery: DNS lookup failed", "host", d.host, "error", err)
		return
	}

	discovered := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		url := fmt.Sprintf("http://%s:%s", ip, d.port)
		discovered[url] = struct{}{}
	}

	current, err := d.redis.ActiveBackends(ctx)
	if err != nil {
		slog.Error("backend discovery: failed to fetch active backends", "error", err)
		return
	}

	currentSet := make(map[string]struct{}, len(current))
	for _, b := range current {
		currentSet[b] = struct{}{}
	}

	// Add newly discovered backends.
	var added int
	for url := range discovered {
		if _, exists := currentSet[url]; !exists {
			if err := d.redis.AddBackend(ctx, url); err != nil {
				slog.Error("backend discovery: failed to add backend", "backend", url, "error", err)
				continue
			}
			added++
		}
	}

	// Remove backends no longer in DNS.
	var removed int
	for _, url := range current {
		if _, exists := discovered[url]; !exists {
			if err := d.redis.RemoveBackend(ctx, url); err != nil {
				slog.Error("backend discovery: failed to remove backend", "backend", url, "error", err)
				continue
			}
			removed++
		}
	}

	if added > 0 || removed > 0 {
		names := make([]string, 0, len(discovered))
		for url := range discovered {
			names = append(names, url)
		}
		sort.Strings(names)
		slog.Info("backend discovery: reconciled", "added", added, "removed", removed, "backends", names)
	}
}
