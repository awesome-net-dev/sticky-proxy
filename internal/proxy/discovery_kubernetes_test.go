package proxy

import (
	"testing"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func ptr[T any](v T) *T { return &v }

func TestBuildDesiredState_ClassifiesEndpoints(t *testing.T) {
	t.Parallel()

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "http", nil, nil)

	slices := []*discoveryv1.EndpointSlice{
		{
			Ports: []discoveryv1.EndpointPort{
				{Name: ptr("http"), Port: ptr(int32(8080))},
			},
			Endpoints: []discoveryv1.Endpoint{
				{
					Addresses:  []string{"10.0.0.1"},
					Conditions: discoveryv1.EndpointConditions{Ready: ptr(true), Serving: ptr(true), Terminating: ptr(false)},
				},
				{
					Addresses:  []string{"10.0.0.2"},
					Conditions: discoveryv1.EndpointConditions{Ready: ptr(false), Serving: ptr(true), Terminating: ptr(true)},
				},
				{
					Addresses:  []string{"10.0.0.3"},
					Conditions: discoveryv1.EndpointConditions{Ready: ptr(false), Serving: ptr(false), Terminating: ptr(false)},
				},
				{
					Addresses:  []string{"10.0.0.4"},
					Conditions: discoveryv1.EndpointConditions{Ready: ptr(false), Serving: ptr(false), Terminating: ptr(true)},
				},
			},
		},
	}

	desired := k.buildDesiredState(slices)

	if desired["http://10.0.0.1:8080"] != epActive {
		t.Error("expected 10.0.0.1 to be active")
	}
	if desired["http://10.0.0.2:8080"] != epTerminating {
		t.Error("expected 10.0.0.2 to be terminating")
	}
	if _, ok := desired["http://10.0.0.3:8080"]; ok {
		t.Error("expected 10.0.0.3 to be absent (starting, not ready)")
	}
	if _, ok := desired["http://10.0.0.4:8080"]; ok {
		t.Error("expected 10.0.0.4 to be absent (terminating, not serving)")
	}
}

func TestBuildDesiredState_IPv6Addresses(t *testing.T) {
	t.Parallel()

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "http", nil, nil)

	slices := []*discoveryv1.EndpointSlice{
		{
			Ports: []discoveryv1.EndpointPort{
				{Name: ptr("http"), Port: ptr(int32(8080))},
			},
			Endpoints: []discoveryv1.Endpoint{
				{
					Addresses:  []string{"fd00::1"},
					Conditions: discoveryv1.EndpointConditions{Ready: ptr(true), Serving: ptr(true), Terminating: ptr(false)},
				},
			},
		},
	}

	desired := k.buildDesiredState(slices)

	// IPv6 addresses must be bracketed in URLs.
	want := "http://[fd00::1]:8080"
	if desired[want] != epActive {
		t.Errorf("expected IPv6 URL %q to be active, got keys: %v", want, keys(desired))
	}
}

func TestBuildDesiredState_MultipleSlices(t *testing.T) {
	t.Parallel()

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "", nil, nil)

	slices := []*discoveryv1.EndpointSlice{
		{
			Ports: []discoveryv1.EndpointPort{
				{Name: ptr("http"), Port: ptr(int32(8080))},
			},
			Endpoints: []discoveryv1.Endpoint{
				{
					Addresses:  []string{"10.0.0.1"},
					Conditions: discoveryv1.EndpointConditions{Ready: ptr(true), Serving: ptr(true), Terminating: ptr(false)},
				},
			},
		},
		{
			Ports: []discoveryv1.EndpointPort{
				{Name: ptr("http"), Port: ptr(int32(8080))},
			},
			Endpoints: []discoveryv1.Endpoint{
				{
					Addresses:  []string{"10.0.0.2"},
					Conditions: discoveryv1.EndpointConditions{Ready: ptr(true), Serving: ptr(true), Terminating: ptr(false)},
				},
			},
		},
	}

	desired := k.buildDesiredState(slices)

	if len(desired) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(desired))
	}
	if desired["http://10.0.0.1:8080"] != epActive {
		t.Error("expected 10.0.0.1 to be active")
	}
	if desired["http://10.0.0.2:8080"] != epActive {
		t.Error("expected 10.0.0.2 to be active")
	}
}

