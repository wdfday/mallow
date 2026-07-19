package actor

import (
	"fmt"

	"github.com/google/uuid"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
)

// AccountEventHandler is the outbound port HelmRuntime uses to surface
// account/connection-level conditions that the owning broker/account layer
// needs to react to (pause, mark connection status, notify the user) — as
// opposed to an ordinary per-order retry. Each method decides its own
// action; the interface itself doesn't force a uniform response (e.g. auth
// rejection might mark the broker connection errored, while a margin call
// might just log/notify).
//
// Set by the registry at spawn time via Registry.SetAccountEventHandler.
// fleet.Registry implements this interface; consumed in module/broker.
type AccountEventHandler interface {
	// OnAccountError fires when a sustained/repeated exchange.ErrClass
	// condition crosses its escalation threshold (see errClassThresholds) —
	// not every single error, only ones that persist past the class's
	// tolerance. reason is the last raw error message, for logging.
	OnAccountError(accountID uuid.UUID, class exchange.ErrClass, reason string)

	// OnMarginCall fires on an exchange.RiskEvent (margin ratio /
	// liquidation-price warning pushed over the private WS).
	OnMarginCall(accountID uuid.UUID, ev exchange.RiskEvent)

	// OnTradingRestricted fires when REST account-sync polling detects the
	// exchange flipped a trading-capability flag off (see helm_sync.go).
	OnTradingRestricted(accountID uuid.UUID, reason string)
}

// errClassThresholds is the number of consecutive PlaceOrder failures of a
// given exchange.ErrClass required before it escalates to OnAccountError.
// A zero threshold means the class never escalates — it stays metrics-only
// (exchange.ClassifyGeneric/ErrorClassifier still tag it for Prometheus),
// same as before this generalization for every class except Auth.
var errClassThresholds = [exchange.ErrClassCount]int32{
	exchange.ErrClassAuth:        1, // credentials rarely fail transiently
	exchange.ErrClassNetwork:     5, // a couple of dropped connections is normal
	exchange.ErrClassServerError: 5, // exchange-side 5xx/maintenance — same reasoning
	// All other classes (RateLimit, ClockSkew, InsufficientBalance, LotSize,
	// PriceFilter, InvalidSymbol, OrderNotFound, Unknown) default to 0 —
	// either self-resolving (rate limit backoff, clock sync) or per-order
	// validation issues that don't belong at the account/connection level.
}

// classifyErr uses the exchange adapter's precise ErrorClassifier when the
// wrapped exchange implements it (MeteredExchange forwards this from the
// inner adapter — see infra/exchange/metrics.go), falling back to the
// transport-only string heuristic otherwise. Adapter-specific typed codes
// (Binance/fbinance API codes, OKX sCodes, Bybit retCodes, Alpaca status
// codes) are far more precise than ClassifyGeneric's substring matching —
// see errors_test.go's DNS-vs-signature regression for why that matters.
func classifyErr(ex exchange.Exchange, err error) exchange.ErrClass {
	if c, ok := ex.(exchange.ErrorClassifier); ok {
		return c.ClassifyError(err)
	}
	return exchange.ClassifyGeneric(err)
}

// NoteOrderError classifies a PlaceOrder error and, if its class's streak
// crosses errClassThresholds, self-pauses the helm and fires OnAccountError.
// Called from hand_runner.go on every PlaceOrder failure. Safe to call from
// any goroutine (matches TriggerAccountError's own safety).
func (r *HelmRuntime) NoteOrderError(err error) {
	class := classifyErr(r.Exchange, err)
	threshold := errClassThresholds[class]
	if threshold <= 0 {
		return
	}
	streak := r.errStreaks[class].Add(1)
	if streak < threshold {
		return
	}
	r.errStreaks[class].Store(0) // reset so re-entry after resume starts fresh
	r.TriggerAccountError(class, fmt.Sprintf(
		"%s: %d consecutive failures: %s", exchange.ErrClassName[class], streak, err.Error(),
	))
}

// ResetErrStreaks clears every class's consecutive-failure counter. Called
// on any successful PlaceOrder — a successful placement means the
// connection/exchange/credentials are all currently fine.
func (r *HelmRuntime) ResetErrStreaks() {
	for i := range r.errStreaks {
		r.errStreaks[i].Store(0)
	}
}

// checkTradingRestricted compares the just-synced AccountPermissions against
// the previous sync's (r.lastPermissions) and fires OnTradingRestricted on a
// flip from OK to restricted (CanTrade going false, or TradingBlocked/
// AccountBlocked going true). Does nothing on the first sync with permission
// data (nothing to compare against yet) or when the exchange doesn't report
// permissions at all (perms == nil). Always updates r.lastPermissions to the
// latest snapshot so the next call compares against this one. Called from
// Sync() (helm_sync.go) after every successful REST account sync.
func (r *HelmRuntime) checkTradingRestricted(perms *exchange.AccountPermissions) {
	if perms == nil {
		return
	}
	r.mu.Lock()
	prev := r.lastPermissions
	r.lastPermissions = perms
	r.mu.Unlock()
	if prev == nil {
		return
	}
	var reason string
	switch {
	case !prev.AccountBlocked && perms.AccountBlocked:
		reason = "exchange account blocked"
	case !prev.TradingBlocked && perms.TradingBlocked:
		reason = "exchange trading blocked"
	case prev.CanTrade && !perms.CanTrade:
		reason = "exchange revoked trading permission"
	default:
		return
	}
	if r.AccountEvents != nil {
		go r.AccountEvents.OnTradingRestricted(r.AccountID, reason)
	}
}

// TriggerAccountError self-pauses the helm, emits an account-error event, and
// notifies the broker layer via AccountEvents.OnAccountError (set at spawn
// time) so it can persist the error state (e.g. mark the broker connection
// status). Safe to call from any goroutine — the callback runs in a new
// goroutine to avoid blocking the caller.
func (r *HelmRuntime) TriggerAccountError(class exchange.ErrClass, reason string) {
	r.Pause()
	// CodeHelmCredentialError is the pre-existing, consumer-facing code for the
	// auth case specifically (kept stable for downstream compatibility);
	// CodeHelmAccountError covers every other escalating class added here.
	code := CodeHelmAccountError
	if class == exchange.ErrClassAuth {
		code = CodeHelmCredentialError
	}
	r.EmitEvent(natsapi.HelmEvent{Code: code, Msg: reason})
	if r.AccountEvents != nil {
		go r.AccountEvents.OnAccountError(r.AccountID, class, reason)
	}
}
