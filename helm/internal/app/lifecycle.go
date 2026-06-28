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
	"mallow/helm/internal/infra/eventlog"
	"mallow/helm/internal/infra/herald"
	"mallow/helm/internal/infra/marketdata"
	mdbinance "mallow/helm/internal/infra/marketdata/binance"
	mdbybit "mallow/helm/internal/infra/marketdata/bybit"
	mdokx "mallow/helm/internal/infra/marketdata/okx"
	"mallow/helm/internal/infra/poslog"
	"mallow/helm/internal/infra/tradelog"
	brokerservice "mallow/helm/internal/module/broker/service"
	handdomain "mallow/helm/internal/module/hand/domain"
	handhandler "mallow/helm/internal/module/hand/handler"
	handservice "mallow/helm/internal/module/hand/service"
	orchhandler "mallow/helm/internal/module/helm/handler"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/safe"
)

// syncBrokerAccounts runs a one-shot account-discovery pass on startup.
// For every active broker connection that supports MultiAccountDetector (e.g. Binance),
// it calls DetectAccounts and ensures each sub-account has a matching Account + Helm row.
// Runs in a goroutine — startup is not blocked. Comment out after running once.
func syncBrokerAccounts(lc fx.Lifecycle, brokerSvc brokerservice.BrokerConnectionService) {
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				defer safe.Recover()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				if err := brokerSvc.SyncAllAccounts(ctx); err != nil {
					slog.Warn("startup broker account sync failed", "err", err)
				}
			}()
			return nil
		},
	})
}

// backfillTerminalMetrics is a ONE-SHOT backfill: it recomputes FinalMetrics for
// every killed/released hand from the persisted event + trade logs and rewrites it
// to the DB. Terminal hands snapshot their metrics at kill/release time, so hands
// that went terminal before activity counters were event-sourced have zeroed
// counters — this repairs them.
//
// Usage: uncomment the fx.Invoke(backfillTerminalMetrics) in fx.go, build + run once,
// then comment it back out. Live (non-terminal) hands rebuild counters automatically
// via RestoreCounters on every startup and need no backfill.
func backfillTerminalMetrics(lc fx.Lifecycle, repo handdomain.HandRepo, ec eventlog.Log, pnl tradelog.Log) {
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				defer safe.Recover()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				updated := 0
				for _, d := range repo.All() {
					if !d.Status.IsTerminal() {
						continue
					}
					counts, err := ec.CountHandEvents(ctx, d.ID)
					if err != nil {
						slog.Warn("backfill: count events failed", "hand_id", d.ID, "err", err)
						continue
					}
					mv := metricsViewFromCounts(counts)
					mv.TotalPnL, mv.TotalCommission, mv.WinCount, mv.LossCount, _ = pnl.SumHandPnL(ctx, d.ID)
					id := d.ID
					if err := repo.Update(id, func(h *handdomain.Hand) error {
						h.FinalMetrics = &mv
						return nil
					}); err != nil {
						slog.Warn("backfill: persist FinalMetrics failed", "hand_id", id, "err", err)
						continue
					}
					updated++
				}
				slog.Info("backfill terminal metrics: done", "hands_updated", updated)
			}()
			return nil
		},
	})
}

// metricsViewFromCounts maps event-code counts → the activity counters of a
// HandMetricsView (PnL fields filled separately). Mirrors Hand.RestoreCounters.
func metricsViewFromCounts(counts map[int]int64) handdomain.HandMetricsView {
	var filtered int64
	for _, c := range []int{
		runtime.CodeSignalStale, runtime.CodeSignalHelmPaused, runtime.CodeSignalRateLimited,
		runtime.CodeSignalDoNothing, runtime.CodeSignalMaxUnits, runtime.CodeSignalRejected,
		runtime.CodeSignalNoPosition,
	} {
		filtered += counts[c]
	}
	return handdomain.HandMetricsView{
		SignalsReceived: counts[runtime.CodeSignalReceived],
		SignalsFiltered: filtered,
		SignalsDropped:  counts[runtime.CodeSignalDropped],
		TradesApproved:  counts[runtime.CodeTradeApproved],
		OrdersPlaced:    counts[runtime.CodeOrderPlaced],
		OrdersFilled:    counts[runtime.CodeOrderFilled],
		OrdersFailed:    counts[runtime.CodeOrderFailed],
	}
}

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

// heraldReregisterAll re-registers every running hand with herald.
// Called on herald restart detection.
func heraldReregisterAll(reg *runtime.Registry) {
	count := 0
	for _, rt := range reg.All() {
		for _, id := range rt.RunningHandIDs() {
			if rt.ReregisterHand(context.Background(), id) {
				count++
			}
		}
	}
	if count > 0 {
		slog.Info("herald re-register: all running hands", "count", count)
	}
}