func TestBuildDesiredState_NilConditions(t *testing.T) {
	t.Parallel()

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "", nil, nil)

	slices := []*discoveryv1.EndpointSlice{
		{
			Ports: []discoveryv1.EndpointPort{
				{Port: ptr(int32(9090))},
			},
			Endpoints: []discoveryv1.Endpoint{
				{
					Addresses:  []string{"10.0.0.1"},
					Conditions: discoveryv1.EndpointConditions{},
				},
			},
		},
	}

	desired := k.buildDesiredState(slices)

	if _, ok := desired["http://10.0.0.1:9090"]; ok {
		t.Error("expected endpoint with nil conditions to be absent")
	}
}

func TestApplyDesiredState_Transitions(t *testing.T) {
	t.Parallel()

	// Redis with unreachable address — operations fail but don't panic.
	r, _ := NewRedis("localhost:1", 1, 0, 5, time.Second)

	tests := []struct {
		name      string
		prev      map[string]epState
		desired   map[string]epState
		wantKnown map[string]epState
	}{
		{
			name:      "new active backend",
			prev:      map[string]epState{},
			desired:   map[string]epState{"http://10.0.0.1:8080": epActive},
			wantKnown: map[string]epState{"http://10.0.0.1:8080": epActive},
		},
		{
			name:      "backend removed",
			prev:      map[string]epState{"http://10.0.0.1:8080": epActive},
			desired:   map[string]epState{},
			wantKnown: map[string]epState{},
		},
		{
			name:      "active to terminating",
			prev:      map[string]epState{"http://10.0.0.1:8080": epActive},
			desired:   map[string]epState{"http://10.0.0.1:8080": epTerminating},
			wantKnown: map[string]epState{"http://10.0.0.1:8080": epTerminating},
		},
		{
			name:      "terminating to active",
			prev:      map[string]epState{"http://10.0.0.1:8080": epTerminating},
			desired:   map[string]epState{"http://10.0.0.1:8080": epActive},
			wantKnown: map[string]epState{"http://10.0.0.1:8080": epActive},
		},
		{
			name:      "no change keeps state",
			prev:      map[string]epState{"http://10.0.0.1:8080": epActive},
			desired:   map[string]epState{"http://10.0.0.1:8080": epActive},
			wantKnown: map[string]epState{"http://10.0.0.1:8080": epActive},
		},
		{
			name: "mixed transitions",
			prev: map[string]epState{
				"http://10.0.0.1:8080": epActive,
				"http://10.0.0.2:8080": epActive,
				"http://10.0.0.3:8080": epTerminating,
			},
			desired: map[string]epState{
				"http://10.0.0.1:8080": epActive,      // unchanged
				"http://10.0.0.2:8080": epTerminating,  // active → terminating
				"http://10.0.0.4:8080": epActive,       // new
				// 10.0.0.3 removed
			},
			wantKnown: map[string]epState{
				"http://10.0.0.1:8080": epActive,
				"http://10.0.0.2:8080": epTerminating,
				"http://10.0.0.4:8080": epActive,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "http", r, nil)
			k.known = tt.prev

			k.applyDesiredState(t.Context(), tt.desired)

			k.mu.Lock()
			defer k.mu.Unlock()
			if len(k.known) != len(tt.wantKnown) {
				t.Fatalf("known map length: got %d, want %d", len(k.known), len(tt.wantKnown))
			}
			for url, wantState := range tt.wantKnown {
				if gotState, ok := k.known[url]; !ok || gotState != wantState {
					t.Errorf("known[%s]: got %v (exists=%v), want %v", url, gotState, ok, wantState)
				}
			}
		})
	}
}

