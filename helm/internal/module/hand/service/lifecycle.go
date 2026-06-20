package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
)

func (s *Service) Start(id uuid.UUID) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if bi.Data.Status.IsTerminal() {
		return fmt.Errorf("hand %q is %s — terminal hands cannot be restarted", id, bi.Data.Status)
	}
	if err := s.heraldRegister(id, bi.Data); err != nil {
		return fmt.Errorf("hand start: %w", err)
	}
	bi.Runner.Start()
	bi.Data.Status = domain.HandStatusRunning
	return s.repo.Update(id, func(d *domain.Hand) error {
		d.Status = domain.HandStatusRunning
		return nil
	})
}

func (s *Service) Stop(id uuid.UUID) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	s.heraldDeregister(id)
	bi.Runner.Stop()
	bi.Data.Status = domain.HandStatusStopped
	return s.repo.Update(id, func(d *domain.Hand) error {
		d.Status = domain.HandStatusStopped
		return nil
	})
}

// Kill stops the hand and immediately flattens all open positions at market.
// The hand becomes terminal (HandStatusKilled) and cannot be restarted.
func (s *Service) Kill(ctx context.Context, id uuid.UUID) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if bi.Data.Status.IsTerminal() {
		return fmt.Errorf("hand %q is already terminal (%s)", id, bi.Data.Status)
	}
	s.heraldDeregister(id)
	bi.Runner.Kill(ctx)

	if rt, _ := s.registry.Get(bi.Data.HelmID); rt != nil {
		rt.RemoveHand(id.String())
	}
	s.mu.Lock()
	delete(s.hands, id)
	s.mu.Unlock()

	return s.repo.Update(id, func(d *domain.Hand) error {
		d.Status = domain.HandStatusKilled
		return nil
	})
}

func (s *Service) Release(ctx context.Context, id uuid.UUID) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	s.heraldDeregister(id)
	bi.Runner.Release(ctx)
	finalMetrics := bi.Runner.MetricsView() // snapshot before removing
	bi.Data.Status = domain.HandStatusReleased

	if rt, _ := s.registry.Get(bi.Data.HelmID); rt != nil {
		rt.RemoveHand(id.String())
	}
	s.mu.Lock()
	delete(s.hands, id)
	s.mu.Unlock()

	return s.repo.Update(id, func(d *domain.Hand) error {
		d.Status = domain.HandStatusReleased
		d.FinalMetrics = &finalMetrics
		return nil
	})
}

// StopBots cascade-stops hands in-memory only (no DB persist). Called by helm pause.
// ids are string UUIDs from runtime.Registry — parsed here at the boundary.
func (s *Service) StopBots(ids []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, idStr := range ids {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		if bi, ok := s.hands[id]; ok && bi.Runner.IsRunning() {
			s.heraldDeregister(id)
			bi.Runner.Stop()
		}
	}
}

// StartBots cascade-starts hands in-memory only (no DB persist). Called by helm resume.
// Only restarts hands whose DB status is still `running` (i.e. hands stopped by helm cascade,
// not hands explicitly stopped by the user — those have status=stopped and are skipped).
func (s *Service) StartBots(ids []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, idStr := range ids {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		bi, ok := s.hands[id]
		if !ok || bi.Runner.IsRunning() || bi.Data.Status == domain.HandStatusStopped {
			continue
		}
		if err := s.heraldRegister(id, bi.Data); err != nil {
			slog.Error("hand start bot: herald register failed", "hand_id", id, "err", err)
			continue
		}
		bi.Runner.Start()
	}
}

// PurgeBots removes hand entries from the in-memory map. Called after helm teardown.
func (s *Service) PurgeBots(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, idStr := range ids {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		delete(s.hands, id)
	}
}

// DeleteBotsByHelm hard-deletes all Hand rows in the DB for the given helm.
// Called during broker connection delete so the sweep is complete.
func (s *Service) DeleteBotsByHelm(helmID uuid.UUID) error {
	return s.repo.DeleteByHelm(helmID)
}

// KillBots cascade-kills hands (flatten positions + stop) and persists stopped status to DB. Called by helm kill.
func (s *Service) KillBots(ids []string) {
	ctx := context.Background()
	toKill := make([]*runtime.HandRef, 0, len(ids))

	s.mu.Lock()
	for _, idStr := range ids {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		if bi, ok := s.hands[id]; ok {
			toKill = append(toKill, bi)
			delete(s.hands, id)
		}
	}
	s.mu.Unlock()

	for _, bi := range toKill {
		s.heraldDeregister(bi.Data.ID)
		bi.Runner.Kill(ctx)
		bi.Data.Status = domain.HandStatusStopped
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

// ReleaseBots cascade-releases hands (stop without flattening, positions become orphaned) and persists stopped status to DB.
// Called by helm disable.
func (s *Service) ReleaseBots(ids []string) {
	ctx := context.Background()
	toRelease := make([]*runtime.HandRef, 0, len(ids))

	s.mu.Lock()
	for _, idStr := range ids {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		if bi, ok := s.hands[id]; ok {
			toRelease = append(toRelease, bi)
			delete(s.hands, id)
		}
	}
	s.mu.Unlock()

	for _, bi := range toRelease {
		s.heraldDeregister(bi.Data.ID)
		bi.Runner.Release(ctx)
		bi.Data.Status = domain.HandStatusStopped
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
			slog.Error("hand: persist stopped status failed on helm disable", "hand_id", id, "err", err)
		}
	}
}

// AllocateCapital updates the allocated capital of a hand (adds the specified delta).
// If the hand is in-memory, it dynamically updates the runner's allocated capital.
func (s *Service) AllocateCapital(id uuid.UUID, delta decimal.Decimal) (decimal.Decimal, error) {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return decimal.Zero, err
	}

	newCapital := bi.Data.AllocatedCapital.Add(delta)
	if !newCapital.IsPositive() {
		return decimal.Zero, fmt.Errorf("new allocated capital must be greater than zero")
	}

	// Update DB
	err = s.repo.Update(id, func(d *domain.Hand) error {
		d.AllocatedCapital = newCapital
		return nil
	})
	if err != nil {
		return decimal.Zero, err
	}

	// Update service cache data
	bi.Data.AllocatedCapital = newCapital

	// Update runtime Hand runner
	bi.Runner.SetAllocatedCapital(newCapital)

	return newCapital, nil
}
