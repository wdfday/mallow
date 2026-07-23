// Package eventcode holds the numeric event codes for the hand/helm activity
// log and NATS HelmEvent stream. It is a leaf package with no dependency on
// either package actor (HelmRuntime) or package signalfollower (Hand) —
// both sides emit these codes via natsapi.HelmEvent.Code, so the vocabulary
// lives here instead of forcing a one-directional import between them.
package eventcode

// Numeric event codes for the hand activity log and NATS HelmEvent stream.
// Ranges:
//   10000–10099  signal lifecycle
//   10100–10199  order lifecycle
//   10200–10299  hand lifecycle
//   10300–10399  helm lifecycle

const (
	// Signal received — logged for every signal before any filtering.
	CodeSignalReceived = 10000

	// Signal filtered codes — signal was received but not acted on.
	CodeSignalStale      = 10001 // arrived more than signalMaxAge after dispatch
	CodeSignalHelmPaused = 10002 // helm-level cascade pause active
	// 10003 reserved (was CodeSignalHandPaused — hand-level pause removed)
	CodeSignalRateLimited = 10004 // exceeded per-second order rate limit
	CodeSignalDoNothing   = 10005 // strategy evaluated to no-action (e.g. strength below min)
	CodeSignalMaxUnits    = 10006 // position count already at maxUnits cap
	CodeSignalRejected    = 10007 // ProcessTrade rejected (risk, capital, duplicate, etc.)
	CodeSignalNoPosition  = 10008 // urgent exit dropped: position already closed (OCO race guard)
	CodeTradeApproved     = 10009 // signal passed all gates → an entry/exit order will be placed
	CodeSignalDropped     = 10010 // non-urgent signal dropped: hand channel full (latest-wins)

	// Order lifecycle codes.
	CodeOrderPlaced        = 10100 // order successfully submitted to exchange
	CodeOrderFilled        = 10101 // order confirmed filled (via WS or poll)
	CodeOrderFailed        = 10102 // exchange returned an error for the order
	CodeOrderPartialCancel = 10103 // partial fill remainder auto-cancelled (below min lot)
	CodeOrderLimitTimeout  = 10104 // limit order cancelled by helm after timeout with no fill
	CodeOrderLimitReprice  = 10105 // reserved: cancel + re-price (not yet implemented; see EXECUTION_TACTICS.md)
	CodeOrderLimitFallback = 10106 // limit order cancelled and re-placed as market after timeout
	CodeOrderCancelled     = 10107 // order cancelled / rejected / expired (detected via poll)
	CodeOrderExitTriggered = 10108 // local stop-loss or take-profit safety net triggered
	CodeOrderExitPlaced    = 10109 // safety net exit orders (SL/TP bracket) submitted to exchange
	CodeOrderDustExit      = 10110 // exit qty below exchange minimum — poslog closed without selling; dust stays at helm level
	CodeOrderExitFailed    = 10111 // exchange-side SL/TP bracket failed after retries — only the in-process local monitor protects the position

	// Hand lifecycle codes.
	CodeHandAutoStopped   = 10200 // hand auto-stopped due to persistent sizing failure or edge risk
	CodeHandStarted       = 10201 // hand run-loop started
	CodeHandStopped       = 10202 // hand run-loop stopped (clean shutdown)
	CodeHandKilled        = 10205 // hand killed — all positions flattened at market
	CodeHandReleased      = 10206 // hand released — open positions orphaned (left live at exchange)
	CodeHandLeverageSet   = 10208 // futures leverage and margin type configured at exchange
	CodePositionExtClosed = 10209 // position externally closed (user manual exit at exchange detected via bracket order cancel)
	CodeFeeAttributed     = 10210 // account-level fee/funding event attributed a proportional share to this hand (see FeeEvent)

	// Helm lifecycle codes.
	CodeHelmPaused          = 10300 // helm paused — all hands will ignore signals
	CodeHelmResumed         = 10301 // helm resumed
	CodeHelmSynced          = 10302 // portfolio synced from exchange
	CodeHelmHalted          = 10303 // helm halted by risk manager
	CodeHelmUnhalted        = 10304 // helm halt reset (manual)
	CodeHelmCredentialError = 10305 // exchange credential rejected mid-run (auth error); helm auto-paused
	CodeHelmAccountError    = 10306 // sustained non-auth account/connection error (network, exchange server error) crossed its escalation threshold; helm auto-paused

	// Reconciler codes — startup gap recovery.
	CodeReconcileRestored      = 10400 // order / position confirmed still live at exchange after restart
	CodeReconcileFillApplied   = 10401 // fill missed during downtime — applied retroactively
	CodeReconcileCancelled     = 10402 // order was cancelled / rejected at exchange during downtime
	CodeReconcileExternalClose = 10403 // position was closed externally during downtime
	CodeReconcileFailed        = 10404 // reconciler could not determine state — hand left stopped

	// Position lifecycle codes — position-level view on top of order-level fills.
	// Pre-fill events (Entering/Adding) fire when the order is placed.
	// Post-fill events (Opened/Added/Closed) fire when the exchange confirms the fill.
	// Cancel events fire when a pending entry or add order is cancelled before it fills.
	CodePositionEntering       = 10503 // entry order placed, position in PhaseEntering — waiting for exchange fill
	CodePositionAdding         = 10504 // pyramid add order placed, position in PhaseAdding — current qty/avg unchanged until fill
	CodePositionOpened         = 10500 // entry fill confirmed — position now PhaseOpen; carries avg_entry, qty
	CodePositionAdded          = 10501 // pyramid add fill confirmed — position grown; carries new total qty, new blended avg_entry
	CodePositionClosed         = 10502 // exit fill confirmed — position closed; carries pnl, pnl_pct, entry_price, exit_price
	CodePositionEnterCancelled = 10505 // entry order cancelled before fill — position never opened
	CodePositionAddCancelled   = 10506 // pyramid add order cancelled before fill — position reverts to prior PhaseOpen state

	// Extended reconciler codes.
	CodeReconcileComplete    = 10405 // reconcile finished — summary of all outcomes (hands checked, fills applied, …)
	CodeReconcileEquityDrift = 10406 // post-reconcile equity cross-check: helm portfolio diverges from exchange balance by > 1%
)

