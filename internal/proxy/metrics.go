package proxy

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// Counters
// ---------------------------------------------------------------------------

var (
	totalRequests  uint64
	backendErrors  uint64
	redisFailures  uint64
	cacheHitsLocal uint64
	cacheHitsRedis uint64
	cacheMisses    uint64
	authFailures   uint64
	wsConnections  uint64
	rateLimited    uint64
)

// Per-backend request counts: map[backendName] -> *uint64
var backendRequests sync.Map

// IncRequests increments stickyproxy_requests_total.
func IncRequests() { atomic.AddUint64(&totalRequests, 1) }

// IncBackendErrors increments stickyproxy_backend_errors_total.
func IncBackendErrors() { atomic.AddUint64(&backendErrors, 1) }

// IncRedisFailures increments stickyproxy_redis_failures_total.
func IncRedisFailures() { atomic.AddUint64(&redisFailures, 1) }

// IncCacheHitsLocal increments stickyproxy_cache_hits_total{layer="local"}.
func IncCacheHitsLocal() { atomic.AddUint64(&cacheHitsLocal, 1) }

// IncCacheHitsRedis increments stickyproxy_cache_hits_total{layer="redis"}.
func IncCacheHitsRedis() { atomic.AddUint64(&cacheHitsRedis, 1) }

// IncCacheMisses increments stickyproxy_cache_misses_total.
func IncCacheMisses() { atomic.AddUint64(&cacheMisses, 1) }

// IncAuthFailures increments stickyproxy_auth_failures_total.
func IncAuthFailures() { atomic.AddUint64(&authFailures, 1) }

// IncWebSocketConnections increments stickyproxy_websocket_connections_total.
func IncWebSocketConnections() { atomic.AddUint64(&wsConnections, 1) }

// IncRateLimited increments stickyproxy_rate_limited_total.
func IncRateLimited() { atomic.AddUint64(&rateLimited, 1) }

// IncBackendRequests increments stickyproxy_backend_requests_total{backend="name"}.
func IncBackendRequests(backend string) {
	v, _ := backendRequests.LoadOrStore(backend, new(uint64))
	atomic.AddUint64(v.(*uint64), 1)
}

// ---------------------------------------------------------------------------
// Gauges
// ---------------------------------------------------------------------------

var (
	activeConnections int64
	healthyBackends   int64
)

// IncActiveConnections increments stickyproxy_active_connections.
func IncActiveConnections() { atomic.AddInt64(&activeConnections, 1) }

// DecActiveConnections decrements stickyproxy_active_connections.
func DecActiveConnections() { atomic.AddInt64(&activeConnections, -1) }

// SetHealthyBackends sets stickyproxy_healthy_backends gauge.
func SetHealthyBackends(n int64) { atomic.StoreInt64(&healthyBackends, n) }

// ---------------------------------------------------------------------------
// Histogram – stickyproxy_request_duration_seconds
// ---------------------------------------------------------------------------

