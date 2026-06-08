package handler

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// heraldTransport is used for the herald reverse proxy.
// ResponseHeaderTimeout is set generously to accommodate long-running backtests
// (engine can take several minutes on large date ranges) while still releasing
// goroutines if herald crashes or deadlocks mid-request.
var heraldTransport http.RoundTripper = &http.Transport{
	ResponseHeaderTimeout: 10 * time.Minute,
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   10,
	IdleConnTimeout:       90 * time.Second,
}

// IdentityProxy returns a gin handler that reverse-proxies all requests to the identity service.
func IdentityProxy(identityURL string) gin.HandlerFunc {
	return newProxy(identityURL)
}

// HelmProxy returns a gin handler that reverse-proxies to the helm service.
// /api/v1/helms/* and /api/v1/hands/* are rewritten to /api/* (helm's internal prefix).
// /api/v1/accounts/* and /api/v1/broker-connections/* are forwarded unchanged.
func HelmProxy(orchestratorURL string) gin.HandlerFunc {
	return newProxy(orchestratorURL, func(path string) string {
		if rest, ok := strings.CutPrefix(path, "/swagger/helm/"); ok {
			return "/swagger/" + rest
		}
		if rest, ok := strings.CutPrefix(path, "/api/v1/helms"); ok {
			return "/api/helms" + rest
		}
		if rest, ok := strings.CutPrefix(path, "/api/v1/hands"); ok {
			return "/api/hands" + rest
		}
		return path
	})
}

// HeraldProxy proxies requests to the Rust herald service.
// Uses heraldTransport with a 10-minute ResponseHeaderTimeout to handle
// long-running backtest requests without leaking goroutines on herald failures.
//
// Path rewriting:
//   - /swagger/herald        → /swagger/ui   (utoipa serves UI here)
//   - /swagger/herald/*rest  → /swagger/*rest
//   - all other paths         → unchanged (herald mounts at /api/v1/*)
func HeraldProxy(heraldURL string) gin.HandlerFunc {
	target, err := url.Parse(heraldURL)
	if err != nil {
		panic("invalid herald URL: " + heraldURL + ": " + err.Error())
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = heraldTransport
	proxy.ModifyResponse = stripUpstreamCORS

	rewrite := func(path string) string {
		if path == "/swagger/herald" {
			return "/swagger/ui"
		}
		if rest, ok := strings.CutPrefix(path, "/swagger/herald/"); ok {
			return "/swagger/" + rest
		}
		return path
	}

	return func(c *gin.Context) {
		c.Request.Host = target.Host
		c.Request.URL.Path = rewrite(c.Request.URL.Path)
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func newProxy(rawURL string, rewritePath ...func(string) string) gin.HandlerFunc {
	target, err := url.Parse(rawURL)
	if err != nil {
		panic("invalid proxy URL: " + rawURL + ": " + err.Error())
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = stripUpstreamCORS
	var rewrite func(string) string
	if len(rewritePath) > 0 {
		rewrite = rewritePath[0]
	}
	return func(c *gin.Context) {
		c.Request.Host = target.Host
		if rewrite != nil {
			c.Request.URL.Path = rewrite(c.Request.URL.Path)
		}
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func stripUpstreamCORS(resp *http.Response) error {
	resp.Header.Del("Access-Control-Allow-Origin")
	resp.Header.Del("Access-Control-Allow-Credentials")
	resp.Header.Del("Access-Control-Allow-Methods")
	resp.Header.Del("Access-Control-Allow-Headers")
	resp.Header.Del("Access-Control-Max-Age")
	return nil
}
