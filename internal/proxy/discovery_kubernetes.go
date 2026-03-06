package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// epState represents the observed state of a Kubernetes endpoint.
type epState int

const (
	epActive      epState = iota // ready=true, terminating=false
	epTerminating                // serving=true, terminating=true
)

// KubernetesBackendDiscovery watches Kubernetes EndpointSlice resources via
// an informer and reconciles the backends:active set in Redis. It provides
// event-driven discovery with proactive drain on pod termination — the proxy
// sees the terminating signal seconds before health checks or DNS would notice.
type KubernetesBackendDiscovery struct {
	clientset kubernetes.Interface
	namespace string
	selector  string
	portName  string
	redis     *Redis
	drain     *DrainManager

	mu       sync.Mutex
	known    map[string]epState
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewKubernetesBackendDiscovery creates a discovery that watches EndpointSlices
// in the given namespace matching the label selector. It attempts in-cluster
// config first, falling back to KUBECONFIG / ~/.kube/config for local dev.
func NewKubernetesBackendDiscovery(
	namespace, selector, portName string,
	r *Redis,
	drain *DrainManager,
) (*KubernetesBackendDiscovery, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home, _ := os.UserHomeDir()
			kubeconfig = home + "/.kube/config"
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("kubernetes discovery: unable to build client config: %w", err)
		}
	}

	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("kubernetes discovery: unable to create clientset: %w", err)
	}

	return newKubernetesBackendDiscovery(cs, namespace, selector, portName, r, drain), nil
}

// newKubernetesBackendDiscovery is the internal constructor that accepts a
// pre-built clientset, used by tests with a fake client.
func newKubernetesBackendDiscovery(
	cs kubernetes.Interface,
	namespace, selector, portName string,
	r *Redis,
	drain *DrainManager,
) *KubernetesBackendDiscovery {
	if namespace == "" {
		ns, readErr := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
		if readErr == nil && len(ns) > 0 {
			namespace = string(ns)
		} else {
			namespace = "default"
		}
	}

	return &KubernetesBackendDiscovery{
		clientset: cs,
		namespace: namespace,
		selector:  selector,
		portName:  portName,
		redis:     r,
		drain:     drain,
		known:     make(map[string]epState),
		stopCh:    make(chan struct{}),
	}
}

// Start sets up the EndpointSlice informer and blocks until ctx is cancelled
// or Stop is called.
func (k *KubernetesBackendDiscovery) Start(ctx context.Context) {
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()
	defer k.Stop()

	factory := informers.NewSharedInformerFactoryWithOptions(
		k.clientset,
		0, // no periodic resync — rely purely on watch events
		informers.WithNamespace(k.namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = k.selector
		}),
	)

	informer := factory.Discovery().V1().EndpointSlices().Informer()
	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(_ interface{}) { k.reconcile(innerCtx, informer.GetStore()) },
		UpdateFunc: func(_, _ interface{}) { k.reconcile(innerCtx, informer.GetStore()) },
		DeleteFunc: func(_ interface{}) { k.reconcile(innerCtx, informer.GetStore()) },
	})
	if err != nil {
		slog.Error("kubernetes discovery: failed to add event handler", "error", err)
		return
	}

	factory.Start(k.stopCh)

	synced := factory.WaitForCacheSync(k.stopCh)
	for _, ok := range synced {
		if !ok {
			slog.Error("kubernetes discovery: informer cache sync failed")
			return
		}
	}

	slog.Info("kubernetes backend discovery started",
		"namespace", k.namespace,
		"selector", k.selector,
		"portName", k.portName,
	)

	select {
	case <-ctx.Done():
	case <-k.stopCh:
	}
}

// Stop terminates the discovery loop and informer goroutines.
func (k *KubernetesBackendDiscovery) Stop() {
	k.stopOnce.Do(func() { close(k.stopCh) })
}