// heraldReregisterByIDs re-registers specific hands by string IDs.
func heraldReregisterByIDs(reg *runtime.Registry, handIDs []string) {
	if len(handIDs) == 0 {
		return
	}
	idSet := make(map[string]bool, len(handIDs))
	for _, id := range handIDs {
		idSet[id] = true
	}
	registered := 0
	for _, rt := range reg.All() {
		for _, id := range rt.RunningHandIDs() {
			if !idSet[id] {
				continue
			}
			if rt.ReregisterHand(context.Background(), id) {
				registered++
			}
		}
	}
	if registered > 0 {
		slog.Info("herald re-register: by IDs", "count", registered)
	}
}

// heraldDeregisterByIDs deregisters orphan hands directly via the herald client.
// Deregister does not need exchange info — no runtime lookup required.
func heraldDeregisterByIDs(hc *herald.Client, handIDs []string) {
	if len(handIDs) == 0 {
		return
	}
	for _, raw := range handIDs {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := hc.DeregisterHand(ctx, raw); err != nil {
			slog.Warn("herald orphan deregister failed", "hand_id", raw, "err", err)
		}
		cancel()
	}
	slog.Info("herald deregister orphans: done", "count", len(handIDs))
}

// subscribeHeraldReady subscribes to engine.ready and triggers re-registration
// of all running hands when herald restarts (detected by herald_id change).
func subscribeHeraldReady(lc fx.Lifecycle, hc *herald.Client, reg *runtime.Registry) {
	var (
		sub          interface{ Drain() error }
		lastHeraldID string
	)
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			s, err := hc.SubscribeReady(func(ev *herald.ReadyEvent) {
				if ev.HeraldId == lastHeraldID {
					return
				}
				if lastHeraldID != "" {
					slog.Warn("herald restart detected — re-registering all running hands",
						"old_herald_id", lastHeraldID, "new_herald_id", ev.HeraldId)
				}
				lastHeraldID = ev.HeraldId
				heraldReregisterAll(reg)
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
func startHeartbeatLoop(lc fx.Lifecycle, hc *herald.Client, reg *runtime.Registry) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			runCtx, cancel := context.WithCancel(context.Background())
			go func() {
				defer safe.Recover()
				defer cancel()
				runHeraldHeartbeat(runCtx, hc, reg, 30*time.Second)
			}()
			// Store cancel so OnStop can shut it down.
			lc.Append(fx.Hook{OnStop: func(ctx context.Context) error { cancel(); return nil }})
			return nil
		},
	})
}

func runHeraldHeartbeat(ctx context.Context, hc *herald.Client, reg *runtime.Registry, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			doHeraldHeartbeat(ctx, hc, reg)
		case <-ctx.Done():
			return
		}
	}
}

func doHeraldHeartbeat(ctx context.Context, hc *herald.Client, reg *runtime.Registry) { //nolint:unparam
	resp, err := hc.ListHands(ctx)
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

	expectedRunning := make(map[string]bool)
	var missing []string
	for _, rt := range reg.All() {
		for _, idStr := range rt.RunningHandIDs() {
			expectedRunning[idStr] = true
			if !heraldHands[idStr] {
				missing = append(missing, idStr)
			}
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
		heraldReregisterByIDs(reg, missing)
	}
	if len(orphans) > 0 {
		slog.Warn("herald heartbeat: orphan hands detected — deregistering", "orphans", orphans)
		heraldDeregisterByIDs(hc, orphans)
	}

	slog.Info("herald heartbeat: sync cycle completed",
		"running_hands", len(expectedRunning),
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
//  5. StartStreaming           — start WS fill listener (hands now ready)
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
	hc *herald.Client,
	posLog poslog.Log,
	handSvc *handservice.Service,
) {
	srv := &http.Server{Addr: cfg.Server.APIAddr, Handler: ginEngine}
	var cancel context.CancelFunc

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			runCtx, c := context.WithCancel(context.Background())
			cancel = c

			reg.SetRuntime(runCtx, nc)
			reg.SetHerald(hc)

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

			// Steps 3–6.
			reg.ReconcileAllOrders(ctx)
			reg.RecoverGapFills(ctx)
			reg.RecoverAllBrackets(ctx) // re-place brackets lost in crash window between KindSLUpdated and KindBracketPlaced
			reg.StartStreaming(runCtx)
			reg.StartPollingSync(runCtx, cfg.Runtime.SyncInterval)

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
						defer safe.Recover()
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
				defer safe.Recover()
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
	defer safe.Recover()
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
