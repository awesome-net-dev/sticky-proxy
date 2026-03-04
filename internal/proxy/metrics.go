package proxy

import (
	"net/http"
	"strconv"
	"sync/atomic"
)

var (
	totalRequests uint64
	redisFailures uint64
	backendErrors uint64
	rateLimited   uint64
)

func Metrics(w http.ResponseWriter, _ *http.Request) {
	w.Write([]byte(
		`# HELP stickyproxy_requests_total Total requests
# TYPE stickyproxy_requests_total counter
stickyproxy_requests_total ` + itoa(atomic.LoadUint64(&totalRequests)) + `

# HELP stickyproxy_redis_failures_total Redis failures
# TYPE stickyproxy_redis_failures_total counter
stickyproxy_redis_failures_total ` + itoa(atomic.LoadUint64(&redisFailures)) + `

# HELP stickyproxy_backend_errors_total Backend errors
# TYPE stickyproxy_backend_errors_total counter
stickyproxy_backend_errors_total ` + itoa(atomic.LoadUint64(&backendErrors)) + `

# HELP stickyproxy_rate_limited_total Requests rejected by rate limiter
# TYPE stickyproxy_rate_limited_total counter
stickyproxy_rate_limited_total ` + itoa(atomic.LoadUint64(&rateLimited)) + `
`))
}

func itoa(v uint64) string {
	return strconv.FormatUint(v, 10)
}