func (k *KubernetesBackendDiscovery) reconcile(ctx context.Context, store cache.Store) {
	if k.redis == nil {
		return
	}

	items := store.List()
	slices := make([]*discoveryv1.EndpointSlice, 0, len(items))
	for _, obj := range items {
		if s, ok := obj.(*discoveryv1.EndpointSlice); ok {
			slices = append(slices, s)
		}
	}

	desired := k.buildDesiredState(slices)
	k.applyDesiredState(ctx, desired)
}

// buildDesiredState aggregates all endpoints across the given EndpointSlices
// and categorises each by its conditions.
func (k *KubernetesBackendDiscovery) buildDesiredState(slices []*discoveryv1.EndpointSlice) map[string]epState {
	desired := make(map[string]epState)
	for _, slice := range slices {
		port := k.resolvePort(slice.Ports)
		if port == 0 {
			continue
		}

		for _, ep := range slice.Endpoints {
			ready := ep.Conditions.Ready != nil && *ep.Conditions.Ready
			serving := ep.Conditions.Serving != nil && *ep.Conditions.Serving
			terminating := ep.Conditions.Terminating != nil && *ep.Conditions.Terminating

			for _, addr := range ep.Addresses {
				url := fmt.Sprintf("http://%s:%d", addr, port)

				if ready && !terminating {
					desired[url] = epActive
				} else if terminating && serving {
					desired[url] = epTerminating
				}
				// Not ready & not terminating (still starting) or
				// terminating & not serving → ignore entirely.
			}
		}
	}
	return desired
}

// applyDesiredState reconciles the desired endpoint map against the previously
// known state, issuing Redis add/remove and drain operations as needed.
func (k *KubernetesBackendDiscovery) applyDesiredState(ctx context.Context, desired map[string]epState) {
	k.mu.Lock()
	prev := k.known
	k.known = make(map[string]epState, len(desired))
	for url, state := range desired {
		k.known[url] = state
	}
	k.mu.Unlock()

	var added, drained, removed int

	for url, state := range desired {
		prevState, existed := prev[url]

		switch state {
		case epActive:
			// If transitioning from terminating back to active, cancel any
			// in-progress drain (shouldn't happen normally but handle it).
			if existed && prevState == epTerminating && k.drain != nil {
				k.drain.CancelDrain(url)
			}
			if !existed || prevState != epActive {
				if err := k.redis.AddBackend(ctx, url); err != nil {
					slog.Error("kubernetes discovery: failed to add backend",
						"backend", url, "error", err)
					continue
				}
				added++
			}

		case epTerminating:
			if !existed || prevState != epTerminating {
				slog.Info("kubernetes discovery: pod terminating, starting drain",
					"backend", url)
				if k.drain != nil {
					k.drain.StartDrain(url)
				} else {
					if err := k.redis.RemoveBackend(ctx, url); err != nil {
						slog.Error("kubernetes discovery: failed to remove backend",
							"backend", url, "error", err)
					}
				}
				drained++
			}
		}
	}

	// Backends that disappeared from the slice entirely.
	for url := range prev {
		if _, exists := desired[url]; !exists {
			if err := k.redis.RemoveBackend(ctx, url); err != nil {
				slog.Error("kubernetes discovery: failed to remove backend",
					"backend", url, "error", err)
				continue
			}
			removed++
		}
	}

	if added > 0 || drained > 0 || removed > 0 {
		names := make([]string, 0, len(desired))
		for url := range desired {
			names = append(names, url)
		}
		sort.Strings(names)
		slog.Info("kubernetes discovery: reconciled",
			"added", added, "drained", drained, "removed", removed,
			"backends", names)
	}
}

// resolvePort finds the matching port from the EndpointSlice port list.
// If portName is set, it matches by name; otherwise it uses the first port.
func (k *KubernetesBackendDiscovery) resolvePort(ports []discoveryv1.EndpointPort) int32 {
	if len(ports) == 0 {
		return 0
	}
	if k.portName == "" {
		if ports[0].Port != nil {
			return *ports[0].Port
		}
		return 0
	}
	for _, p := range ports {
		if p.Name != nil && *p.Name == k.portName {
			if p.Port != nil {
				return *p.Port
			}
		}
	}
	return 0
}
