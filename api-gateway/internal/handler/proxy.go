package handler

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// IdentityProxy returns a gin handler that reverse-proxies all requests to the identity service.
func IdentityProxy(identityURL string) gin.HandlerFunc {
	return newProxy(identityURL)
}

// InvestmentProxy returns a gin handler that reverse-proxies to the investment service.
// All routes behind this proxy require a valid JWT (enforced by the calling route group).
func InvestmentProxy(investmentURL string) gin.HandlerFunc {
	return newProxy(investmentURL, func(path string) string {
		if rest, ok := strings.CutPrefix(path, "/swagger/investment/"); ok {
			return "/swagger/" + rest
		}
		return path
	})
}

// OrchestratorProxy returns a gin handler that reverse-proxies to the orchestrator service.
// Public gateway paths use /api/v1/orchestrator/* and are rewritten to the upstream /api/* surface.
func OrchestratorProxy(orchestratorURL string) gin.HandlerFunc {
	return newProxy(orchestratorURL, func(path string) string {
		if rest, ok := strings.CutPrefix(path, "/swagger/orchestrator/"); ok {
			return "/swagger/" + rest
		}
		if rest, ok := strings.CutPrefix(path, "/api/v1/orchestrator/"); ok {
			return "/api/" + rest
		}
		if path == "/api/v1/orchestrator" {
			return "/api"
		}
		return path
	})
}

// StrategistProxy handles both HTTP and WebSocket requests proxied to the strategist service.
// Gateway paths use /api/v1/strategist/* and are rewritten to the upstream root surface.
func StrategistProxy(strategistURL string) gin.HandlerFunc {
	target, err := url.Parse(strategistURL)
	if err != nil {
		panic("invalid strategist URL: " + strategistURL + ": " + err.Error())
	}

	rewrite := func(path string) string {
		if rest, ok := strings.CutPrefix(path, "/api/v1/strategist/"); ok {
			return "/" + rest
		}
		if path == "/api/v1/strategist" {
			return "/"
		}
		return path
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	return func(c *gin.Context) {
		upstreamPath := rewrite(c.Request.URL.Path)
		c.Request.Host = target.Host

		if isWSUpgrade(c.Request) {
			proxyWS(c, target, upstreamPath)
			return
		}

		c.Request.URL.Path = upstreamPath
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

// isWSUpgrade reports whether the request is a WebSocket upgrade.
func isWSUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// proxyWS proxies a WebSocket connection to the upstream target at the given path.
func proxyWS(c *gin.Context, target *url.URL, path string) {
	wsScheme := "ws"
	if target.Scheme == "https" {
		wsScheme = "wss"
	}
	upstreamURL := url.URL{
		Scheme:   wsScheme,
		Host:     target.Host,
		Path:     path,
		RawQuery: c.Request.URL.RawQuery,
	}

	// Forward injected user headers to the upstream WebSocket.
	forwardHeaders := http.Header{}
	for _, h := range []string{"X-User-ID", "X-User-Role", "X-User-Email"} {
		if v := c.Request.Header.Get(h); v != "" {
			forwardHeaders.Set(h, v)
		}
	}

	upstreamConn, _, err := websocket.DefaultDialer.Dial(upstreamURL.String(), forwardHeaders)
	if err != nil {
		c.AbortWithStatus(http.StatusBadGateway)
		return
	}
	defer upstreamConn.Close()

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	clientConn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer clientConn.Close()

	errc := make(chan error, 2)
	go func() {
		for {
			mt, msg, err := upstreamConn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if err = clientConn.WriteMessage(mt, msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() {
		for {
			mt, msg, err := clientConn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if err = upstreamConn.WriteMessage(mt, msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	<-errc
}

// LogbookProxy proxies requests to the Rust logbook service.
// No path rewriting — logbook mounts SwaggerUI at /swagger/logbook so all
// gateway paths match the upstream paths directly.
func LogbookProxy(logbookURL string) gin.HandlerFunc {
	return newProxy(logbookURL)
}

func newProxy(rawURL string, rewritePath ...func(string) string) gin.HandlerFunc {
	target, err := url.Parse(rawURL)
	if err != nil {
		panic("invalid proxy URL: " + rawURL + ": " + err.Error())
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
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
