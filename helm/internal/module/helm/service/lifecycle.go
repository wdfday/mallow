package service

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"mallow/helm/internal/module/helm/domain"
)

// Enable marks an orchestrator as enabled and triggers an immediate portfolio sync.
func (s *Service) Enable(id uuid.UUID) error {
	if err := s.repo.Update(id, func(o *domain.Helm) error {
		o.Enabled = true
		return nil
	}); err != nil {
		return err
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
		s.hands.StopBots(wasRunning)
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
	toRestart, err := s.spawner.Resume(id)
	if err != nil {
		return err
	}
	if s.hands != nil && len(toRestart) > 0 {
		s.hands.StartBots(toRestart)
	}
	if err := s.repo.Update(id, func(o *domain.Helm) error {
		o.Status = domain.HelmStatusActive
		return nil
	}); err != nil {
		return fmt.Errorf("persist active status: %w", err)
	}
	slog.Info("helm resumed", "id", id, "hands_restarted", len(toRestart))
	return nil
}

// Disable flattens all open positions across every running hand, marks the helm
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
func (s *Service) Disable(id uuid.UUID) error {
	// Step 1: pause runtime so no new signals are accepted.
	runningHandIDs, _ := s.spawner.Pause(id)

	// Step 2: flatten open positions — runtime remains in registry so WS fills
	// from the close orders can still be routed and applied to poslog.
	if s.hands != nil && len(runningHandIDs) > 0 {
		s.hands.KillBots(runningHandIDs)
	}

	// Step 3: persist immediately so restarts do not re-spawn this helm.
	if err := s.repo.Update(id, func(o *domain.Helm) error {
		o.Enabled = false
		o.Status = domain.HelmStatusDisabled
		return nil
	}); err != nil {
		return fmt.Errorf("persist disabled status: %w", err)
	}

	// Step 4: remove runtime after grace period.
	go func() {
		time.Sleep(5 * time.Second)
		handIDs := s.spawner.Teardown(id)
		if s.hands != nil && len(handIDs) > 0 {
			s.hands.PurgeBots(handIDs)
		}
		slog.Info("helm disabled: runtime torn down", "id", id)
	}()

	slog.Info("helm disabled", "id", id, "hands_killed", len(runningHandIDs))
	return nil
}

// ResetHalt clears the risk-manager halt flag and restores the orchestrator to active.
// Does NOT automatically restart hands — caller must call Resume or Start each hand manually.
func (s *Service) ResetHalt(id uuid.UUID) error {
	if err := s.spawner.ResetHalt(id); err != nil {
		return err
	}
	if err := s.repo.Update(id, func(o *domain.Helm) error {
		o.Status = domain.HelmStatusActive
		return nil
	}); err != nil {
		return fmt.Errorf("persist active status: %w", err)
	}
	slog.Info("helm halt reset", "id", id)
	return nil
}
