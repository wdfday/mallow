package metrics

import (
	"strings"
	"testing"
)

func TestRenderExposition(t *testing.T) {
	RequestStarted()
	RequestFinished("GET", "/api/v1/helms", 200, 12)
	RequestStarted()
	RequestFinished("GET", "/api/v1/helms", 200, 8)
	AuthRejection("invalid_token")
	RateLimitBlocked()
	ProxyError("helm:8084")
	WSConnOpened()
	WSFrame("market")

	out := Render()

	for _, want := range []string{
		`gateway_http_requests_total{method="GET",route="/api/v1/helms",status="200"} 2`,
		`# TYPE gateway_http_requests_total counter`,
		`gateway_auth_rejections_total{reason="invalid_token"} 1`,
		`gateway_ratelimit_blocked_total 1`,
		`gateway_proxy_errors_total{upstream="helm:8084"} 1`,
		`gateway_ws_connections 1`,
		`gateway_ws_frames_total{direction="market"} 1`,
		`gateway_http_request_duration_seconds_sum{route="/api/v1/helms"} 0.02`,
		`gateway_http_in_flight 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing line:\n  %s\n--- full ---\n%s", want, out)
		}
	}
}
