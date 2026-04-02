package app

import (
	"net/http"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	pkgmw "mallow/pkg/middleware"
	pkgtelemetry "mallow/pkg/telemetry"
	_ "orchestrator/docs"
	orchhandler "orchestrator/internal/module/orchesrator/handler"
	bothandler "orchestrator/internal/module/worker/handler"
	"orchestrator/internal/shared"
)

type HealthResponse struct {
	Status  string `json:"status" example:"ok"`
	Service string `json:"service" example:"orchestrator"`
}

// NewServer creates the Gin engine and registers all module routes.
func NewServer(
	orchH *orchhandler.Handler,
	botH *bothandler.Handler,
) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(pkgtelemetry.GinMiddleware("orchestrator"))

	// Orchestrator is an internal service; it trusts X-User-* headers from the gateway.
	// All /api routes require a valid user identity.
	api := r.Group("/api", pkgmw.TrustedHeaders())
	orchH.Register(api)
	botH.Register(api)

	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	r.GET("/metrics", botH.Metrics)

	// Health godoc
	// @Summary Health check
	// @Tags system
	// @Produce json
	// @Success 200 {object} shared.SuccessResponse[HealthResponse]
	// @Router /health [get]
	r.GET("/health", func(c *gin.Context) {
		shared.RespondWithSuccess(c, http.StatusOK, "Service is healthy", HealthResponse{Status: "ok", Service: "orchestrator"})
	})

	return r
}
