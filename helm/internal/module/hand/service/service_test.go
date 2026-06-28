package service_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/module/hand/service"
	"mallow/helm/internal/runtime"
)

// ── in-process stub repo (replaces deleted memory store) ─────────────────────

type stubHandRepo struct {
	mu   sync.RWMutex
	rows map[uuid.UUID]*domain.Hand
}

func newStubRepo() *stubHandRepo { return &stubHandRepo{rows: map[uuid.UUID]*domain.Hand{}} }

func (r *stubHandRepo) GenerateID() uuid.UUID { return uuid.New() }
func (r *stubHandRepo) Save(d *domain.Hand) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *d
	r.rows[d.ID] = &cp
	return nil
}
func (r *stubHandRepo) Get(id uuid.UUID) (*domain.Hand, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if v, ok := r.rows[id]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, fmt.Errorf("hand %q not found", id)
}
func (r *stubHandRepo) All() []*domain.Hand {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*domain.Hand, 0, len(r.rows))
	for _, v := range r.rows {
		cp := *v
		out = append(out, &cp)
	}
	return out
}
func (r *stubHandRepo) AllByHelm(helmID uuid.UUID) []*domain.Hand {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*domain.Hand
	for _, v := range r.rows {
		if v.HelmID == helmID {
			cp := *v
			out = append(out, &cp)
		}
	}
	return out
}
func (r *stubHandRepo) AllByHelms(helmIDs []uuid.UUID) []*domain.Hand {
	r.mu.RLock()
	defer r.mu.RUnlock()
	want := make(map[uuid.UUID]struct{}, len(helmIDs))
	for _, id := range helmIDs {
		want[id] = struct{}{}
	}
	var out []*domain.Hand
	for _, v := range r.rows {
		if _, ok := want[v.HelmID]; ok {
			cp := *v
			out = append(out, &cp)
		}
	}
	return out
}
func (r *stubHandRepo) Update(id uuid.UUID, fn func(*domain.Hand) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.rows[id]
	if !ok {
		return fmt.Errorf("hand %q not found", id)
	}
	return fn(v)
}
func (r *stubHandRepo) DeleteByHelm(helmID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, h := range r.rows {
		if h.HelmID == helmID {
			delete(r.rows, id)
		}
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func newSvc() *service.Service {
	reg := runtime.NewRegistry(nil)
	return service.NewService(newStubRepo(), reg)
}

func validConfig(helmID uuid.UUID) domain.HandConfig {
	return domain.HandConfig{
		Name:             "test-hand",
		HelmID:           helmID,
		Symbols:          []string{"AAPL"},
		Strategy:         domain.StrategySpec{Script: "let rsi = ind.RSI(14); if rsi[0] < 30 { \"long\" } else { \"\" }"},
		AllocatedCapital: decimal.NewFromFloat(1000),
	}
}

// ── Create — input validation ─────────────────────────────────────────────────

func TestCreate_ZeroAllocatedCapital_ReturnsError(t *testing.T) {
	svc := newSvc()
	cfg := validConfig(uuid.New())
	cfg.AllocatedCapital = decimal.Zero

	_, err := svc.Create(cfg)
	if err == nil {
		t.Fatal("expected error for zero allocated capital")
	}
	if err.Error() != "allocated capital must be greater than zero" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreate_EmptyName_ReturnsError(t *testing.T) {
	svc := newSvc()
	cfg := validConfig(uuid.New())
	cfg.Name = ""

	_, err := svc.Create(cfg)
	if err == nil {
		t.Fatal("expected error for empty hand name")
	}
	if err.Error() != "hand name is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreate_ZeroHelmID_ReturnsError(t *testing.T) {
	svc := newSvc()
	cfg := validConfig(uuid.Nil)

	_, err := svc.Create(cfg)
	if err == nil {
		t.Fatal("expected error for zero helm_id")
	}
	if err.Error() != "helm_id is required" {
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

	testCases := []struct {
		name   string
		cfg    domain.HandConfig
		errMsg string
	}{
		{
			name:   "empty name",
			cfg:    domain.HandConfig{HelmID: uuid.New(), Symbols: []string{"X"}},
			errMsg: "hand name is required",
		},
		{
			name:   "zero helm id",
			cfg:    domain.HandConfig{Name: "b", Symbols: []string{"X"}},
			errMsg: "helm_id is required",
		},
		{
			name:   "no symbols",
			cfg:    domain.HandConfig{Name: "b", HelmID: uuid.New()},
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
// Registry has no runtime for the helm. Confirms validation order is correct.
func TestCreate_NoRuntime_ReturnsRuntimeNotFound(t *testing.T) {
	svc := newSvc()
	cfg := validConfig(uuid.New())

	_, err := svc.Create(cfg)
	if err == nil {
		t.Fatal("expected error when helm runtime is not registered")
	}
	if !strings.HasPrefix(err.Error(), "helm runtime not found") {
		t.Fatalf("expected 'helm runtime not found...', got %q", err.Error())
	}
}

// ── Get — not found ───────────────────────────────────────────────────────────

func TestGet_UnknownHand_ReturnsError(t *testing.T) {
	svc := newSvc()
	_, err := svc.Get(uuid.New())
	if err == nil {
		t.Fatal("expected error for nonexistent hand")
	}
}

// ── List / ListByHelm — empty state ──────────────────────────────────────────

func TestList_EmptyService_ReturnsEmptySlice(t *testing.T) {
	svc := newSvc()
	hands := svc.List()
	if hands == nil {
		t.Fatal("expected non-nil slice, got nil")
	}
	if len(hands) != 0 {
		t.Fatalf("expected 0 hands, got %d", len(hands))
	}
}

func TestListByHelm_EmptyService_ReturnsNilOrEmpty(t *testing.T) {
	svc := newSvc()
	hands := svc.ListByHelm(uuid.New())
	if len(hands) != 0 {
		t.Fatalf("expected 0 hands, got %d", len(hands))
	}
}

// ── RunningHandCount — empty state ─────────────────────────────────────────────

func TestRunningHandCount_EmptyService_ReturnsZero(t *testing.T) {
	svc := newSvc()
	if n := svc.RunningHandCount(); n != 0 {
		t.Fatalf("expected 0 running hands, got %d", n)
	}
}

// ── StopBots / StartBots / KillBots — unknown helm no-ops ─────────────────────

func TestStopBots_UnknownHelm_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("StopBots panicked: %v", r)
		}
	}()
	svc := newSvc()
	svc.StopBots(uuid.New(), []string{"ghost-1", "ghost-2"})
}

func TestStartBots_UnknownHelm_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("StartBots panicked: %v", r)
		}
	}()
	svc := newSvc()
	svc.StartBots(uuid.New(), []string{"ghost-1", "ghost-2"})
}

func TestKillBots_UnknownHelm_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("KillBots panicked: %v", r)
		}
	}()
	svc := newSvc()
	svc.KillBots(uuid.New(), []string{"ghost-1", "ghost-2"})
}
