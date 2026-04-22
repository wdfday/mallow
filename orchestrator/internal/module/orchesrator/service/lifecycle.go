package service

import (
	"context"
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

// Kill flattens all open positions across every running bot and halts the orchestrator.
// The runtime stays alive for monitoring; use Enable/Resume to reactivate.
func (s *Service) Kill(ctx context.Context, id uuid.UUID) error {
	// Collect all running bot IDs before stopping.
	var runningBots []string
	if s.bots != nil {
		// Pause the runtime first so new signals are rejected immediately.
		runningBots, _ = s.spawner.Pause(id)
		// Kill (flatten + stop) each running bot.
		s.bots.KillBots(runningBots)
	}
	if err := s.repo.Update(id, func(o *domain.OrchestratorConfig) error {
		o.Status = "halted"
		return nil
	}); err != nil {
		return fmt.Errorf("persist halted status: %w", err)
	}
	slog.Warn("orchestrator killed", "id", id, "bots_killed", len(runningBots))
	return nil
}

// ResetHalt clears the risk-manager halt flag and restores the orchestrator to active.
// Does NOT automatically restart bots — caller must call Resume or Start each bot manually.
func (s *Service) ResetHalt(id uuid.UUID) error {
	if err := s.spawner.ResetHalt(id); err != nil {
		return err
	}
	if err := s.repo.Update(id, func(o *domain.OrchestratorConfig) error {
		o.Status = "active"
		return nil
	}); err != nil {
		return fmt.Errorf("persist active status: %w", err)
	}
	slog.Info("orchestrator halt reset", "id", id)
	return nil
}
