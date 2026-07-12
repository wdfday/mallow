package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"mallow/helm/internal/module/helm/domain"
)

// reactivate is the single path back to HelmStatusActive: it un-gates the live
// runtime (spawner.Resume clears rt.paused, which is the real gate — DB status
// alone does not stop or start anything), restarts whichever hands were running
// before the runtime was gated, persists the active status, and re-syncs the
// portfolio. Every recovery action that ends in "active" — Resume, Enable on an
// already-spawned (paused/halted/error) helm, and ResetHalt — routes through
// this so the DB status and the runtime's actual gating state can never diverge
// the way a bare `status = active` write would let them.
//
// Returns an error if there is no live runtime to reactivate (e.g. the helm was
// disabled and torn down) — callers that also handle the "cold start" case
// (Enable) use that to fall back to spawning a fresh runtime instead.
func (s *Service) reactivate(id uuid.UUID) (int, error) {
	toRestart, err := s.spawner.Resume(id)
	if err != nil {
		return 0, err
	}
	if s.hands != nil && len(toRestart) > 0 {
		s.hands.StartBots(id, toRestart)
	}
	if err := s.repo.Update(id, func(o *domain.Helm) error {
		o.Status = domain.HelmStatusActive
		return nil
	}); err != nil {
		return 0, fmt.Errorf("persist active status: %w", err)
	}
	// Fire-and-forget SyncOne so portfolio state (cash/equity/positions) reflects
	// the exchange immediately — otherwise it stays at whatever it was when the
	// helm paused/errored (often zero) until the next 5-minute poll tick.
	s.spawner.SyncOne(id)
	return len(toRestart), nil
}

// canonicalRecoveryFrom returns the error to reject a request with when the
// helm is in a state that has its own dedicated way back to active — paused
// (/resume), halted (/halt/reset), or error (rotate the broker connection's
// API key). Every other action must route through that path instead of
// jumping straight to enable/disable, so the DB status and the runtime's
// actual gating/position state can't drift apart. Returns nil for
// active/disabled, where the caller's own transition is valid.
func canonicalRecoveryFrom(status domain.HelmStatus) error {
	switch status {
	case domain.HelmStatusPaused:
		return fmt.Errorf("helm is paused — use /resume first")
	case domain.HelmStatusHalted:
		return fmt.Errorf("helm is halted — use /halt/reset first")
	case domain.HelmStatusError:
		return fmt.Errorf("helm has a credential error — rotate the broker connection's API key first")
	default:
		return nil
	}
}

// Enable marks an orchestrator as enabled and, for a true disabled→active
// transition, spawns a fresh runtime. Rejected if the helm is paused/halted/error:
// each of those has its own dedicated recovery action (resume/halt-reset/rotate-key)
// that un-gates the *existing* runtime correctly — Enable's Spawn is idempotent and
// does nothing when a runtime already exists, so allowing Enable here would flip the
// DB status to active while the runtime stayed silently gated.
func (s *Service) Enable(id uuid.UUID) error {
	cfg, err := s.repo.Get(id)
	if err != nil {
		return err
	}
	if err := canonicalRecoveryFrom(cfg.Status); err != nil {
		return err
	}

	// Persist status=active.
	if err := s.repo.Update(id, func(o *domain.Helm) error {
		o.Status = domain.HelmStatusActive
		return nil
	}); err != nil {
		return err
	}

	if s.creds != nil {
		exchCfg, err := s.creds.GetCredentialsByAccountID(context.Background(), cfg.AccountID.String())
		if err != nil {
			slog.Warn("helm enable: could not fetch credentials for spawn", "helm_id", id, "err", err)
		} else {
			// BrokerType is authoritative from the helm config (not the credentials resp).
			exchCfg.BrokerType = cfg.BrokerType
			if spawnErr := s.spawner.Spawn(cfg, exchCfg); spawnErr != nil {
				slog.Error("helm enable: spawn failed", "helm_id", id, "err", spawnErr)
			} else {
				slog.Info("helm enable: runtime spawned", "helm_id", id)
			}
		}
	}

	s.spawner.SyncOne(id)
	return nil
}

// Pause pauses an orchestrator and cascade-stops all its running bots.
func (s *Service) Pause(id uuid.UUID) error {
	wasRunning, err := s.spawner.Pause(id)
	if err != nil {
		return err
	}
	if s.hands != nil && len(wasRunning) > 0 {
		s.hands.StopBots(id, wasRunning)
	}
	if err := s.repo.Update(id, func(o *domain.Helm) error {
		o.Status = domain.HelmStatusPaused
		return nil
	}); err != nil {
		return fmt.Errorf("persist paused status: %w", err)
	}
	slog.Info("helm paused", "id", id, "hands_stopped", len(wasRunning))
	return nil
}

// Resume resumes a paused orchestrator and restarts hands that were running before pause.
func (s *Service) Resume(id uuid.UUID) error {
	n, err := s.reactivate(id)
	if err != nil {
		return err
	}
	slog.Info("helm resumed", "id", id, "hands_restarted", n)
	return nil
}

