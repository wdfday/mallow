package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"
	"go.uber.org/fx"

	"orchestrator/internal/config"
	"orchestrator/internal/infra/engine"
	"orchestrator/internal/infra/marketdata"
	mdbinance "orchestrator/internal/infra/marketdata/binance"
	mdbybit "orchestrator/internal/infra/marketdata/bybit"
	mdokx "orchestrator/internal/infra/marketdata/okx"
	orchhandler "orchestrator/internal/module/orchesrator/handler"
	bothandler "orchestrator/internal/module/bot/handler"
	"orchestrator/internal/runtime"
	"orchestrator/internal/runtime/core/tick"
)

// startNATSAPI subscribes per-module NATS request/reply handlers on start, drains on stop.
func startNATSAPI(
	lc fx.Lifecycle,
	nc *nats.Conn,
	orchNATS *orchhandler.NATSHandler,
	botNATS *bothandler.NATSHandler,
) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := orchNATS.Subscribe(nc); err != nil {
				return err
			}
			return botNATS.Subscribe(nc)
		},
		OnStop: func(ctx context.Context) error {
			orchNATS.Drain()
			botNATS.Drain()
			return nil
		},
	})
}

// subscribeSignals wires NATS signal subscription → runtime SignalDispatcher.
func subscribeSignals(lc fx.Lifecycle, sc *engine.SignalClient, dispatcher *runtime.SignalDispatcher) {
	var sub interface{ Drain() error }
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			s, err := sc.SubscribeSignals(func(resp *engine.SignalResponse) {
				dispatcher.Dispatch(resp)
			})
			if err != nil {
				return err
			}
			sub = s
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if sub != nil {
				return sub.Drain()
			}
			return nil
		},
	})
}

// runOrchestrator starts market data listener, equity recorder, API server, tick router,
// and account fill streaming for exchanges that support it.
func runOrchestrator(
	lc fx.Lifecycle,
	cfg *config.Config,
	router *tick.Router,
	ginEngine *gin.Engine,
	reg *runtime.Registry,
	nc *nats.Conn,
) {
	srv := &http.Server{Addr: cfg.Server.APIAddr, Handler: ginEngine}
	var cancel context.CancelFunc

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			runCtx, c := context.WithCancel(context.Background())
			cancel = c

			reg.SetRuntime(runCtx, nc)
			reg.StartFillStreaming(runCtx, nc)
			reg.StartPollingSync(runCtx, nc, cfg.Runtime.SyncInterval)

			if cfg.MarketData.Source != "none" && cfg.MarketData.Source != "" {
				listener, err := buildMarketDataListener(cfg)
				if err != nil {
					slog.Error("market data listener init failed", "err", err)
				} else {
					go func() {
						slog.Info("market data listener starting", "source", listener.Name(), "symbols", cfg.MarketData.Symbols)
						if err := listener.Subscribe(runCtx, cfg.MarketData.Symbols, func(symbol string, price decimal.Decimal) {
							for _, rt := range reg.All() {
								rt.UpdatePrice(symbol, price)
							}
						}); err != nil {
							slog.Error("market data listener error", "err", err)
						}
					}()
				}
			}

			go recordEquity(runCtx, reg, cfg.Runtime.BarInterval)
			go heartbeat(runCtx, reg, 30*time.Second)

			go func() {
				slog.Info("API server starting", "addr", cfg.Server.APIAddr)
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					slog.Error("API server error", "err", err)
				}
			}()

			go func() {
				slog.Info("orchestrator running", "bar_interval", cfg.Runtime.BarInterval, "api_addr", cfg.Server.APIAddr)
				if err := router.Run(runCtx); err != nil {
					slog.Error("tick router stopped", "err", err)
				}
			}()

			return nil
		},
		OnStop: func(ctx context.Context) error {
			if cancel != nil {
				cancel()
			}
			return srv.Shutdown(ctx)
		},
	})
}

func heartbeat(ctx context.Context, reg *runtime.Registry, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rts := reg.All()
			if len(rts) == 0 {
				slog.Info("heartbeat: no active runtimes")
				continue
			}
			for _, rt := range rts {
				slog.Info("heartbeat",
					"orchestrator_id", rt.OrchestratorID,
					"account_id", rt.AccountID,
					"broker", rt.BrokerType,
					"bots", len(rt.BotIDs()),
					"running_bots", len(rt.RunningBotIDs()),
					"equity", rt.Portfolio.Equity(),
					"paused", rt.IsPaused(),
					"last_sync_at", rt.LastSyncAt(),
				)
			}
		case <-ctx.Done():
			return
		}
	}
}

func recordEquity(ctx context.Context, reg *runtime.Registry, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now().UTC()
			for _, rt := range reg.All() {
				rt.Portfolio.RecordEquity(now)
			}
		case <-ctx.Done():
			return
		}
	}
}

func buildMarketDataListener(cfg *config.Config) (marketdata.Listener, error) {
	switch cfg.MarketData.Source {
	case "okx":
		return mdokx.New(), nil
	case "binance":
		return mdbinance.New(), nil
	case "bybit":
		return mdbybit.New(), nil
	case "alpaca", "ibkr", "oanda":
		return nil, fmt.Errorf(
			"market data source %q now requires per-account provider credentials from orchestrator config, not global env config",
			cfg.MarketData.Source,
		)
	default:
		return nil, fmt.Errorf("unknown market data source: %q", cfg.MarketData.Source)
	}
}
