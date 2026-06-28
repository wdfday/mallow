package runtime

import (
	"context"
	"errors"
	"log/slog"

	heraldinfra "mallow/helm/internal/infra/herald"
)

// HeraldRegistrar is the outbound port to the signal-engine hand registry.
// Implemented by infra/herald.Client; nil-safe usage is on HelmRuntime.
type HeraldRegistrar interface {
	ValidateHand(ctx context.Context, helmID, exchangeName, symbol, script, timeframe string, isFuture bool) error
	RegisterHand(ctx context.Context, handID, helmID, exchangeName, symbol, script, timeframe string, isFuture bool) error
	DeregisterHand(ctx context.Context, handID string) error
}

// ValidateHandAll runs herald dry-run validation for all symbols in the hand config.
// Exchange name is taken from rt.Exchange.
//
// ErrUnavailable — herald offline/unreachable: warn and skip (hand accepted without script check).
// ErrRejected    — herald rejected request (bad script, unknown symbol, unsupported TF): return to caller.
func (r *HelmRuntime) ValidateHandAll(ctx context.Context, helmID string, symbols []string, script, timeframe string, isFuture bool) error {
	if r.Herald == nil {
		slog.Warn("herald not wired — skipping validate (hand will be accepted without script check)", "helm_id", helmID)
		return nil
	}
	exchangeName := r.Exchange.Name()
	for _, sym := range symbols {
		if err := r.Herald.ValidateHand(ctx, helmID, exchangeName, sym, script, timeframe, isFuture); err != nil {
			if errors.Is(err, heraldinfra.ErrUnavailable) {
				slog.Warn("herald unavailable — skipping script validation", "helm_id", helmID, "symbol", sym, "err", err)
				return nil
			}
			return err // ErrRejected or unexpected — surface to caller
		}
	}
	return nil
}

// RegisterHandAll registers all (hand, symbol) pairs with herald.
// Exchange name is taken from rt.Exchange so callers do not need to know it.
//
// ErrUnavailable — herald offline: warn and return nil (hand will re-register on next heartbeat).
// ErrRejected    — herald refused the registration: return to caller.
func (r *HelmRuntime) RegisterHandAll(ctx context.Context, handID, helmID string, symbols []string, script, timeframe string, isFuture bool) error {
	if r.Herald == nil {
		slog.Warn("herald not wired — hand will not receive signals", "hand_id", handID, "helm_id", helmID)
		return nil
	}
	exchangeName := r.Exchange.Name()
	for _, sym := range symbols {
		if err := r.Herald.RegisterHand(ctx, handID, helmID, exchangeName, sym, script, timeframe, isFuture); err != nil {
			if errors.Is(err, heraldinfra.ErrUnavailable) {
				slog.Warn("herald unavailable — hand registered locally but will not receive signals until re-register", "hand_id", handID, "symbol", sym, "err", err)
				return nil
			}
			return err // ErrRejected or unexpected — surface to caller
		}
		slog.Info("herald registered", "hand_id", handID, "symbol", sym)
	}
	return nil
}

// DeregisterHand removes a hand from the herald registry. Logs but does not return errors.
func (r *HelmRuntime) DeregisterHand(ctx context.Context, handID string) {
	if r.Herald == nil {
		return
	}
	if err := r.Herald.DeregisterHand(ctx, handID); err != nil {
		if errors.Is(err, heraldinfra.ErrUnavailable) {
			slog.Warn("herald unavailable during deregister — hand may linger in registry until herald restarts", "hand_id", handID)
			return
		}
		slog.Warn("herald deregister failed", "hand_id", handID, "err", err)
	}
}
