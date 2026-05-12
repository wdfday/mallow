package router

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.uber.org/fx"

	_ "mallow/investment/docs"
	"mallow/investment/internal/config"
	"mallow/investment/internal/middleware"
	accountHandler "mallow/investment/internal/module/account/handler"
	brokerHandler "mallow/investment/internal/module/broker/handler"
	cashHandler "mallow/investment/internal/module/cash_flow/handler"
	derivHandler "mallow/investment/internal/module/derivative/handler"
	"mallow/investment/internal/module/portfolio/replay"
	positionHandler "mallow/investment/internal/module/position/handler"
	snapshotHandler "mallow/investment/internal/module/snapshot/handler"
	txHandler "mallow/investment/internal/module/transaction/handler"
	pkgmw "mallow/pkg/middleware"
	pkgtelemetry "mallow/pkg/telemetry"
)

var Module = fx.Module("router", fx.Invoke(Register))

type Params struct {
	fx.In
	Engine       *gin.Engine
	Config       *config.Config
	AuthMiddle   *middleware.Middleware
	Positions    *positionHandler.Handler
	Transactions *txHandler.Handler
	Derivatives  *derivHandler.Handler
	CashFlows    *cashHandler.Handler
	Snapshots    *snapshotHandler.Handler
	Replay       *replay.Handler
	Broker       *brokerHandler.BrokerConnectionHandler
	Account      *accountHandler.Handler
}

func Register(p Params) {
	engine := p.Engine
	engine.Use(pkgtelemetry.GinMiddleware("investment"))
	engine.Use(corsMiddleware(p.Config.CORS.AllowedOrigins))

	engine.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "investment"})
	})
	engine.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	v1 := engine.Group("/api/v1/investment", pkgmw.TrustedHeaders())
	{
		v1.GET("/positions", p.Positions.List)
		v1.GET("/positions/:symbol", p.Positions.Get)

		v1.GET("/transactions", p.Transactions.List)
		v1.POST("/transactions", p.Transactions.Create)

		v1.GET("/derivatives", p.Derivatives.List)

		v1.GET("/cash-flows", p.CashFlows.List)

		v1.GET("/snapshots", p.Snapshots.List)
		v1.POST("/snapshots", p.Snapshots.Trigger)

		// Admin ops
		v1.POST("/admin/rebuild", p.Replay.Rebuild)
	}

	// Broker connections and accounts use their own route groups (RegisterRoutes).
	// jwtMiddleware already sets user_id; AuthMiddle is a passthrough guard.
	p.Broker.RegisterRoutes(engine, p.AuthMiddle)
	p.Account.RegisterRoutes(engine, p.AuthMiddle)
}

func corsMiddleware(origins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		for _, o := range origins {
			if o == "*" || strings.EqualFold(o, origin) {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
				c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,X-User-ID,X-User-Role,X-User-Email")
				break
			}
		}
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
