package service

import (
	"context"
	"fmt"

	"orchestrator/internal/module/bot/domain"
)

func (s *Service) Start(id string) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if rt, _ := s.registry.Get(bi.Data.OrchestratorID); rt != nil && rt.IsPaused() {
		return fmt.Errorf("orchestrator %q is paused — resume it first", bi.Data.OrchestratorID)
	}
	s.heraldRegister(id, bi.Data.Config)
	bi.Bot.Start()
	bi.Data.Status = "running"
	return s.repo.Update(id, func(d *domain.BotInstance) error {
		d.Status = "running"
		return nil
	})
}

func (s *Service) Stop(id string) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	s.heraldDeregister(id)
	bi.Bot.Stop()
	bi.Data.Status = "stopped"
	return s.repo.Update(id, func(d *domain.BotInstance) error {
		d.Status = "stopped"
		return nil
	})
}

func (s *Service) Pause(id string) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if !bi.Bot.IsRunning() {
		return fmt.Errorf("bot %q is not running", id)
	}
	bi.Bot.Pause()
	return s.repo.Update(id, func(d *domain.BotInstance) error {
		d.Status = "paused"
		return nil
	})
}

func (s *Service) Resume(id string) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if !bi.Bot.IsRunning() {
		return fmt.Errorf("bot %q is not running", id)
	}
	bi.Bot.Resume()
	return s.repo.Update(id, func(d *domain.BotInstance) error {
		d.Status = "running"
		return nil
	})
}

func (s *Service) Kill(ctx context.Context, id string) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	s.heraldDeregister(id)
	bi.Bot.Kill(ctx)
	bi.Data.Status = "stopped"
	return s.repo.Update(id, func(d *domain.BotInstance) error {
		d.Status = "stopped"
		return nil
	})
}

// StopBots cascade-stops bots in-memory only (no DB persist). Called by orchestrator pause.
func (s *Service) StopBots(ids []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range ids {
		if bi, ok := s.bots[id]; ok && bi.Bot.IsRunning() {
			bi.Bot.Stop()
		}
	}
}

// StartBots cascade-starts bots in-memory only (no DB persist). Called by orchestrator resume.
func (s *Service) StartBots(ids []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range ids {
		if bi, ok := s.bots[id]; ok && !bi.Bot.IsRunning() {
			bi.Bot.Start()
		}
	}
}
