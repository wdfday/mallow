package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"

	"mallow/investment/internal/config"
)

var Module = fx.Module("server", fx.Provide(New), fx.Invoke(registerLifecycle))

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
			logger.Info("Investment service ready", "addr", srv.Addr)
			logger.Info("Swagger UI available", "url", "http://localhost"+srv.Addr+"/swagger/index.html")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("Shutting down investment HTTP server")
			return srv.Shutdown(ctx)
		},
	})
}
