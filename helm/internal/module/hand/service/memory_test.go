package service

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime"
)

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

type stubExchange struct {
	exchange.Exchange
}

func (s *stubExchange) Name() string { return "stub" }
func (s *stubExchange) PlaceOrder(ctx context.Context, creds exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	return &exchange.OrderResult{
		ID:        "test-order-id",
		Symbol:    req.Symbol,
		Side:      req.Side,
		Status:    "filled",
		Qty:       req.Qty,
		FilledQty: req.Qty,
		FilledAvg: decimal.NewFromFloat(100.0),
	}, nil
}
func (s *stubExchange) ListPositions(ctx context.Context, creds exchange.Credentials) ([]exchange.PositionResult, error) {
	return nil, nil
}

type stubExchFactory struct {
	ex exchange.Exchange
}

func (f *stubExchFactory) New(cfg helmdomain.ExchangeConfig) (exchange.Exchange, error) {
	return f.ex, nil
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

func TestKillAndRelease_FreesMemory(t *testing.T) {
	mockEx := &stubExchange{}
	factory := &stubExchFactory{ex: mockEx}
	reg := runtime.NewRegistry(factory)
	svc := NewService(newStubRepo(), reg)

	helmID := uuid.New()

	hCfg := &helmdomain.Helm{
		ID:        helmID,
		AccountID: uuid.New(),
		UserID:    uuid.New(),
		Status:    "running",
	}
	exchCfg := helmdomain.ExchangeConfig{
		BrokerType: "stub",
	}
	err := reg.Spawn(hCfg, exchCfg)
	if err != nil {
		t.Fatalf("failed to spawn helm runtime: %v", err)
	}

	rt, err := reg.Get(helmID)
	if err != nil {
		t.Fatalf("failed to get helm runtime: %v", err)
	}

	// Create a hand
	cfg := validConfig(helmID)
	cfg.Market = domain.MarketTypeSpot
	summary, err := svc.Create(cfg)
	if err != nil {
		t.Fatalf("failed to create hand: %v", err)
	}

	handID := summary.ID

	// Verify hand is present in the owning runtime.
	if _, _, ok := rt.GetHandEntry(handID.String()); !ok {
		t.Fatal("expected hand to be present in runtime")
	}

	// Call Release
	err = svc.Release(context.Background(), handID, helmID)
	if err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// Verify hand is removed from the runtime.
	if _, _, ok := rt.GetHandEntry(handID.String()); ok {
		t.Fatal("expected hand to be removed from runtime after Release")
	}
}

func TestAllocateCapital_DynamicScale(t *testing.T) {
	mockEx := &stubExchange{}
	factory := &stubExchFactory{ex: mockEx}
	reg := runtime.NewRegistry(factory)
	repo := newStubRepo()
	svc := NewService(repo, reg)

	helmID := uuid.New()
	hCfg := &helmdomain.Helm{
		ID:        helmID,
		AccountID: uuid.New(),
		UserID:    uuid.New(),
		Status:    "running",
	}
	exchCfg := helmdomain.ExchangeConfig{BrokerType: "stub"}
	if err := reg.Spawn(hCfg, exchCfg); err != nil {
		t.Fatalf("failed to spawn: %v", err)
	}

	rt, err := reg.Get(helmID)
	if err != nil {
		t.Fatalf("failed to get runtime: %v", err)
	}

	cfg := validConfig(helmID)
	cfg.Market = domain.MarketTypeSpot
	cfg.AllocatedCapital = decimal.NewFromFloat(1000)
	summary, err := svc.Create(cfg)
	if err != nil {
		t.Fatalf("failed to create hand: %v", err)
	}

	handID := summary.ID

	// Verify initial capital
	if !summary.AllocatedCapital.Equal(decimal.NewFromFloat(1000)) {
		t.Fatalf("expected initial capital 1000, got %v", summary.AllocatedCapital)
	}

	runner, _, ok := rt.GetHandEntry(handID.String())
	if !ok {
		t.Fatal("expected hand runner in runtime")
	}

	// 1. Add capital (positive delta)
	newCap, err := svc.AllocateCapital(handID, helmID, decimal.NewFromFloat(500))
	if err != nil {
		t.Fatalf("AllocateCapital failed: %v", err)
	}
	if !newCap.Equal(decimal.NewFromFloat(1500)) {
		t.Fatalf("expected 1500, got %v", newCap)
	}

	// Verify DB record has updated capital
	dbHand, err := repo.Get(handID)
	if err != nil {
		t.Fatalf("failed to get from DB: %v", err)
	}
	if !dbHand.AllocatedCapital.Equal(decimal.NewFromFloat(1500)) {
		t.Fatalf("expected DB capital 1500, got %v", dbHand.AllocatedCapital)
	}

	// Verify runner is updated
	runnerCap := runner.AllocatedCapital()
	if !runnerCap.Equal(decimal.NewFromFloat(1500)) {
		t.Fatalf("expected runner capital 1500, got %v", runnerCap)
	}

	// 2. Reduce capital (negative delta)
	newCap, err = svc.AllocateCapital(handID, helmID, decimal.NewFromFloat(-700))
	if err != nil {
		t.Fatalf("AllocateCapital failed: %v", err)
	}
	if !newCap.Equal(decimal.NewFromFloat(800)) {
		t.Fatalf("expected 800, got %v", newCap)
	}

	runnerCap = runner.AllocatedCapital()
	if !runnerCap.Equal(decimal.NewFromFloat(800)) {
		t.Fatalf("expected runner capital 800, got %v", runnerCap)
	}

	// 3. Error case: new capital <= 0
	_, err = svc.AllocateCapital(handID, helmID, decimal.NewFromFloat(-800))
	if err == nil {
		t.Fatal("expected error when new capital is <= 0, got nil")
	}

	// Capital remains unchanged (800)
	runnerCap = runner.AllocatedCapital()
	if !runnerCap.Equal(decimal.NewFromFloat(800)) {
		t.Fatalf("expected runner capital 800 after failed reduction, got %v", runnerCap)
	}
}
