// Package metrics provides a small dependency-free Prometheus exposition for the
// gateway. Matches the repo precedent (helm hand-rolls text/plain v0.0.4) rather
// than pulling in the prometheus client library.
//
// Metrics:
//
//	gateway_http_requests_total{method,route,status}  counter
//	gateway_http_request_duration_seconds_sum{route}  counter (with _count → avg)
//	gateway_http_request_duration_seconds_count{route} counter
//	gateway_http_in_flight                             gauge
//	gateway_proxy_errors_total{upstream}               counter
//	gateway_auth_rejections_total{reason}              counter
//	gateway_ratelimit_blocked_total                    counter
//	gateway_ws_connections                             gauge
//	gateway_ws_frames_total{direction}                 counter
package metrics

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// labeledCounter is a thread-safe counter keyed by a rendered label set.
type labeledCounter struct {
	mu sync.Mutex
	v  map[string]uint64 // label-string → value
}

func newLabeled() *labeledCounter { return &labeledCounter{v: make(map[string]uint64)} }

func (c *labeledCounter) inc(labels string) { c.add(labels, 1) }

func (c *labeledCounter) add(labels string, n uint64) {
	c.mu.Lock()
	c.v[labels] += n
	c.mu.Unlock()
}

func (c *labeledCounter) snapshot() map[string]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]uint64, len(c.v))
	maps.Copy(out, c.v)
	return out
}

var (
	httpRequests   = newLabeled()
	durationSumMs  = newLabeled() // route → summed ms (rendered as seconds)
	durationCount  = newLabeled() // route → request count
	proxyErrors    = newLabeled()
	authRejections = newLabeled()
	wsFrames       = newLabeled()

	rateLimitBlocked atomic.Uint64
	inFlight         atomic.Int64
	wsConnections    atomic.Int64
)

// ── recording API ────────────────────────────────────────────────────────────

func RequestStarted() { inFlight.Add(1) }
func RequestFinished(method, route string, status int, durMs float64) {
	inFlight.Add(-1)
	httpRequests.inc(labels("method", method, "route", route, "status", itoa(status)))
	r := labels("route", route)
	durationCount.inc(r)
	durationSumMs.add(r, uint64(durMs))
}

func ProxyError(upstream string)  { proxyErrors.inc(labels("upstream", upstream)) }
func AuthRejection(reason string) { authRejections.inc(labels("reason", reason)) }
func RateLimitBlocked()           { rateLimitBlocked.Add(1) }

func WSConnOpened()      { wsConnections.Add(1) }
func WSConnClosed()      { wsConnections.Add(-1) }
func WSFrame(dir string) { wsFrames.inc(labels("direction", dir)) }

// ── exposition ─────────────────────────────────────────────────────────────

// Render returns the full Prometheus text exposition (text/plain; version=0.0.4).
func Render() string {
	var b strings.Builder

	writeCounter(&b, "gateway_http_requests_total", "Total HTTP requests handled by the gateway.", httpRequests)
	writeCounter(&b, "gateway_http_request_duration_seconds_count", "HTTP request count per route.", durationCount)
	writeDurationSum(&b, durationSumMs)
	writeGaugeScalar(&b, "gateway_http_in_flight", "In-flight HTTP requests.", float64(inFlight.Load()))
	writeCounter(&b, "gateway_proxy_errors_total", "Upstream proxy errors.", proxyErrors)
	writeCounter(&b, "gateway_auth_rejections_total", "Rejected requests by reason.", authRejections)
	writeCounterScalar(&b, "gateway_ratelimit_blocked_total", "Requests blocked by the rate limiter.", rateLimitBlocked.Load())
	writeGaugeScalar(&b, "gateway_ws_connections", "Active WebSocket connections.", float64(wsConnections.Load()))
	writeCounter(&b, "gateway_ws_frames_total", "WebSocket frames sent to clients.", wsFrames)

	return b.String()
}

func writeCounter(b *strings.Builder, name, help string, c *labeledCounter) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
	emit(b, name, c.snapshot())
}

// duration sum is stored in ms but exposed in seconds.
func writeDurationSum(b *strings.Builder, c *labeledCounter) {
	const name = "gateway_http_request_duration_seconds_sum"
	fmt.Fprintf(b, "# HELP %s Summed HTTP request duration per route (seconds).\n# TYPE %s counter\n", name, name)
	snap := c.snapshot()
	keys := sortedKeys(snap)
	for _, k := range keys {
		fmt.Fprintf(b, "%s{%s} %g\n", name, k, float64(snap[k])/1000.0)
	}
}

func writeCounterScalar(b *strings.Builder, name, help string, v uint64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
}

func writeGaugeScalar(b *strings.Builder, name, help string, v float64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, v)
}

func emit(b *strings.Builder, name string, snap map[string]uint64) {
	for _, k := range sortedKeys(snap) {
		fmt.Fprintf(b, "%s{%s} %d\n", name, k, snap[k])
	}
}

func sortedKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// labels builds a Prometheus label string `k1="v1",k2="v2"` from k,v pairs,
// escaping values per the exposition format.
func labels(kv ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(kv[i])
		b.WriteString(`="`)
		b.WriteString(escape(kv[i+1]))
		b.WriteString(`"`)
	}
	return b.String()
}

func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
