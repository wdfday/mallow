package server

import (
	"context"
	"log/slog"
	"mallow/identity/internal/config"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
)

// Module provides the HTTP server to fx.
var Module = fx.Module("server", fx.Provide(New), fx.Invoke(registerLifecycle))

// New creates a configured Gin engine.
func New(cfg *config.Config) *gin.Engine {
	gin.SetMode(cfg.Server.GinMode)
	return gin.New()
}

func registerLifecycle(lc fx.Lifecycle, engine *gin.Engine, cfg *config.Config, logger *slog.Logger) {
	srv := &http.Server{
		Addr:    ":" + cfg.Server.Port,
		Handler: engine,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go func() {
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					logger.Error("HTTP server error", "error", err)
				}
			}()
			logger.Info("Identity service ready", "addr", srv.Addr)
			logger.Info("Swagger UI available", "url", "http://localhost"+srv.Addr+"/swagger/index.html")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("Shutting down HTTP server")
			return srv.Shutdown(ctx)
		},
	})
}
