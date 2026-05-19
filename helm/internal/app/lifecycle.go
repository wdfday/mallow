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

	"mallow/helm/internal/config"
	"mallow/helm/internal/infra/engine"
	"mallow/helm/internal/infra/marketdata"
	mdbinance "mallow/helm/internal/infra/marketdata/binance"
	mdbybit "mallow/helm/internal/infra/marketdata/bybit"
	mdokx "mallow/helm/internal/infra/marketdata/okx"
	handhandler "mallow/helm/internal/module/hand/handler"
	handservice "mallow/helm/internal/module/hand/service"
	orchhandler "mallow/helm/internal/module/helm/handler"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/perf"
)

// startNATSAPI subscribes per-module NATS request/reply handlers on start, drains on stop.
func startNATSAPI(
	lc fx.Lifecycle,
	nc *nats.Conn,
	orchNATS *orchhandler.NATSHandler,
	handNATS *handhandler.NATSHandler,
) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := orchNATS.Subscribe(nc); err != nil {
				return err
			}
			return handNATS.Subscribe(nc)
		},
		OnStop: func(ctx context.Context) error {
			orchNATS.Drain()
			handNATS.Drain()
			return nil
		},
	})
}

// subscribeSignals wires NATS signal subscription → runtime SignalDispatcher.
func subscribeSignals(lc fx.Lifecycle, sc *engine.SignalClient, dispatcher *runtime.SignalDispatcher) {
	var sub interface{ Drain() error }
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			s, err := sc.SubscribeSignals(func(resp *engine.SignalResponse, receivedAt time.Time) {
				dispatcher.Dispatch(resp, receivedAt)
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

// subscribeHeraldReady subscribes to engine.ready and triggers re-registration
// of all running hands when herald restarts (detected by herald_id change).
func subscribeHeraldReady(lc fx.Lifecycle, sc *engine.SignalClient, handMgr *handservice.Service) {
	var (
		sub          interface{ Drain() error }
		lastHeraldID string
	)
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			s, err := sc.SubscribeReady(func(ev *engine.ReadyEvent) {
				if ev.HeraldId == lastHeraldID {
					return
				}
				if lastHeraldID != "" {
					slog.Warn("herald restart detected — re-registering all running hands",
						"old_herald_id", lastHeraldID, "new_herald_id", ev.HeraldId)
				}
				lastHeraldID = ev.HeraldId
				handMgr.ReregisterAll()
			})
			if err != nil {
				// Non-fatal: herald may not be running yet; heartbeat loop is the safety net.
				slog.Warn("engine.ready subscribe failed (non-fatal)", "err", err)
				return nil
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

// startHeartbeatLoop periodically checks that all running hands are registered
// in herald. Runs every 30s; re-registers any that herald reports as missing.
func startHeartbeatLoop(lc fx.Lifecycle, sc *engine.SignalClient, reg *runtime.Registry, handMgr *handservice.Service) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			runCtx, cancel := context.WithCancel(context.Background())
			go func() {
				defer cancel()
				runHeraldHeartbeat(runCtx, sc, reg, handMgr, 30*time.Second)
			}()
			// Store cancel so OnStop can shut it down.
			lc.Append(fx.Hook{OnStop: func(ctx context.Context) error { cancel(); return nil }})
			return nil
		},
	})
}

func runHeraldHeartbeat(ctx context.Context, sc *engine.SignalClient, reg *runtime.Registry, handMgr *handservice.Service, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			doHeraldHeartbeat(ctx, sc, reg, handMgr)
		case <-ctx.Done():
			return
		}
	}
}

func doHeraldHeartbeat(ctx context.Context, sc *engine.SignalClient, reg *runtime.Registry, handMgr *handservice.Service) {
	for _, rt := range reg.All() {
		runningIDs := rt.RunningHandIDs()
		if len(runningIDs) == 0 {
			continue
		}
		handIDs := runningIDs
		resp, err := sc.Heartbeat(ctx, rt.HelmID.String(), handIDs)
		if err != nil {
			slog.Warn("herald heartbeat failed", "helm_id", rt.HelmID, "err", err)
			continue
		}
		if len(resp.Missing) == 0 {
			continue
		}
		slog.Warn("herald heartbeat: missing hands — re-registering",
			"helm_id", rt.HelmID, "missing", resp.Missing)
		handMgr.ReregisterByIDs(resp.Missing)
	}
}

// runOrchestrator starts market data listener, API server, and account fill
// streaming for exchanges that support it.
func runOrchestrator(
	lc fx.Lifecycle,
	cfg *config.Config,
	ginEngine *gin.Engine,
	reg *runtime.Registry,
	nc *nats.Conn,
	portfolioLog perf.PortfolioLog,
) {
	srv := &http.Server{Addr: cfg.Server.APIAddr, Handler: ginEngine}
	var cancel context.CancelFunc

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			runCtx, c := context.WithCancel(context.Background())
			cancel = c

			reg.SetRuntime(runCtx, nc)
			reg.SetPortfolioLog(portfolioLog)
			reg.ReconcileAllOrders(ctx)
			reg.RecoverGapFills(ctx, nc)
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

			go heartbeat(runCtx, reg, 30*time.Second)

			go func() {
				slog.Info("API server starting", "addr", cfg.Server.APIAddr)
				if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					slog.Error("API server error", "err", err)
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
				pf := rt.Portfolio
				slog.Info("heartbeat",
					"helm_id", rt.HelmID,
					"account_id", rt.AccountID,
					"broker", rt.BrokerType,
					"paused", rt.IsPaused(),
					"last_sync_at", rt.LastSyncAt(),
					// portfolio
					"cash", pf.Cash(),
					"equity", pf.Equity(),
					"total_return_pct", fmt.Sprintf("%.2f%%", pf.TotalReturn()*100),
					"drawdown_pct", fmt.Sprintf("%.2f%%", pf.CurrentDrawdown()*100),
					"max_drawdown_pct", fmt.Sprintf("%.2f%%", pf.MaxDrawdown()*100),
					"daily_pnl", pf.DailyPnL(),
					// hands
					"hands", len(rt.HandIDs()),
					"running_hands", len(rt.RunningHandIDs()),
				)

				// positions
				for _, pos := range pf.Positions() {
					side := "long"
					if pos.Qty.IsNegative() {
						side = "short"
					}
					unrealized := pos.CurrentPrice.Sub(pos.AvgPrice).Mul(pos.Qty)
					slog.Info("heartbeat: position",
						"helm_id", rt.HelmID,
						"symbol", pos.Symbol,
						"side", side,
						"qty", pos.Qty,
						"avg_price", pos.AvgPrice,
						"current_price", pos.CurrentPrice,
						"unrealized_pnl", unrealized,
					)
				}

				// per-hand metrics
				for _, h := range rt.HandSummaries() {
					m := h.Metrics
					slog.Info("heartbeat: hand",
						"helm_id", rt.HelmID,
						"hand_id", h.ID,
						"symbol", h.Symbol,
						"status", h.Status,
						"signals_received", m.SignalsReceived,
						"signals_filtered", m.SignalsFiltered,
						"trades_approved", m.TradesApproved,
						"orders_placed", m.OrdersPlaced,
						"orders_filled", m.OrdersFilled,
						"orders_failed", m.OrdersFailed,
						"total_pnl", m.TotalPnL,
						"win", m.WinCount,
						"loss", m.LossCount,
					)
				}
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
			"market data source %q now requires per-account provider credentials from helm config, not global env config",
			cfg.MarketData.Source,
		)
	default:
		return nil, fmt.Errorf("unknown market data source: %q", cfg.MarketData.Source)
	}
}
