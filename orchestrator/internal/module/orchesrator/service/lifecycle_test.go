package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"orchestrator/internal/module/orchesrator/domain"
	orchrepo "orchestrator/internal/module/orchesrator/repo"
	"orchestrator/internal/module/orchesrator/service"
)

// ── mock RuntimeSpawner ──────────────────────────────────────────────────────

type mockSpawner struct {
	spawned          []uuid.UUID
	tornDown         []uuid.UUID
	paused           []uuid.UUID
	resumed          []uuid.UUID
	resetHaltCalled  []uuid.UUID
	updateRiskCalled []uuid.UUID
	syncedOne        []uuid.UUID

	pauseResult  []string // bot IDs returned by Pause
	resumeResult []string // bot IDs returned by Resume
	pauseErr     error
	resumeErr    error
	resetHaltErr error
}

func (m *mockSpawner) Spawn(cfg *domain.OrchestratorConfig) error {
	m.spawned = append(m.spawned, cfg.ID)
	return nil
}

func (m *mockSpawner) Teardown(id uuid.UUID) []string {
	m.tornDown = append(m.tornDown, id)
	return []string{}
}

func (m *mockSpawner) Pause(id uuid.UUID) ([]string, error) {
	m.paused = append(m.paused, id)
	return m.pauseResult, m.pauseErr
}

func (m *mockSpawner) Resume(id uuid.UUID) ([]string, error) {
	m.resumed = append(m.resumed, id)
	return m.resumeResult, m.resumeErr
}

func (m *mockSpawner) ResetHalt(id uuid.UUID) error {
	m.resetHaltCalled = append(m.resetHaltCalled, id)
	return m.resetHaltErr
}

func (m *mockSpawner) UpdateRiskConfig(id uuid.UUID, _ domain.PortfolioConfig, _ domain.RiskConfig) error {
	m.updateRiskCalled = append(m.updateRiskCalled, id)
	return nil
}

func (m *mockSpawner) SyncOne(id uuid.UUID) {
	m.syncedOne = append(m.syncedOne, id)
}

// ── mock BotLifecycle ────────────────────────────────────────────────────────

type mockBotLifecycle struct {
	stopped []string
	started []string
	killed  []string
	purged  []string
}

func (m *mockBotLifecycle) StopBots(ids []string)  { m.stopped = append(m.stopped, ids...) }
func (m *mockBotLifecycle) StartBots(ids []string) { m.started = append(m.started, ids...) }
func (m *mockBotLifecycle) KillBots(ids []string)  { m.killed = append(m.killed, ids...) }
func (m *mockBotLifecycle) PurgeBots(ids []string) { m.purged = append(m.purged, ids...) }

// ── test helpers ─────────────────────────────────────────────────────────────

func newOrch(id uuid.UUID, status string) *domain.OrchestratorConfig {
	return &domain.OrchestratorConfig{
		ID:        id,
		UserID:    uuid.New(),
		AccountID: uuid.New(),
		Name:      "test-orch",
		Capital:   10_000,
		Enabled:   true,
		Status:    status,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func setup() (*service.Service, *mockSpawner, *mockBotLifecycle, *orchrepo.MemoryOrchestratorStore) {
	store := orchrepo.NewMemoryOrchestratorStore()
	spawner := &mockSpawner{}
	bots := &mockBotLifecycle{}
	svc := service.New(store, spawner)
	svc.SetBotLifecycle(bots)
	return svc, spawner, bots, store
}

// ── Enable / Disable ─────────────────────────────────────────────────────────

func TestEnable_SetsEnabledTrue(t *testing.T) {
	svc, spawner, _, store := setup()
	id := uuid.New()
	orch := newOrch(id, "active")
	orch.Enabled = false
	_ = store.Save(orch)

	if err := svc.Enable(id); err != nil {
		t.Fatalf("Enable returned error: %v", err)
	}

	got, _ := store.Get(id)
	if !got.Enabled {
		t.Fatal("expected Enabled=true after Enable()")
	}
	// SyncOne should be fired.
	if len(spawner.syncedOne) != 1 || spawner.syncedOne[0] != id {
		t.Fatalf("expected SyncOne to be called with %v, got %v", id, spawner.syncedOne)
	}
}

func TestDisable_SetsEnabledFalse(t *testing.T) {
	svc, _, _, store := setup()
	id := uuid.New()
	orch := newOrch(id, "active")
	orch.Enabled = true
	_ = store.Save(orch)

	if err := svc.Disable(id); err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}

	got, _ := store.Get(id)
	if got.Enabled {
		t.Fatal("expected Enabled=false after Disable()")
	}
}

func TestEnable_UnknownOrch_ReturnsError(t *testing.T) {
	svc, _, _, _ := setup()
	err := svc.Enable(uuid.New())
	if err == nil {
		t.Fatal("expected error for unknown orchestrator")
	}
}

// ── Pause ─────────────────────────────────────────────────────────────────────

func TestPause_PersistsStatusAndStopsBots(t *testing.T) {
	svc, spawner, bots, store := setup()
	id := uuid.New()
	_ = store.Save(newOrch(id, "active"))

	spawner.pauseResult = []string{"bot-a", "bot-b"}

	if err := svc.Pause(id); err != nil {
		t.Fatalf("Pause returned error: %v", err)
	}

	// Status persisted.
	got, _ := store.Get(id)
	if got.Status != "paused" {
		t.Fatalf("expected status 'paused', got %q", got.Status)
	}
	// StopBots called with running bots.
	if len(bots.stopped) != 2 {
		t.Fatalf("expected 2 bots stopped, got %d", len(bots.stopped))
	}
}

func TestPause_NoBots_DoesNotCallStopBots(t *testing.T) {
	svc, spawner, bots, store := setup()
	id := uuid.New()
	_ = store.Save(newOrch(id, "active"))

	spawner.pauseResult = nil // no running bots

	_ = svc.Pause(id)

	if len(bots.stopped) != 0 {
		t.Fatalf("StopBots should not be called when no running bots, got %v", bots.stopped)
	}
}

func TestPause_SpawnerError_Propagated(t *testing.T) {
	svc, spawner, _, store := setup()
	id := uuid.New()
	_ = store.Save(newOrch(id, "active"))

	spawner.pauseErr = errTest("spawner pause failed")

	err := svc.Pause(id)
	if err == nil {
		t.Fatal("expected error from spawner.Pause to propagate")
	}
}

// ── Resume ────────────────────────────────────────────────────────────────────

func TestResume_PersistsStatusAndStartsBots(t *testing.T) {
	svc, spawner, bots, store := setup()
	id := uuid.New()
	_ = store.Save(newOrch(id, "paused"))

	spawner.resumeResult = []string{"bot-a", "bot-b", "bot-c"}

	if err := svc.Resume(id); err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}

	got, _ := store.Get(id)
	if got.Status != "active" {
		t.Fatalf("expected status 'active', got %q", got.Status)
	}
	if len(bots.started) != 3 {
		t.Fatalf("expected 3 bots started, got %d", len(bots.started))
	}
}

