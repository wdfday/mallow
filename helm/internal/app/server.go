package app

import (
	"net/http"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "mallow/helm/docs"
	handhandler "mallow/helm/internal/module/hand/handler"
	helmhandler "mallow/helm/internal/module/helm/handler"
	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
	pkgtelemetry "mallow/pkg/telemetry"
)

type HealthResponse struct {
	Status  string `json:"status" example:"ok"`
	Service string `json:"service" example:"helm"`
}

// NewServer creates the Gin engine and registers all module routes.
func NewServer(
	helmH *helmhandler.Handler,
	handH *handhandler.Handler,
) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(pkgtelemetry.GinMiddleware("helm"))

	// Orchestrator is an internal service; it trusts X-User-* headers from the gateway.
	// All /api routes require a valid user identity.
	api := r.Group("/api", pkgmw.TrustedHeaders())
	helmH.Register(api)
	handH.Register(api)

	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	r.GET("/metrics", handH.Metrics)

	// Health godoc
	// @Summary Health check
	// @Tags system
	// @Produce json
	// @Success 200 {object} shared.SuccessResponse[HealthResponse]
	// @Router /health [get]
	r.GET("/health", func(c *gin.Context) {
		shared.RespondWithSuccess(c, http.StatusOK, "Service is healthy", HealthResponse{Status: "ok", Service: "helm"})
	})

	return r
}
