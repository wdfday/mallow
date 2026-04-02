package service

import (
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"orchestrator/internal/module/orchesrator/domain"
)

// Enable marks an orchestrator as enabled and triggers an immediate portfolio sync.
func (s *Service) Enable(id uuid.UUID) error {
	if err := s.repo.Update(id, func(o *domain.OrchestratorConfig) error {
		o.Enabled = true
		return nil
	}); err != nil {
		return err
	}
	s.spawner.SyncOne(id)
	return nil
}

// Disable marks an orchestrator as disabled, blocking bot creation and deletion.
func (s *Service) Disable(id uuid.UUID) error {
	return s.repo.Update(id, func(o *domain.OrchestratorConfig) error {
		o.Enabled = false
		return nil
	})
}

// Pause pauses an orchestrator and cascade-stops all its running bots.
func (s *Service) Pause(id uuid.UUID) error {
	wasRunning, err := s.spawner.Pause(id)
	if err != nil {
		return err
	}
	if s.bots != nil && len(wasRunning) > 0 {
		s.bots.StopBots(wasRunning)
	}
	if err := s.repo.Update(id, func(o *domain.OrchestratorConfig) error {
		o.Status = "paused"
		return nil
	}); err != nil {
		return fmt.Errorf("persist paused status: %w", err)
	}
	slog.Info("orchestrator paused", "id", id, "bots_stopped", len(wasRunning))
	return nil
}

// Resume resumes a paused orchestrator and restarts bots that were running before pause.
func (s *Service) Resume(id uuid.UUID) error {
	toRestart, err := s.spawner.Resume(id)
	if err != nil {
		return err
	}
	if s.bots != nil && len(toRestart) > 0 {
		s.bots.StartBots(toRestart)
	}
	if err := s.repo.Update(id, func(o *domain.OrchestratorConfig) error {
		o.Status = "active"
		return nil
	}); err != nil {
		return fmt.Errorf("persist active status: %w", err)
	}
	slog.Info("orchestrator resumed", "id", id, "bots_restarted", len(toRestart))
	return nil
}