// Disable flattens all open positions across every non-terminal hand, marks the helm
// disabled, and tears down the runtime after a short grace period so in-flight
// WS fills can settle before the runtime is removed from the registry.
//
// Ordering is critical:
//  1. Pause  — gate new signals immediately (runtime still in registry)
//  2. KillBots — place market close orders (fills route to the live runtime)
//  3. Persist disabled status in DB
//  4. Async teardown after 5 s — market fills should arrive well within this window
//
// Use Enable to re-spawn a fresh runtime.
//
// Rejected if the helm is paused/halted/error: those states must go back to
// active through their own recovery action first (resume/halt-reset/rotate-key),
// same reasoning as Enable — jumping straight to disable from a gated state
// would tear the runtime down without ever going through the un-gating step
// those actions perform.
func (s *Service) Disable(id uuid.UUID) error {
	cfg, err := s.repo.Get(id)
	if err != nil {
		return err
	}
	if err := canonicalRecoveryFrom(cfg.Status); err != nil {
		return err
	}

	// Step 1: pause runtime so no new signals are accepted.
	_, _ = s.spawner.Pause(id)

	// Step 2: flatten open positions — runtime remains in registry so WS fills
	// from the close orders can still be routed and applied to poslog.
	// Deliberately NOT using Pause's "was running" return value here: it only
	// reflects the hand runner's live IsRunning() state, which can lag behind
	// what's actually open at the exchange (e.g. a runner that crashed mid-position).
	// NonTerminalHandIDs reads persisted status instead, so every hand that could
	// still hold a position gets a kill attempt.
	var handIDs []string
	if s.hands != nil {
		handIDs = s.hands.NonTerminalHandIDs(id)
	}
	if len(handIDs) > 0 {
		s.hands.KillBots(id, handIDs)
	}

	// Step 3: persist immediately so restarts do not re-spawn this helm.
	if err := s.repo.Update(id, func(o *domain.Helm) error {
		o.Status = domain.HelmStatusDisabled
		return nil
	}); err != nil {
		return fmt.Errorf("persist disabled status: %w", err)
	}

	// Step 4: remove runtime after grace period.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panic recovered", "recover", r)
			}
		}()
		time.Sleep(5 * time.Second)
		s.spawner.Teardown(id)
		slog.Info("helm disabled: runtime torn down", "id", id)
	}()

	slog.Info("helm disabled", "id", id, "hands_killed", len(handIDs))
	return nil
}

// MarkError sets the helm status to error to indicate a credential failure detected
// mid-run. The runtime is already paused by TriggerAuthError before this is called.
// Recover by rotating credentials (RotateKey auto-resumes).
func (s *Service) MarkError(id uuid.UUID) error {
	return s.repo.Update(id, func(o *domain.Helm) error {
		o.Status = domain.HelmStatusError
		return nil
	})
}

// ResetHalt clears the risk-manager halt flag and restores the orchestrator to active
// via reactivate. A halt tripped live never sets rt.paused or stops hands (RiskMgr.IsHalted
// alone gates new entries), so reactivate's Resume/StartBots are no-ops there — but if the
// helm was hydrated from a restart while halted, Spawn does set rt.paused, and without
// routing through reactivate this would clear the risk flag while leaving the runtime
// gated and its hands never restarted.
func (s *Service) ResetHalt(id uuid.UUID) error {
	if err := s.spawner.ResetHalt(id); err != nil {
		return err
	}
	n, err := s.reactivate(id)
	if err != nil {
		return err
	}
	slog.Info("helm halt reset", "id", id, "hands_restarted", n)
	return nil
}

// HydrateAll loads all non-disabled helms from the DB and spawns their runtimes.
// Paused helms are spawned then immediately gated so signal delivery is blocked
// without tearing down the runtime. Called once at startup.
func (s *Service) HydrateAll(ctx context.Context) error {
	if s.creds == nil {
		return fmt.Errorf("helm service: CredentialFetcher not wired before HydrateAll")
	}
	cfgs, err := s.repo.All()
	if err != nil {
		return err
	}
	spawned := 0
	for _, cfg := range cfgs {
		if cfg.Status == domain.HelmStatusDisabled {
			continue
		}
		exchCfg, err := s.creds.GetCredentialsByAccountID(ctx, cfg.AccountID.String())
		if err != nil {
			slog.Error("helm hydrate: fetch credentials failed", "helm_id", cfg.ID, "account_id", cfg.AccountID, "err", err)
			continue
		}
		// BrokerType is authoritative from the helm config (not the credentials resp) —
		// GetCredentialsByAccountID returns the parent BrokerConnection's type (e.g.
		// "binance"), which is wrong for a futures_usdm sub-account remapped to "fbinance"
		// at creation time. Without this, every restart re-spawns that helm with the
		// spot binance client instead of fbinance, silently reading the spot balance.
		exchCfg.BrokerType = cfg.BrokerType
		if err := s.spawner.Spawn(cfg, exchCfg); err != nil {
			slog.Error("helm hydrate: spawn failed", "helm_id", cfg.ID, "err", err)
			continue
		}
		if cfg.Status == domain.HelmStatusPaused {
			_ = s.Pause(cfg.ID)
			slog.Info("helm hydrate: spawned paused", "helm_id", cfg.ID)
		}
		spawned++
	}
	slog.Info("helms hydrated", "count", spawned, "total", len(cfgs))
	return nil
}
