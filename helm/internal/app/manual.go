package app

import (
	"context"
	"log/slog"
	"mallow/helm/internal/infra/journal/eventlog"
	"mallow/helm/internal/infra/journal/tradelog"
	brokerservice "mallow/helm/internal/module/broker/service"
	handdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/safe"
	"time"

	"go.uber.org/fx"
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