// CodeNames maps event code constants to their human-readable label for Prometheus metrics.
var CodeNames = map[int]string{
	CodeSignalReceived:         "signal_received",
	CodeSignalStale:            "signal_stale",
	CodeSignalHelmPaused:       "signal_helm_paused",
	CodeSignalRateLimited:      "signal_rate_limited",
	CodeSignalDoNothing:        "signal_do_nothing",
	CodeSignalMaxUnits:         "signal_max_units",
	CodeSignalRejected:         "signal_rejected",
	CodeSignalNoPosition:       "signal_no_position",
	CodeTradeApproved:          "trade_approved",
	CodeSignalDropped:          "signal_dropped",
	CodeOrderPlaced:            "order_placed",
	CodeOrderFilled:            "order_filled",
	CodeOrderFailed:            "order_failed",
	CodeOrderPartialCancel:     "order_partial_cancel",
	CodeOrderLimitTimeout:      "order_limit_timeout",
	CodeOrderLimitReprice:      "order_limit_reprice",
	CodeOrderLimitFallback:     "order_limit_fallback",
	CodeOrderCancelled:         "order_cancelled",
	CodeOrderExitTriggered:     "order_exit_triggered",
	CodeOrderExitPlaced:        "order_exit_placed",
	CodeOrderDustExit:          "order_dust_exit",
	CodeOrderExitFailed:        "order_exit_failed",
	CodeHandAutoStopped:        "hand_auto_stopped",
	CodeHandStarted:            "hand_started",
	CodeHandStopped:            "hand_stopped",
	CodeHandKilled:             "hand_killed",
	CodeHandReleased:           "hand_released",
	CodeHandLeverageSet:        "hand_leverage_set",
	CodePositionExtClosed:      "position_ext_closed",
	CodeHelmPaused:             "helm_paused",
	CodeHelmResumed:            "helm_resumed",
	CodeHelmSynced:             "helm_synced",
	CodeHelmHalted:             "helm_halted",
	CodeHelmUnhalted:           "helm_unhalted",
	CodeHelmCredentialError:    "helm_credential_error",
	CodeHelmAccountError:       "helm_account_error",
	CodeReconcileRestored:      "reconcile_restored",
	CodeReconcileFillApplied:   "reconcile_fill_applied",
	CodeReconcileCancelled:     "reconcile_cancelled",
	CodeReconcileExternalClose: "reconcile_external_close",
	CodeReconcileFailed:        "reconcile_failed",
	CodeReconcileComplete:      "reconcile_complete",
	CodeReconcileEquityDrift:   "reconcile_equity_drift",
	CodePositionOpened:         "position_opened",
	CodePositionAdded:          "position_added",
	CodePositionClosed:         "position_closed",
	CodePositionEntering:       "position_entering",
	CodePositionAdding:         "position_adding",
	CodePositionEnterCancelled: "position_enter_cancelled",
	CodePositionAddCancelled:   "position_add_cancelled",
}