func TestResume_NoBots_DoesNotCallStartBots(t *testing.T) {
	svc, spawner, bots, store := setup()
	id := uuid.New()
	_ = store.Save(newOrch(id, "paused"))

	spawner.resumeResult = nil

	_ = svc.Resume(id)

	if len(bots.started) != 0 {
		t.Fatalf("StartBots should not be called when no bots to restart, got %v", bots.started)
	}
}

// ── Kill ──────────────────────────────────────────────────────────────────────

func TestKill_PersistsHaltedAndKillsBots(t *testing.T) {
	svc, spawner, bots, store := setup()
	id := uuid.New()
	_ = store.Save(newOrch(id, "active"))

	spawner.pauseResult = []string{"bot-x", "bot-y"}

	if err := svc.Kill(context.Background(), id); err != nil {
		t.Fatalf("Kill returned error: %v", err)
	}

	got, _ := store.Get(id)
	if got.Status != "halted" {
		t.Fatalf("expected status 'halted', got %q", got.Status)
	}
	// KillBots called.
	if len(bots.killed) != 2 {
		t.Fatalf("expected 2 bots killed, got %d", len(bots.killed))
	}
}

func TestKill_PauseCalledFirst(t *testing.T) {
	svc, spawner, _, store := setup()
	id := uuid.New()
	_ = store.Save(newOrch(id, "active"))

	_ = svc.Kill(context.Background(), id)

	if len(spawner.paused) == 0 {
		t.Fatal("Kill should call spawner.Pause before killing bots")
	}
}

// ── ResetHalt ─────────────────────────────────────────────────────────────────

func TestResetHalt_PersistsActiveAndCallsSpawner(t *testing.T) {
	svc, spawner, _, store := setup()
	id := uuid.New()
	_ = store.Save(newOrch(id, "halted"))

	if err := svc.ResetHalt(id); err != nil {
		t.Fatalf("ResetHalt returned error: %v", err)
	}

	got, _ := store.Get(id)
	if got.Status != "active" {
		t.Fatalf("expected status 'active' after ResetHalt, got %q", got.Status)
	}
	if len(spawner.resetHaltCalled) != 1 || spawner.resetHaltCalled[0] != id {
		t.Fatalf("expected spawner.ResetHalt called with %v, got %v", id, spawner.resetHaltCalled)
	}
}

func TestResetHalt_SpawnerError_Propagated(t *testing.T) {
	svc, spawner, _, store := setup()
	id := uuid.New()
	_ = store.Save(newOrch(id, "halted"))

	spawner.resetHaltErr = errTest("spawner reset failed")

	err := svc.ResetHalt(id)
	if err == nil {
		t.Fatal("expected spawner.ResetHalt error to propagate")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

type errTest string

func (e errTest) Error() string { return string(e) }