// histogramBuckets are the upper-inclusive boundaries.
var histogramBuckets = [12]float64{
	0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// requestDuration tracks request latency in a lock-free histogram.
var requestDuration histogram

type histogram struct {
	buckets [12]uint64 // one counter per bucket
	count   uint64
	sum     uint64 // stored as float64 bits via math.Float64bits
}

// RecordRequestDuration records a request latency observation.
func RecordRequestDuration(seconds float64) {
	// Increment matching bucket counters (cumulative).
	for i, bound := range histogramBuckets {
		if seconds <= bound {
			atomic.AddUint64(&requestDuration.buckets[i], 1)
		}
	}
	atomic.AddUint64(&requestDuration.count, 1)

	// Atomically add seconds to sum using CAS on the bit pattern.
	for {
		oldBits := atomic.LoadUint64(&requestDuration.sum)
		oldVal := math.Float64frombits(oldBits)
		newVal := oldVal + seconds
		newBits := math.Float64bits(newVal)
		if atomic.CompareAndSwapUint64(&requestDuration.sum, oldBits, newBits) {
			break
		}
	}
}

// ---------------------------------------------------------------------------
// MetricsHandler – Prometheus exposition format
// ---------------------------------------------------------------------------

// MetricsHandler writes all metrics in Prometheus text exposition format.
func MetricsHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	var b strings.Builder

	// --- Counters ----------------------------------------------------------

	writeCounter(&b, "stickyproxy_requests_total",
		"Total number of requests received",
		atomic.LoadUint64(&totalRequests))

	writeCounter(&b, "stickyproxy_backend_errors_total",
		"Total number of backend errors",
		atomic.LoadUint64(&backendErrors))

	writeCounter(&b, "stickyproxy_redis_failures_total",
		"Total number of Redis failures",
		atomic.LoadUint64(&redisFailures))

	// cache_hits_total with layer label
	b.WriteString("# HELP stickyproxy_cache_hits_total Total cache hits by layer\n")
	b.WriteString("# TYPE stickyproxy_cache_hits_total counter\n")
	b.WriteString("stickyproxy_cache_hits_total{layer=\"local\"} ")
	b.WriteString(u64(atomic.LoadUint64(&cacheHitsLocal)))
	b.WriteByte('\n')
	b.WriteString("stickyproxy_cache_hits_total{layer=\"redis\"} ")
	b.WriteString(u64(atomic.LoadUint64(&cacheHitsRedis)))
	b.WriteByte('\n')

	writeCounter(&b, "stickyproxy_cache_misses_total",
		"Total cache misses (new user, no mapping in local or Redis)",
		atomic.LoadUint64(&cacheMisses))

	writeCounter(&b, "stickyproxy_auth_failures_total",
		"Total JWT authentication failures",
		atomic.LoadUint64(&authFailures))

	writeCounter(&b, "stickyproxy_websocket_connections_total",
		"Total WebSocket connections opened",
		atomic.LoadUint64(&wsConnections))

	writeCounter(&b, "stickyproxy_rate_limited_total",
		"Total requests rejected by rate limiter",
		atomic.LoadUint64(&rateLimited))

	// per-backend request counts
	b.WriteString("# HELP stickyproxy_backend_requests_total Total requests per backend\n")
	b.WriteString("# TYPE stickyproxy_backend_requests_total counter\n")
	backendRequests.Range(func(key, value any) bool {
		name := key.(string)
		count := atomic.LoadUint64(value.(*uint64))
		b.WriteString("stickyproxy_backend_requests_total{backend=\"")
		b.WriteString(name)
		b.WriteString("\"} ")
		b.WriteString(u64(count))
		b.WriteByte('\n')
		return true
	})

	// --- Gauges ------------------------------------------------------------

	writeGauge(&b, "stickyproxy_active_connections",
		"Number of currently active connections",
		atomic.LoadInt64(&activeConnections))

	writeGauge(&b, "stickyproxy_healthy_backends",
		"Number of healthy backends",
		atomic.LoadInt64(&healthyBackends))

	// --- Histogram ---------------------------------------------------------

	b.WriteString("# HELP stickyproxy_request_duration_seconds Request latency histogram\n")
	b.WriteString("# TYPE stickyproxy_request_duration_seconds histogram\n")

	// Bucket counters are stored non-cumulatively; Prometheus requires
	// cumulative buckets, so we accumulate on read.
	var cumulative uint64
	for i, bound := range histogramBuckets {
		cumulative += atomic.LoadUint64(&requestDuration.buckets[i])
		b.WriteString("stickyproxy_request_duration_seconds_bucket{le=\"")
		b.WriteString(ftoa(bound))
		b.WriteString("\"} ")
		b.WriteString(u64(cumulative))
		b.WriteByte('\n')
	}
	// +Inf bucket equals total count.
	totalCount := atomic.LoadUint64(&requestDuration.count)
	b.WriteString("stickyproxy_request_duration_seconds_bucket{le=\"+Inf\"} ")
	b.WriteString(u64(totalCount))
	b.WriteByte('\n')

	b.WriteString("stickyproxy_request_duration_seconds_sum ")
	sumBits := atomic.LoadUint64(&requestDuration.sum)
	b.WriteString(ftoa(math.Float64frombits(sumBits)))
	b.WriteByte('\n')

	b.WriteString("stickyproxy_request_duration_seconds_count ")
	b.WriteString(u64(totalCount))
	b.WriteByte('\n')

	w.Write([]byte(b.String()))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeCounter(b *strings.Builder, name, help string, val uint64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n%s %s\n",
		name, help, name, name, u64(val))
}

func writeGauge(b *strings.Builder, name, help string, val int64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n",
		name, help, name, name, i64(val))
}

func u64(v uint64) string { return strconv.FormatUint(v, 10) }
func i64(v int64) string  { return strconv.FormatInt(v, 10) }

func ftoa(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
