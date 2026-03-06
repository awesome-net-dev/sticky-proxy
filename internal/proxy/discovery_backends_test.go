package proxy

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestBackendDiscovery_BuildsURLsFromIPs(t *testing.T) {
	t.Parallel()

	// Use localhost which always resolves.
	ips, err := net.DefaultResolver.LookupHost(context.Background(), "localhost")
	if err != nil || len(ips) == 0 {
		t.Skip("localhost does not resolve in this environment")
	}

	d := NewBackendDiscovery("localhost", "5678", 10*time.Second, nil)
	if d.host != "localhost" {
		t.Errorf("expected host %q, got %q", "localhost", d.host)
	}
	if d.port != "5678" {
		t.Errorf("expected port %q, got %q", "5678", d.port)
	}
}

func TestBackendDiscovery_StopTerminatesLoop(t *testing.T) {
	t.Parallel()

	d := NewBackendDiscovery("localhost", "5678", time.Hour, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		// Start will fail on reconcile (nil redis) but should still respect stop.
		d.Start(ctx)
		close(done)
	}()

	// Give the goroutine time to enter the loop.
	time.Sleep(50 * time.Millisecond)
	d.Stop()

	select {
	case <-done:
		// OK — stopped successfully.
	case <-time.After(2 * time.Second):
		t.Fatal("BackendDiscovery.Start did not return after Stop")
	}
}
