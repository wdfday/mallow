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

	"google.golang.org/protobuf/proto"

	"mallow/helm/internal/config"
	"mallow/helm/internal/infra/engine"
	"mallow/helm/internal/infra/marketdata"
	mdbinance "mallow/helm/internal/infra/marketdata/binance"
	mdbybit "mallow/helm/internal/infra/marketdata/bybit"
	mdokx "mallow/helm/internal/infra/marketdata/okx"
	"mallow/helm/internal/infra/poslog"
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

// subscribeSignals wires NATS signal subscription → runtime SignalDispatcher
// and registers the dispatcher with the Registry for metrics export.
func subscribeSignals(lc fx.Lifecycle, sc *engine.SignalClient, dispatcher *runtime.SignalDispatcher, reg *runtime.Registry) {
	reg.SetDispatcher(dispatcher)
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
	resp, err := sc.ListHands(ctx)
	if err != nil {
		slog.Warn("herald heartbeat: failed to list hands from herald", "err", err)
		return
	}

	heraldHands := make(map[string]bool)
	if resp != nil {
		for _, h := range resp.Hands {
			heraldHands[h.HandId] = true
		}
	}

	runningHands := handMgr.RunningHands()
	expectedRunning := make(map[string]bool, len(runningHands))

	var missing []string
	for _, hRef := range runningHands {
		idStr := hRef.Data.ID.String()
		expectedRunning[idStr] = true
		if !heraldHands[idStr] {
			missing = append(missing, idStr)
		}
	}

	var orphans []string
	if resp != nil {
		for _, h := range resp.Hands {
			// Skip fallback/default hands which have empty HelmId
			if h.HelmId != "" && !expectedRunning[h.HandId] {
				orphans = append(orphans, h.HandId)
			}
		}
	}

	if len(missing) > 0 {
		slog.Warn("herald heartbeat: missing hands detected — re-registering", "missing", missing)
		handMgr.ReregisterByIDs(missing)
	}
	if len(orphans) > 0 {
		slog.Warn("herald heartbeat: orphan hands detected — deregistering", "orphans", orphans)
		handMgr.DeregisterByIDs(orphans)
	}

	slog.Info("herald heartbeat: sync cycle completed",
		"running_hands", len(runningHands),
		"herald_hands", len(heraldHands),
		"re_registered", len(missing),
		"deregistered", len(orphans),
	)
}

// runOrchestrator starts market data listener, API server, and account fill
// streaming for exchanges that support it.
// Startup sequence (order is critical):
//  1. SetRuntime / SetSnapshotLog  — wire NATS context into registry
//  2. ReconcileAll                 — restore hand positions from poslog WAL vs exchange
//  3. ReconcileAllOrders           — re-track open orders in orderHandMap
//  4. RecoverGapFills              — apply fills missed in [lastSyncAt/createdAt, now)
//  5. StartFillStreaming           — start WS fill listener (hands now ready)
//  6. StartPollingSync             — periodic REST sync fallback
//
// subscribeSignals (step 7) is registered AFTER runOrchestrator in fx.go so NATS
// signal delivery only starts after all hand state is fully restored.
func runOrchestrator(
	lc fx.Lifecycle,
	cfg *config.Config,
	ginEngine *gin.Engine,
	reg *runtime.Registry,
	nc *nats.Conn,
	posLog poslog.Log,
	snapshotLog perf.SnapshotLog,
	handSvc *handservice.Service,
) {
	srv := &http.Server{Addr: cfg.Server.APIAddr, Handler: ginEngine}
	var cancel context.CancelFunc

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			runCtx, c := context.WithCancel(context.Background())
			cancel = c

			reg.SetRuntime(runCtx, nc)
			reg.SetSnapshotLog(snapshotLog)

			// Step 2: Reconcile hand positions from poslog WAL vs exchange.
			// Runs synchronously so every hand is fully restored before signals arrive.
			if posLog != nil {
				reconciler := runtime.NewReconciler(posLog)
				for _, rt := range reg.All() {
					results := reconciler.Reconcile(ctx, rt)
					for _, res := range results {
						if res.Err != nil {
							slog.Error("startup reconcile failed",
								"helm_id", rt.HelmID, "hand_id", res.HandID, "err", res.Err)
						} else {
							slog.Info("startup reconcile",
								"helm_id", rt.HelmID, "hand_id", res.HandID,
								"action", res.Action, "phase", res.Phase)
						}
					}
				}
			}

			// Step 2.5: Start all hydrated hands now that position state is reconciled.
			handSvc.StartAllHydrated()

			// Step 2.6: Emit a baseline snapshot per helm + per hand so the FE
			// equity curve has a fresh datapoint right after restart instead of
			// staying frozen at the last pre-shutdown record until the next fill.
			for _, rt := range reg.All() {
				rt.EmitBaselineSnapshots(ctx)
			}

			// Steps 3–6.
			reg.ReconcileAllOrders(ctx)
			reg.RecoverGapFills(ctx, nc)
			reg.StartFillStreaming(runCtx, nc)
			reg.StartPollingSync(runCtx, nc, cfg.Runtime.SyncInterval)

			// Subscribe to herald bar closes (bars.* NATS subject) to keep
			// the per-helm price cache warm. This is the primary price source
			// when no dedicated market-data listener is configured.
			// Each bar carries the close price of the just-confirmed candle;
			// the symbol already has the exchange prefix (e.g. "binance:ETHUSDT").
			if _, err := nc.Subscribe(engine.SubjBarsPrefix+"*", func(msg *nats.Msg) {
				var bar engine.BarMsg
				if err := proto.Unmarshal(msg.Data, &bar); err != nil {
					return
				}
				if bar.C <= 0 {
					return
				}
				price := decimal.NewFromFloat(bar.C)
				symbol := bar.S
				for _, rt := range reg.All() {
					rt.UpdatePrice(symbol, price)
				}
			}); err != nil {
				slog.Warn("bars price feed subscribe failed", "err", err)
			} else {
				slog.Info("bars price feed subscribed", "subject", engine.SubjBarsPrefix+"*")
			}

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
