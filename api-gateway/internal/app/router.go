package app

import (
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "gateway/docs" // generated swagger docs
	"gateway/internal/config"
	"gateway/internal/handler"
	"gateway/internal/metrics"
	"gateway/internal/middleware"
	"gateway/internal/service"
	"gateway/internal/ws"
	pkgtelemetry "mallow/pkg/telemetry"

	"github.com/gin-gonic/gin"
)

func buildRouter(cfg config.Config, h *handler.Handler, rdb *redis.Client, identityClient *service.IdentityClient, hub *ws.Hub) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(pkgtelemetry.GinMiddleware("gateway"))
	r.Use(metrics.Middleware())             // record per-request metrics for /metrics
	r.Use(middleware.StripTrustedHeaders()) // must be first — strips X-User-* before any proxy sees them
	r.Use(middleware.CORS(cfg.CORSOrigins))
	r.Use(middleware.RateLimiter(rdb, cfg.RateLimitPerMinute))

	// ── Proxies ─────────────────────────────────────────────────────────
	identityProxy := handler.IdentityProxy(cfg.IdentityURL)
	helmProxy := handler.HelmProxy(cfg.HelmURL)
	heraldProxy := handler.HeraldProxy(cfg.HeraldURL)

	// ── JWT middleware ───────────────────────────────────────────────────
	cacheTTL, err := time.ParseDuration(cfg.JWKSCacheTTL)
	if err != nil {
		cacheTTL = 5 * time.Minute
	}
	jwksURL := cfg.JWTJWKSURL
	if jwksURL == "" {
		jwksURL = cfg.IdentityURL + "/.well-known/jwks.json"
	}
	jwtAuth := middleware.JWTAuth(middleware.JWTAuthConfig{
		Secret:       cfg.JWTSecret,
		PublicKeyPEM: cfg.JWTPublicKey,
		JWKSURL:      jwksURL,
		Issuer:       cfg.JWTIssuer,
		CacheTTL:     cacheTTL,
	}, rdb, identityClient)
	injectHeaders := middleware.InjectUserHeaders()

	// ── Public routes ────────────────────────────────────────────────────
	r.GET("/health", h.Health)
	r.GET("/metrics", metrics.Handler) // Prometheus exposition
	r.GET("/swagger", h.SwaggerIndex)
	r.GET("/docs", h.SwaggerIndex)
	r.GET("/swagger/gateway/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// Herald swagger UI — public (utoipa HTML + assets at /swagger/herald/*, spec at /api-doc/openapi.json)
	r.GET("/swagger/herald", heraldProxy)
	r.GET("/swagger/herald/*any", heraldProxy)
	r.GET("/api-doc/openapi.json", heraldProxy)
	r.GET("/api-doc/openapi.yaml", heraldProxy)

	// Identity: public auth + swagger routes (no JWT required)
	r.Any("/api/v1/auth/*path", identityProxy)
	r.Any("/api/v1/swagger/*path", identityProxy)

	// Service swagger UI — public
	r.Any("/swagger/helm/*path", helmProxy)

	// ── Protected routes ─────────────────────────────────────────────────
	r.Any("/api/v1/helms", jwtAuth, injectHeaders, helmProxy)
	r.Any("/api/v1/helms/*path", jwtAuth, injectHeaders, helmProxy)
	r.Any("/api/v1/hands", jwtAuth, injectHeaders, helmProxy)
	r.Any("/api/v1/hands/*path", jwtAuth, injectHeaders, helmProxy)
	r.Any("/api/v1/accounts", jwtAuth, injectHeaders, helmProxy)
	r.Any("/api/v1/accounts/*path", jwtAuth, injectHeaders, helmProxy)
	r.Any("/api/v1/broker-connections", jwtAuth, injectHeaders, helmProxy)
	r.Any("/api/v1/broker-connections/*path", jwtAuth, injectHeaders, helmProxy)

	// Realtime WebSocket fan-out (bars + scoped account events). The bearer token
	// arrives via Sec-WebSocket-Protocol (browsers can't set Authorization on WS),
	// so WSBearerFromProtocol copies it to Authorization before jwtAuth validates.
	// No injectHeaders — the hub reads user_id straight from the gin context.
	r.GET("/api/v1/stream", middleware.WSBearerFromProtocol(), jwtAuth, hub.HandleStream)

	// Protected endpoints
	api := r.Group("/api/v1", jwtAuth, injectHeaders)
	{
		// Herald — data
		api.GET("/symbols", heraldProxy)
		api.GET("/indicators", heraldProxy)
		api.POST("/data/:source/:symbol", heraldProxy)

		// Herald — backtest
		api.GET("/strategies", heraldProxy)
		api.POST("/backtest", heraldProxy)
		api.POST("/backtest/estimate", heraldProxy)
		api.POST("/backtest/script", heraldProxy)
		api.POST("/script/validate", heraldProxy)

		// Herald — strategy
		api.Any("/strategy/*path", heraldProxy)
	}

	// Identity catch-all: /api/v1/* not matched above — Gin cannot mix catch-all
	// with sibling concrete routes, so we dispatch via NoRoute.
	r.NoRoute(func(c *gin.Context) {
		if !strings.HasPrefix(c.Request.URL.Path, "/api/v1/") {
			c.AbortWithStatus(404)
			return
		}
		jwtAuth(c)
		if c.IsAborted() {
			return
		}
		injectHeaders(c)
		if c.IsAborted() {
			return
		}
		identityProxy(c)
	})

	return r
}
