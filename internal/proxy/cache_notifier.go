package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const redisCacheInvalidateChannel = "sticky-proxy:cache-invalidate"

// CacheNotifier broadcasts cache invalidation events across proxy replicas
// so that local UserCache entries don't go stale after drains, rebalances,
// or backend evictions performed by a different replica.
type CacheNotifier interface {
	// Publish sends a backend invalidation event to all replicas.
	Publish(ctx context.Context, backend string) error

	// Subscribe blocks, listening for invalidation events and calling
	// onInvalidate for each received backend. Returns when ctx is cancelled.
	Subscribe(ctx context.Context, onInvalidate func(backend string))
}

// publishNotification publishes a cache invalidation event if the notifier is
// configured. Logs and swallows errors to avoid disrupting callers.
func publishNotification(ctx context.Context, notifier CacheNotifier, backend string) {
	if notifier == nil {
		return
	}
	if err := notifier.Publish(ctx, backend); err != nil {
		slog.Error("cache notifier: publish failed", "backend", backend, "error", err)
	}
}

// subscribeDebouncedNotifier starts a notifier subscriber that debounces
// invalidation events. Events for the same backend within the window are
// collapsed into a single InvalidateBackend call.
func subscribeDebouncedNotifier(ctx context.Context, notifier CacheNotifier, cache *UserCache, window time.Duration) {
	ch := make(chan string, 64)
	go notifier.Subscribe(ctx, func(backend string) {
		select {
		case ch <- backend:
		default:
			slog.Warn("cache notifier: event buffer full, dropping", "backend", backend)
		}
	})

	pending := make(map[string]struct{})
	var timer *time.Timer
	var timerC <-chan time.Time

	for {
		select {
		case backend := <-ch:
			pending[backend] = struct{}{}
			if timer == nil {
				timer = time.NewTimer(window)
				timerC = timer.C
			}
		case <-timerC:
			for backend := range pending {
				cache.InvalidateBackend(backend)
				slog.Debug("cache notifier: invalidated backend (debounced)", "backend", backend)
			}
			clear(pending)
			timer = nil
			timerC = nil
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		}
	}
}

// RedisCacheNotifier implements CacheNotifier using Redis Pub/Sub.
type RedisCacheNotifier struct {
	client *redis.Client
}

// NewRedisCacheNotifier creates a notifier backed by Redis Pub/Sub.
func NewRedisCacheNotifier(client *redis.Client) *RedisCacheNotifier {
	return &RedisCacheNotifier{client: client}
}

func (n *RedisCacheNotifier) Publish(ctx context.Context, backend string) error {
	return n.client.Publish(ctx, redisCacheInvalidateChannel, backend).Err()
}

func (n *RedisCacheNotifier) Subscribe(ctx context.Context, onInvalidate func(backend string)) {
	pubsub := n.client.Subscribe(ctx, redisCacheInvalidateChannel)
	defer func() {
		if err := pubsub.Close(); err != nil {
			slog.Error("cache notifier: failed to close pubsub", "error", err)
		}
	}()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			onInvalidate(msg.Payload)
		}
	}
}
