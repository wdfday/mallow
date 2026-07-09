// Package app wires all components and starts the gateway server.
package app

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"

	"gateway/internal/config"
	"gateway/internal/handler"
	"gateway/internal/service"
	"gateway/internal/ws"
)

// Run wires all components and blocks until ctx is cancelled.
func Run(ctx context.Context) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	//shutdownTracing, err := pkgtelemetry.Setup("gateway")
	//if err != nil {
	//	log.Fatalf("otel setup failed: %v", err)
	//}
	//defer func() {
	//	tctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	//	defer cancel()
	//	_ = shutdownTracing(tctx)
	//}()

	cfg := config.Load()

	// ── NATS ────────────────────────────────────────────────────────────
	nc, err := nats.Connect(cfg.NatsURL,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1), // infinite reconnect — gateway must not stop routing after 10 failures
	)
	if err != nil {
		log.Fatalf("nats connect failed: %v", err)
	}
	defer nc.Drain()
	slog.Info("connected to NATS", "url", cfg.NatsURL)

	// ── Redis ───────────────────────────────────────────────────────────
	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("redis parse URL failed: %v", err)
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()
	slog.Info("redis configured", "url", cfg.RedisURL)

	// ── Handler & Router ────────────────────────────────────────────────
	h := &handler.Handler{
		NC:        nc,
		HeraldURL: cfg.HeraldURL,
	}
	identityClient := service.NewIdentityClient(cfg.IdentityURL, cfg.ServiceSecret)

	// JetStream context for replayable account streams. Non-fatal if unavailable —
	// the hub then serves market data only and logs per connection.
	js, err := nc.JetStream()
	if err != nil {
		slog.Warn("jetstream unavailable, account streaming disabled", "err", err)
	}
	helmClient := service.NewHelmClient(nc)
	hub := ws.NewHub(nc, js, helmClient, cfg.CORSOrigins)

	r := buildRouter(cfg, h, rdb, identityClient, hub)

	// ── HTTP server ─────────────────────────────────────────────────────
	addr := fmt.Sprintf(":%s", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: r}

	go func() {
		slog.Info("gateway listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server failed: %v", err)
		}
	}()

	<-ctx.Done()
	slog.Info("gateway shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
}
