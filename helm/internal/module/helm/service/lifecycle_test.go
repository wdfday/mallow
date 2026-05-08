package service_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/module/helm/service"
)

// ── inline stub HelmRepo (replaces deleted memory store) ────────────────────

type stubHelmRepo struct {
	mu   sync.RWMutex
	rows map[uuid.UUID]*domain.HelmConfig
}

func newStubHelmRepo() *stubHelmRepo {
	return &stubHelmRepo{rows: map[uuid.UUID]*domain.HelmConfig{}}
}

func (r *stubHelmRepo) Save(o *domain.HelmConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *o
	r.rows[o.ID] = &cp
	return nil
}
func (r *stubHelmRepo) Get(id uuid.UUID) (*domain.HelmConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if v, ok := r.rows[id]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, fmt.Errorf("helm %v not found", id)
}
func (r *stubHelmRepo) GetByAccountID(accountID uuid.UUID) (*domain.HelmConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.rows {
		if v.AccountID == accountID {
			cp := *v
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("helm for account %v not found", accountID)
}
func (r *stubHelmRepo) All() ([]*domain.HelmConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*domain.HelmConfig, 0, len(r.rows))
	for _, v := range r.rows {
		cp := *v
		out = append(out, &cp)
	}
	return out, nil
}
func (r *stubHelmRepo) AllByUser(userID uuid.UUID) ([]*domain.HelmConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*domain.HelmConfig
	for _, v := range r.rows {
		if v.UserID == userID {
			cp := *v
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (r *stubHelmRepo) Update(id uuid.UUID, fn func(*domain.HelmConfig) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.rows[id]
	if !ok {
		return fmt.Errorf("helm %v not found", id)
	}
	return fn(v)
}
func (r *stubHelmRepo) UpdateLastSyncedAt(id uuid.UUID, t time.Time) error {
	return r.Update(id, func(c *domain.HelmConfig) error {
		c.UpdatedAt = t
		return nil
	})
}
func (r *stubHelmRepo) Delete(id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rows[id]; !ok {
		return fmt.Errorf("helm %v not found", id)
	}
	delete(r.rows, id)
	return nil
}

// ── mock RuntimeSpawner ──────────────────────────────────────────────────────

type mockSpawner struct {
	spawned          []uuid.UUID
	tornDown         []uuid.UUID
	paused           []uuid.UUID
	resumed          []uuid.UUID
	resetHaltCalled  []uuid.UUID
	updateRiskCalled []uuid.UUID
	syncedOne        []uuid.UUID

	pauseResult  []string // hand IDs returned by Pause
	resumeResult []string // hand IDs returned by Resume
	pauseErr     error
	resumeErr    error
	resetHaltErr error
}

func (m *mockSpawner) Spawn(cfg *domain.HelmConfig) error {
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

func newOrch(id uuid.UUID, status string) *domain.HelmConfig {
	return &domain.HelmConfig{
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

func setup() (*service.Service, *mockSpawner, *mockBotLifecycle, *stubHelmRepo) {
	store := newStubHelmRepo()
	spawner := &mockSpawner{}
	bots := &mockBotLifecycle{}
	svc := service.New(store, spawner)
	svc.SetHandLifecycle(bots)
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
		t.Fatalf("expected 2 hands stopped, got %d", len(bots.stopped))
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
		t.Fatalf("expected 3 hands started, got %d", len(bots.started))
	}
}

func TestResume_NoBots_DoesNotCallStartBots(t *testing.T) {
	svc, spawner, bots, store := setup()
	id := uuid.New()
	_ = store.Save(newOrch(id, "paused"))

	spawner.resumeResult = nil

	_ = svc.Resume(id)

	if len(bots.started) != 0 {
		t.Fatalf("StartBots should not be called when no hands to restart, got %v", bots.started)
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
		t.Fatalf("expected 2 hands killed, got %d", len(bots.killed))
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
