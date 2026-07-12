package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"mallow/helm/internal/module/hand/domain"
)

// Start registers the hand with herald and starts its runner.
// helmID is required — the caller (HTTP handler) always knows it from the path.
func (s *Service) Start(handID, helmID uuid.UUID) error {
	rt, err := s.registry.Get(helmID)
	if err != nil {
		return err
	}
	// Paused/halted/error helms reject manual hand starts — stop/kill/release stay
	// open as the exit path, but nothing that resumes trading should proceed while
	// the parent helm isn't active.
	if rt.IsPaused() {
		return fmt.Errorf("helm is not active — cannot start a hand")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rt.StartHand(ctx, handID.String()); err != nil {
		return err
	}
	return s.repo.Update(handID, func(d *domain.Hand) error {
		d.Status = domain.HandStatusRunning
		return nil
	})
}

// Stop deregisters the hand from herald and stops its runner.
func (s *Service) Stop(handID, helmID uuid.UUID) error {
	rt, err := s.registry.Get(helmID)
	if err != nil {
		return err
	}
	// killed/released are terminal — StopHand is a no-op against the runtime once the
	// hand has already been removed from it (RemoveHand), so without this guard the DB
	// write below would silently flip a terminal status back to "stopped", making a
	// flattened/orphaned hand look startable again and erasing the terminal audit trail.
	cur, err := s.repo.Get(handID)
	if err != nil {
		return err
	}
	if cur.Status.IsTerminal() {
		return fmt.Errorf("hand %q is %s — terminal hands cannot be stopped", handID, cur.Status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rt.StopHand(ctx, handID.String())
	return s.repo.Update(handID, func(d *domain.Hand) error {
		d.Status = domain.HandStatusStopped
		return nil
	})
}

// Kill stops the hand and immediately flattens all open positions at market.
// The hand becomes terminal (HandStatusKilled) and cannot be restarted.
func (s *Service) Kill(ctx context.Context, handID, helmID uuid.UUID) error {
	rt, err := s.registry.Get(helmID)
	if err != nil {
		return err
	}
	// Block killing an already-terminal hand: if it was previously released, the
	// runtime has already dropped it (RemoveHand), so KillHand would return found=false
	// and this would just overwrite HandStatusReleased with HandStatusKilled — a false
	// audit record claiming positions were flattened when they were actually orphaned.
	cur, err := s.repo.Get(handID)
	if err != nil {
		return err
	}
	if cur.Status.IsTerminal() {
		return fmt.Errorf("hand %q is already %s", handID, cur.Status)
	}
	finalMetrics, found := rt.KillHand(ctx, handID.String())
	return s.repo.Update(handID, func(d *domain.Hand) error {
		d.Status = domain.HandStatusKilled
		if found {
			d.FinalMetrics = &finalMetrics
		}
		return nil
	})
}

// Release stops the hand without flattening positions (positions become orphaned in poslog).
func (s *Service) Release(ctx context.Context, handID, helmID uuid.UUID) error {
	rt, err := s.registry.Get(helmID)
	if err != nil {
		return err
	}
	// Same terminal guard as Kill — prevents a killed hand from being silently
	// relabeled as released (or double-released) with no matching runtime effect.
	cur, err := s.repo.Get(handID)
	if err != nil {
		return err
	}
	if cur.Status.IsTerminal() {
		return fmt.Errorf("hand %q is already %s", handID, cur.Status)
	}
	finalMetrics, found := rt.ReleaseHand(ctx, handID.String())
	return s.repo.Update(handID, func(d *domain.Hand) error {
		d.Status = domain.HandStatusReleased
		if found {
			d.FinalMetrics = &finalMetrics
		}
		return nil
	})
}

// ── Cascade operations (called by helm lifecycle) ─────────────────────────────

// NonTerminalHandIDs returns the IDs of every hand under helmID that is not yet
// killed/released. Used by helm Disable instead of the live "was running" set:
// deriving the kill list from in-memory IsRunning() misses hands that were already
// stopped by an earlier pause (a stopped hand can still hold an open position),
// so a helm disabled after being paused would otherwise skip flattening entirely.
func (s *Service) NonTerminalHandIDs(helmID uuid.UUID) []string {
	all := s.repo.AllByHelm(helmID)
	ids := make([]string, 0, len(all))
	for _, d := range all {
		if !d.Status.IsTerminal() {
			ids = append(ids, d.ID.String())
		}
	}
	return ids
}

// StopBots cascade-stops hands in-memory only (no DB persist). Called by helm pause.
func (s *Service) StopBots(helmID uuid.UUID, ids []string) {
	rt, err := s.registry.Get(helmID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, idStr := range ids {
		rt.StopHand(ctx, idStr)
	}
}

// StartBots cascade-starts hands in-memory only (no DB persist). Called by helm resume.
// Only restarts hands whose persisted status is HandStatusRunning (not manually stopped).
func (s *Service) StartBots(helmID uuid.UUID, ids []string) {
	rt, err := s.registry.Get(helmID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, idStr := range ids {
		hand, data, ok := rt.GetHandEntry(idStr)
		if !ok || hand.IsRunning() || data.Status == domain.HandStatusStopped {
			continue
		}
		if err := rt.StartHand(ctx, idStr); err != nil {
			slog.Error("hand start bot: failed", "hand_id", idStr, "err", err)
		}
	}
}

// KillBots cascade-kills hands (flatten positions + stop) and persists stopped status. Called by helm kill.
func (s *Service) KillBots(helmID uuid.UUID, ids []string) {
	rt, err := s.registry.Get(helmID)
	if err != nil {
		return
	}
	ctx := context.Background()
	for _, idStr := range ids {
		rt.KillHand(ctx, idStr)
	}
	for _, idStr := range ids {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		if err := s.repo.Update(id, func(d *domain.Hand) error {
			d.Status = domain.HandStatusStopped
			return nil
		}); err != nil {
			slog.Error("hand: persist stopped status failed on helm kill", "hand_id", id, "err", err)
		}
	}
}

// DeleteBotsByHelm hard-deletes all Hand rows in the DB for the given helm.
func (s *Service) DeleteBotsByHelm(helmID uuid.UUID) error {
	return s.repo.DeleteByHelm(helmID)
}
