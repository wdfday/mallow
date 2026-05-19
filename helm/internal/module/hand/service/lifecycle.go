package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"mallow/helm/internal/module/hand/domain"
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
	return s.repo.Update(id, func(d *domain.Hand) error {
		d.Status = domain.HandStatusStopped
		return nil
	})
}

// Release stops the hand without closing open positions.
// Open legs are marked position_orphaned in the poslog so the reconciler
// never restores them to this hand on restart.
func (s *Service) Release(ctx context.Context, id uuid.UUID) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	s.heraldDeregister(id)
	bi.Runner.Release(ctx)
	bi.Data.Status = domain.HandStatusStopped
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	ctx := context.Background()
	for _, idStr := range ids {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		if bi, ok := s.hands[id]; ok {
			s.heraldDeregister(id)
			bi.Runner.Kill(ctx)
		}
	}
}
