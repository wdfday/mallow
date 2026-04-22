package service_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"orchestrator/internal/module/bot/domain"
	botrepo "orchestrator/internal/module/bot/repo"
	"orchestrator/internal/module/bot/service"
	"orchestrator/internal/runtime"
)

// newSvc creates a Service backed by an in-memory store and an empty Registry.
// herald is nil (graceful no-op in tests).
func newSvc() *service.Service {
	store := botrepo.NewMemoryStore()
	reg := runtime.NewRegistry(nil, nil)
	return service.NewService(store, reg, nil)
}

// validConfig returns a minimal valid BotConfig.
func validConfig(orchID uuid.UUID) domain.BotConfig {
	return domain.BotConfig{
		Name:           "test-bot",
		OrchestratorID: orchID,
		Symbols:        []string{"AAPL"},
		Strategy:       domain.StrategyConfig{Name: "ma_crossover"},
	}
}

// ── Create — input validation ─────────────────────────────────────────────────

func TestCreate_EmptyName_ReturnsError(t *testing.T) {
	svc := newSvc()
	cfg := validConfig(uuid.New())
	cfg.Name = ""

	_, err := svc.Create(cfg)
	if err == nil {
		t.Fatal("expected error for empty bot name")
	}
	if err.Error() != "bot name is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreate_ZeroOrchestratorID_ReturnsError(t *testing.T) {
	svc := newSvc()
	cfg := validConfig(uuid.Nil)

	_, err := svc.Create(cfg)
	if err == nil {
		t.Fatal("expected error for zero orchestrator_id")
	}
	if err.Error() != "orchestrator_id is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreate_EmptySymbols_ReturnsError(t *testing.T) {
	svc := newSvc()
	cfg := validConfig(uuid.New())
	cfg.Symbols = nil

	_, err := svc.Create(cfg)
	if err == nil {
		t.Fatal("expected error for empty symbols slice")
	}
	if err.Error() != "at least one symbol is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreate_EmptySliceSymbols_ReturnsError(t *testing.T) {
	svc := newSvc()
	cfg := validConfig(uuid.New())
	cfg.Symbols = []string{} // empty but non-nil

	_, err := svc.Create(cfg)
	if err == nil {
		t.Fatal("expected error for zero-length symbols slice")
	}
	if err.Error() != "at least one symbol is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Validation fires before registry lookup — confirm ordering.
func TestCreate_ValidationBeforeRuntimeLookup(t *testing.T) {
	svc := newSvc()

	// All three validation errors must fire *before* any registry call.
	testCases := []struct {
		name   string
		cfg    domain.BotConfig
		errMsg string
	}{
		{
			name:   "empty name",
			cfg:    domain.BotConfig{OrchestratorID: uuid.New(), Symbols: []string{"X"}},
			errMsg: "bot name is required",
		},
		{
			name:   "zero orchestrator id",
			cfg:    domain.BotConfig{Name: "b", Symbols: []string{"X"}},
			errMsg: "orchestrator_id is required",
		},
		{
			name:   "no symbols",
			cfg:    domain.BotConfig{Name: "b", OrchestratorID: uuid.New()},
			errMsg: "at least one symbol is required",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(tc.cfg)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if err.Error() != tc.errMsg {
				t.Fatalf("expected %q, got %q", tc.errMsg, err.Error())
			}
		})
	}
}

// After all field validation passes, Create should fail because the empty
// Registry has no runtime for the orchestrator. This confirms the validation
// runs in the right order and the error message is informative.
func TestCreate_NoRuntime_ReturnsRuntimeNotFound(t *testing.T) {
	svc := newSvc()
	cfg := validConfig(uuid.New())

	_, err := svc.Create(cfg)
	if err == nil {
		t.Fatal("expected error when orchestrator runtime is not registered")
	}
	if !strings.HasPrefix(err.Error(), "orchestrator runtime not found") {
		t.Fatalf("expected 'orchestrator runtime not found...', got %q", err.Error())
	}
}

// ── Get — not found ───────────────────────────────────────────────────────────

func TestGet_UnknownBot_ReturnsError(t *testing.T) {
	svc := newSvc()
	_, err := svc.Get("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent bot")
	}
}

// ── List / ListByOrchestrator — empty state ───────────────────────────────────

func TestList_EmptyService_ReturnsEmptySlice(t *testing.T) {
	svc := newSvc()
	bots := svc.List()
	if bots == nil {
		t.Fatal("expected non-nil slice, got nil")
	}
	if len(bots) != 0 {
		t.Fatalf("expected 0 bots, got %d", len(bots))
	}
}

func TestListByOrchestrator_EmptyService_ReturnsNilOrEmpty(t *testing.T) {
	svc := newSvc()
	bots := svc.ListByOrchestrator(uuid.New())
	// May be nil or empty — both are acceptable.
	if len(bots) != 0 {
		t.Fatalf("expected 0 bots, got %d", len(bots))
	}
}

// ── RunningBots — empty state ─────────────────────────────────────────────────

func TestRunningBots_EmptyService_ReturnsEmpty(t *testing.T) {
	svc := newSvc()
	running := svc.RunningBots()
	if len(running) != 0 {
		t.Fatalf("expected 0 running bots, got %d", len(running))
	}
}

// ── PurgeBots — no-op for unknown IDs ────────────────────────────────────────

func TestPurgeBots_UnknownIDs_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("PurgeBots panicked: %v", r)
		}
	}()

	svc := newSvc()
	svc.PurgeBots([]string{"ghost-1", "ghost-2", "ghost-3"})
}

func TestPurgeBots_EmptyList_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("PurgeBots panicked on empty list: %v", r)
		}
	}()

	svc := newSvc()
	svc.PurgeBots(nil)
	svc.PurgeBots([]string{})
}

// ── StopBots / StartBots / KillBots — empty map no-ops ───────────────────────

func TestStopBots_UnknownIDs_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("StopBots panicked: %v", r)
		}
	}()
	svc := newSvc()
	svc.StopBots([]string{"ghost-1", "ghost-2"})
}

func TestStartBots_UnknownIDs_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("StartBots panicked: %v", r)
		}
	}()
	svc := newSvc()
	svc.StartBots([]string{"ghost-1", "ghost-2"})
}

func TestKillBots_UnknownIDs_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("KillBots panicked: %v", r)
		}
	}()
	svc := newSvc()
	svc.KillBots([]string{"ghost-1", "ghost-2"})
}