func TestApplyDesiredState_DrainOnTerminating(t *testing.T) {
	t.Parallel()

	r, _ := NewRedis("localhost:1", 1, 0, 5, time.Second)
	cache := NewUserCache(time.Minute)
	t.Cleanup(cache.Stop)
	drain := NewDrainManager(r, nil, cache, "hash", time.Minute, 10)

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "http", r, drain)
	k.known = map[string]epState{"http://10.0.0.1:8080": epActive}

	k.applyDesiredState(t.Context(), map[string]epState{
		"http://10.0.0.1:8080": epTerminating,
	})

	// StartDrain sets the draining map entry synchronously before launching
	// its background goroutine, so IsDraining is true immediately.
	if !drain.IsDraining("http://10.0.0.1:8080") {
		t.Error("expected backend to be draining after terminating transition")
	}
}

func TestApplyDesiredState_CancelDrainOnRecovery(t *testing.T) {
	t.Parallel()

	r, _ := NewRedis("localhost:1", 1, 0, 5, time.Second)
	cache := NewUserCache(time.Minute)
	t.Cleanup(cache.Stop)
	drain := NewDrainManager(r, nil, cache, "hash", time.Minute, 10)

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "http", r, drain)

	// First transition: active → terminating (starts drain).
	k.known = map[string]epState{"http://10.0.0.1:8080": epActive}
	k.applyDesiredState(t.Context(), map[string]epState{
		"http://10.0.0.1:8080": epTerminating,
	})
	if !drain.IsDraining("http://10.0.0.1:8080") {
		t.Fatal("expected drain to start")
	}

	// Second transition: terminating → active (cancels drain).
	k.applyDesiredState(t.Context(), map[string]epState{
		"http://10.0.0.1:8080": epActive,
	})

	// CancelDrain fires the cancel func; the goroutine will clean up.
	// Give it a moment to process.
	time.Sleep(50 * time.Millisecond)
	if drain.IsDraining("http://10.0.0.1:8080") {
		t.Error("expected drain to be cancelled after recovery")
	}
}

func TestResolvePort_MatchesByName(t *testing.T) {
	t.Parallel()

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "grpc", nil, nil)

	ports := []discoveryv1.EndpointPort{
		{Name: ptr("http"), Port: ptr(int32(8080))},
		{Name: ptr("grpc"), Port: ptr(int32(9090))},
	}

	if got := k.resolvePort(ports); got != 9090 {
		t.Errorf("expected port 9090, got %d", got)
	}
}

func TestResolvePort_FirstPortWhenNameEmpty(t *testing.T) {
	t.Parallel()

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "", nil, nil)

	ports := []discoveryv1.EndpointPort{
		{Name: ptr("http"), Port: ptr(int32(8080))},
		{Name: ptr("grpc"), Port: ptr(int32(9090))},
	}

	if got := k.resolvePort(ports); got != 8080 {
		t.Errorf("expected port 8080, got %d", got)
	}
}

func TestResolvePort_ZeroOnNoMatch(t *testing.T) {
	t.Parallel()

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "metrics", nil, nil)

	ports := []discoveryv1.EndpointPort{
		{Name: ptr("http"), Port: ptr(int32(8080))},
	}

	if got := k.resolvePort(ports); got != 0 {
		t.Errorf("expected port 0, got %d", got)
	}
}

func TestResolvePort_ZeroOnEmptySlice(t *testing.T) {
	t.Parallel()

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "http", nil, nil)

	if got := k.resolvePort(nil); got != 0 {
		t.Errorf("expected port 0, got %d", got)
	}
}

func TestKubernetesDiscovery_StopTerminatesLoop(t *testing.T) {
	t.Parallel()

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "http", nil, nil)

	done := make(chan struct{})
	go func() {
		k.Start(t.Context())
		close(done)
	}()

	k.Stop()

	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("KubernetesBackendDiscovery.Start did not return after Stop")
	}
}

func TestKubernetesDiscovery_StopIsIdempotent(t *testing.T) {
	t.Parallel()

	k := newKubernetesBackendDiscovery(fake.NewClientset(), "default", "", "http", nil, nil)

	// Calling Stop twice should not panic.
	k.Stop()
	k.Stop()
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
