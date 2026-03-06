package proxy

import (
	"testing"

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
