package service

import (
	"context"
	"fmt"

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
	if rt, _ := s.registry.Get(bi.Data.HelmID); rt != nil && rt.IsPaused() {
		return fmt.Errorf("helm %q is paused — resume it first", bi.Data.HelmID)
	}
	s.heraldRegister(id, bi.Data)
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

func (s *Service) Pause(id uuid.UUID) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if !bi.Runner.IsRunning() {
		return fmt.Errorf("hand %q is not running", id)
	}
	bi.Runner.Pause()
	return s.repo.Update(id, func(d *domain.Hand) error {
		d.Status = domain.HandStatusPaused
		return nil
	})
}

func (s *Service) Resume(id uuid.UUID) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if !bi.Runner.IsRunning() {
		return fmt.Errorf("hand %q is not running", id)
	}
	bi.Runner.Resume()
	return s.repo.Update(id, func(d *domain.Hand) error {
		d.Status = domain.HandStatusRunning
		return nil
	})
}

func (s *Service) Kill(ctx context.Context, id uuid.UUID) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	s.heraldDeregister(id)
	bi.Runner.Kill(ctx)
	bi.Data.Status = domain.HandStatusStopped

	s.mu.Lock()
	delete(s.hands, id)
	s.mu.Unlock()

	return s.repo.Update(id, func(d *domain.Hand) error {
		d.Status = domain.HandStatusStopped
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
	bi.Data.Status = domain.HandStatusStopped

	s.mu.Lock()
	delete(s.hands, id)
	s.mu.Unlock()

	return s.repo.Update(id, func(d *domain.Hand) error {
		d.Status = domain.HandStatusStopped
		return nil
	})
}

// Restart stops then starts a hand (re-registers with herald).
func (s *Service) Restart(id uuid.UUID) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	s.heraldDeregister(id)
	bi.Runner.Stop()
	if rt, _ := s.registry.Get(bi.Data.HelmID); rt != nil && rt.IsPaused() {
		return fmt.Errorf("helm %q is paused — resume it first", bi.Data.HelmID)
	}
	s.heraldRegister(id, bi.Data)
	bi.Runner.Start()
	bi.Data.Status = domain.HandStatusRunning
	return s.repo.Update(id, func(d *domain.Hand) error {
		d.Status = domain.HandStatusRunning
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
func (s *Service) StartBots(ids []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, idStr := range ids {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		if bi, ok := s.hands[id]; ok && !bi.Runner.IsRunning() {
			s.heraldRegister(id, bi.Data)
			bi.Runner.Start()
		}
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

// KillBots cascade-kills hands (flatten positions + stop) in-memory only. Called by helm kill.
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
