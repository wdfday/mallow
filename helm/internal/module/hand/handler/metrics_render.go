package handler

// Prometheus exposition rendering, extracted from the HTTP handler so the Metrics
// endpoint is a thin adapter and the text-format logic lives on its own. This is a
// hand-rolled renderer (text/plain v0.0.4); migrating to the prometheus client library
// would be the larger follow-up — see the debt notes.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor/eventcode"
	"mallow/helm/internal/infra/exchange"
)

// renderPrometheus builds the full /metrics exposition for all live runtimes.
// runningHands is supplied by the caller (HandService) since it is not registry state.
func renderPrometheus(reg RuntimeRegistry, runningHands int) string {
	runtimes := reg.All()

	var totalEquity, totalCash decimal.Decimal
	var out strings.Builder

	for _, rt := range runtimes {
		s := rt.Portfolio.Summary()
		totalEquity = totalEquity.Add(s.Equity)
		totalCash = totalCash.Add(s.Cash)

		hLabels := map[string]string{
			"helm_id":    rt.HelmID.String(),
			"account_id": rt.AccountID.String(),
		}

		out.WriteString(formatMetric("helm_equity", s.Equity.InexactFloat64(), hLabels))
		out.WriteString(formatMetric("helm_cash", s.Cash.InexactFloat64(), hLabels))
		out.WriteString(formatMetric("helm_drawdown_pct", s.CurrentDD, hLabels))
		out.WriteString(formatMetric("helm_max_drawdown_pct", s.MaxDD, hLabels))
		out.WriteString(formatMetric("helm_daily_pnl", s.DailyPnL.InexactFloat64(), hLabels))
		out.WriteString(formatMetric("helm_win_rate_pct", s.WinRate, hLabels))
		out.WriteString(formatMetric("helm_total_trades", float64(s.TotalTrades), hLabels))
		out.WriteString(formatMetric("helm_open_positions_count", float64(s.OpenPositions), hLabels))

		haltedVal := 0.0
		if rt.RiskMgr.IsHalted() {
			haltedVal = 1.0
		}
		out.WriteString(formatMetric("helm_halted", haltedVal, hLabels))

		// Fill routing breakdown — clid vs exchange-id alias vs orphan.
		// Rollout visibility for the client-order-id migration (CLIENT_ORDER_ID.md).
		clidN, aliasN, orphanN := rt.FillRouteCounts()
		for route, n := range map[string]int64{"clid": clidN, "alias": aliasN, "orphan": orphanN} {
			rLabels := map[string]string{
				"helm_id":    rt.HelmID.String(),
				"account_id": rt.AccountID.String(),
				"route":      route,
			}
			out.WriteString(formatMetric("helm_fill_route_total", float64(n), rLabels))
		}

		// Position-level metrics
		for _, pos := range s.Positions {
			pLabels := map[string]string{
				"helm_id":    rt.HelmID.String(),
				"account_id": rt.AccountID.String(),
				"symbol":     pos.Symbol,
			}
			out.WriteString(formatMetric("helm_position_qty", pos.Qty.InexactFloat64(), pLabels))
			out.WriteString(formatMetric("helm_position_unrealized_pnl", pos.UnrealizedPnL.InexactFloat64(), pLabels))
			out.WriteString(formatMetric("helm_position_market_value", pos.MarketValue.InexactFloat64(), pLabels))
		}

		// Hand-level metrics
		for _, handSummary := range rt.HandSummaries() {
			handLabels := map[string]string{
				"helm_id":    rt.HelmID.String(),
				"account_id": rt.AccountID.String(),
				"hand_id":    handSummary.ID,
				"symbol":     handSummary.Symbol,
			}

			m := handSummary.Metrics
			out.WriteString(formatMetric("helm_hand_signals_received", float64(m.SignalsReceived), handLabels))
			out.WriteString(formatMetric("helm_hand_signals_filtered", float64(m.SignalsFiltered), handLabels))
			out.WriteString(formatMetric("helm_hand_signals_dropped", float64(m.SignalsDropped), handLabels))
			out.WriteString(formatMetric("helm_hand_trades_approved", float64(m.TradesApproved), handLabels))
			out.WriteString(formatMetric("helm_hand_orders_placed", float64(m.OrdersPlaced), handLabels))
			out.WriteString(formatMetric("helm_hand_orders_filled", float64(m.OrdersFilled), handLabels))
			out.WriteString(formatMetric("helm_hand_orders_failed", float64(m.OrdersFailed), handLabels))
			out.WriteString(formatMetric("helm_hand_pnl", m.TotalPnL.InexactFloat64(), handLabels))
			out.WriteString(formatMetric("helm_hand_wins", float64(m.WinCount), handLabels))
			out.WriteString(formatMetric("helm_hand_losses", float64(m.LossCount), handLabels))
			out.WriteString(formatMetric("helm_hand_signal_lag_last_ms", float64(m.LatestSignalLagMs), handLabels))
			out.WriteString(formatMetric("helm_hand_signal_queue_depth", float64(m.SignalQueueDepth), handLabels))

			runningVal := 0.0
			if handSummary.Status != "stopped" && handSummary.Status != "error" {
				runningVal = 1.0
			}
			out.WriteString(formatMetric("helm_hand_running_status", runningVal, handLabels))
		}

		// Exchange latency & error metrics (populated by MeteredExchange wrapper)
		if snap := rt.ExchangeSnapshot(); snap != nil {
			exLabels := map[string]string{
				"helm_id":    rt.HelmID.String(),
				"account_id": rt.AccountID.String(),
				"exchange":   snap.Name,
			}
			out.WriteString(formatMetric("helm_exchange_place_order_calls_total", float64(snap.PlaceOrder.Calls), exLabels))
			out.WriteString(formatMetric("helm_exchange_place_order_errors_total", float64(snap.PlaceOrder.Errors), exLabels))
			out.WriteString(formatMetric("helm_exchange_place_order_latency_avg_ms", snap.PlaceOrder.AvgMs, exLabels))
			out.WriteString(formatMetric("helm_exchange_place_order_latency_max_ms", snap.PlaceOrder.MaxMs, exLabels))
			out.WriteString(formatMetric("helm_exchange_get_order_calls_total", float64(snap.GetOrder.Calls), exLabels))
			out.WriteString(formatMetric("helm_exchange_get_order_errors_total", float64(snap.GetOrder.Errors), exLabels))
			out.WriteString(formatMetric("helm_exchange_get_order_latency_avg_ms", snap.GetOrder.AvgMs, exLabels))
			out.WriteString(formatMetric("helm_exchange_get_order_latency_max_ms", snap.GetOrder.MaxMs, exLabels))
			out.WriteString(formatMetric("helm_exchange_cancel_order_calls_total", float64(snap.CancelOrder.Calls), exLabels))
			out.WriteString(formatMetric("helm_exchange_cancel_order_errors_total", float64(snap.CancelOrder.Errors), exLabels))
			out.WriteString(formatMetric("helm_exchange_cancel_order_latency_avg_ms", snap.CancelOrder.AvgMs, exLabels))
			out.WriteString(formatMetric("helm_exchange_ping_last_ms", snap.PingLastMs, exLabels))

			// Per-class error breakdown — only emit classes with non-zero counts.
			for cl, count := range snap.ErrorsByClass {
				if count == 0 {
					continue
				}
				ecLabels := map[string]string{
					"helm_id":    rt.HelmID.String(),
					"account_id": rt.AccountID.String(),
					"exchange":   snap.Name,
					"class":      exchange.ErrClassName[cl],
				}
				out.WriteString(formatMetric("helm_exchange_api_errors_total", float64(count), ecLabels))
			}
		}

		// Runtime event counters — helm_events_total{helm_id, code, name}
		for code, count := range rt.EventCodeCounts() {
			if count == 0 {
				continue
			}
			name, ok := eventcode.CodeNames[code]
			if !ok {
				name = fmt.Sprintf("code_%d", code)
			}
			evLabels := map[string]string{
				"helm_id": rt.HelmID.String(),
				"code":    fmt.Sprintf("%d", code),
				"name":    name,
			}
			out.WriteString(formatMetric("helm_events_total", float64(count), evLabels))
		}
	}

	// Global / summary metrics
	out.WriteString(formatMetric("helm_total_equity", totalEquity.InexactFloat64(), nil))
	out.WriteString(formatMetric("helm_total_cash", totalCash.InexactFloat64(), nil))
	out.WriteString(formatMetric("helm_running_hands", float64(runningHands), nil))
	out.WriteString(formatMetric("helm_active_runtimes", float64(len(runtimes)), nil))

	// Signal dispatcher & routing metrics
	ds := reg.DispatchStats()
	out.WriteString(formatMetric("helm_dispatch_route_no_helm_total", float64(ds.RouteNoHelm), nil))
	out.WriteString(formatMetric("helm_dispatch_route_no_hand_total", float64(ds.RouteNoHand), nil))

	ns := reg.NATSStats()
	out.WriteString(formatMetric("helm_nats_signals_total", float64(ns.SignalsTotal), nil))
	out.WriteString(formatMetric("helm_nats_signals_dispatched_total", float64(ns.SignalsDispatched), nil))
	out.WriteString(formatMetric("helm_nats_signals_missing_id_total", float64(ns.SignalsMissingID), nil))
	out.WriteString(formatMetric("helm_nats_signals_nil_payload_total", float64(ns.SignalsNilPayload), nil))

	return out.String()
}

// formatMetric renders one Prometheus sample line: `name{k="v",...} value`.
func formatMetric(name string, val float64, labels map[string]string) string {
	if len(labels) == 0 {
		return name + " " + formatFloat(val) + "\n"
	}
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	sort.Strings(parts)
	return fmt.Sprintf("%s{%s} %s\n", name, strings.Join(parts, ","), formatFloat(val))
}

// formatFloat prints integers without a decimal point, others with %g.
func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}
