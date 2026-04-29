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
	"gateway/internal/middleware"
	"gateway/internal/service"
	pkgtelemetry "mallow/pkg/telemetry"

	"github.com/gin-gonic/gin"
)

func buildRouter(cfg config.Config, h *handler.Handler, rdb *redis.Client, identityClient *service.IdentityClient) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(pkgtelemetry.GinMiddleware("gateway"))
	r.Use(middleware.CORS())
	r.Use(middleware.RateLimiter(200)) // 200 req/min per IP

	// ── Proxies ─────────────────────────────────────────────────────────
	identityProxy := handler.IdentityProxy(cfg.IdentityURL)
	investmentProxy := handler.InvestmentProxy(cfg.InvestmentURL)
	orchestratorProxy := handler.OrchestratorProxy(cfg.OrchestratorURL)
	strategistProxy := handler.StrategistProxy(cfg.StrategistURL)
	logbookProxy := handler.LogbookProxy(cfg.LogbookURL)

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
	r.GET("/swagger", h.SwaggerIndex)
	r.GET("/docs", h.SwaggerIndex)
	r.GET("/swagger/gateway/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// Logbook swagger UI — public (utoipa HTML + assets at /swagger/logbook/*, spec at /api-doc/openapi.json)
	r.GET("/swagger/logbook", logbookProxy)
	r.GET("/swagger/logbook/*any", logbookProxy)
	r.GET("/api-doc/openapi.json", logbookProxy)

	// Identity: public auth + swagger routes (no JWT required)
	r.Any("/api/v1/auth/*path", identityProxy)
	r.Any("/api/v1/swagger/*path", identityProxy)

	// Service swagger UIs — public, kept outside /api/v1/* to avoid Gin catch-all conflicts
	r.Any("/swagger/investment/*path", investmentProxy)
	r.Any("/swagger/orchestrator/*path", orchestratorProxy)

	// ── Protected routes ─────────────────────────────────────────────────
	r.Any("/api/v1/investment/*path", jwtAuth, injectHeaders, investmentProxy)
	r.Any("/api/v1/orchestrator/*path", jwtAuth, injectHeaders, orchestratorProxy)
	r.Any("/api/v1/strategist/*path", jwtAuth, injectHeaders, strategistProxy)

	// Orchestrator swagger emits /api/* paths — keep protected aliases for docs-driven testing
	r.Any("/api/orchestrators", jwtAuth, injectHeaders, orchestratorProxy)
	r.Any("/api/orchestrators/*path", jwtAuth, injectHeaders, orchestratorProxy)
	r.Any("/api/bots", jwtAuth, injectHeaders, orchestratorProxy)
	r.Any("/api/bots/*path", jwtAuth, injectHeaders, orchestratorProxy)
	r.Any("/api/signal-engine/*path", jwtAuth, injectHeaders, orchestratorProxy)

	// Gateway-native protected endpoints
	api := r.Group("/api", jwtAuth, injectHeaders)
	{
		api.GET("/strategies", h.ListStrategies)
		// Backtest + indicator: fully proxied to logbook (no gateway logic)
		api.POST("/backtest", logbookProxy)
		api.POST("/indicator", logbookProxy)
		api.POST("/chat", h.Chat)
		api.POST("/chat/stream", h.ChatStream)

		// Logbook data endpoints
		api.GET("/symbols", logbookProxy)
		api.GET("/data/:symbol", logbookProxy)
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
