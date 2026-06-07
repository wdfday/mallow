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
func (r *stubHandRepo) Update(id uuid.UUID, fn func(*domain.Hand) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.rows[id]
	if !ok {
		return fmt.Errorf("hand %q not found", id)
	}
	return fn(v)
}
func (r *stubHandRepo) Delete(id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rows[id]; !ok {
		return fmt.Errorf("hand %q not found", id)
	}
	delete(r.rows, id)
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
	svc := NewService(newStubRepo(), reg, nil)

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
	ref, err := svc.Create(cfg)
	if err != nil {
		t.Fatalf("failed to create hand: %v", err)
	}

	handID := ref.Data.ID

	// Verify hand is in service cache and runtime hands list
	svc.mu.RLock()
	_, inCache := svc.hands[handID]
	svc.mu.RUnlock()
	if !inCache {
		t.Fatal("expected hand to be present in service cache")
	}

	rtHands := rt.HandIDs()
	found := false
	for _, id := range rtHands {
		if id == handID.String() {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected hand to be present in runtime registry")
	}

	// Call Release
	err = svc.Release(context.Background(), handID)
	if err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// Verify hand is removed from service cache
	svc.mu.RLock()
	_, inCache = svc.hands[handID]
	svc.mu.RUnlock()
	if inCache {
		t.Fatal("expected hand to be removed from service cache after Release")
	}

	// Verify hand is removed from runtime registry
	rtHands = rt.HandIDs()
	for _, id := range rtHands {
		if id == handID.String() {
			t.Fatal("expected hand to be removed from runtime registry after Release")
		}
	}

	// Re-create the hand to test Kill
	ref, err = svc.Create(cfg)
	if err != nil {
		t.Fatalf("failed to re-create hand: %v", err)
	}
	handID2 := ref.Data.ID

	// Verify hand2 is in cache
	svc.mu.RLock()
	_, inCache = svc.hands[handID2]
	svc.mu.RUnlock()
	if !inCache {
		t.Fatal("expected hand2 to be present in service cache")
	}

	// Call Kill
	err = svc.Kill(context.Background(), handID2)
	if err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	// Verify hand2 is removed from service cache
	svc.mu.RLock()
	_, inCache = svc.hands[handID2]
	svc.mu.RUnlock()
	if inCache {
		t.Fatal("expected hand2 to be removed from service cache after Kill")
	}

	// Verify hand2 is removed from runtime registry
	rtHands = rt.HandIDs()
	for _, id := range rtHands {
		if id == handID2.String() {
			t.Fatal("expected hand2 to be removed from runtime registry after Kill")
		}
	}
}

func TestAllocateCapital_DynamicScale(t *testing.T) {
	mockEx := &stubExchange{}
	factory := &stubExchFactory{ex: mockEx}
	reg := runtime.NewRegistry(factory)
	repo := newStubRepo()
	svc := NewService(repo, reg, nil)

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

	cfg := validConfig(helmID)
	cfg.Market = domain.MarketTypeSpot
	cfg.AllocatedCapital = decimal.NewFromFloat(1000)
	ref, err := svc.Create(cfg)
	if err != nil {
		t.Fatalf("failed to create hand: %v", err)
	}

	handID := ref.Data.ID

	// Verify initial capital
	if !ref.Data.AllocatedCapital.Equal(decimal.NewFromFloat(1000)) {
		t.Fatalf("expected initial capital 1000, got %v", ref.Data.AllocatedCapital)
	}

	// 1. Add capital (positive delta)
	newCap, err := svc.AllocateCapital(handID, decimal.NewFromFloat(500))
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

	// Verify service cache has updated capital
	svc.mu.RLock()
	cacheHand := svc.hands[handID]
	svc.mu.RUnlock()
	if !cacheHand.Data.AllocatedCapital.Equal(decimal.NewFromFloat(1500)) {
		t.Fatalf("expected service cache capital 1500, got %v", cacheHand.Data.AllocatedCapital)
	}

	// Verify runner is updated
	runnerCap := cacheHand.Runner.AllocatedCapital()
	if !runnerCap.Equal(decimal.NewFromFloat(1500)) {
		t.Fatalf("expected runner capital 1500, got %v", runnerCap)
	}

	// 2. Reduce capital (negative delta)
	newCap, err = svc.AllocateCapital(handID, decimal.NewFromFloat(-700))
	if err != nil {
		t.Fatalf("AllocateCapital failed: %v", err)
	}
	if !newCap.Equal(decimal.NewFromFloat(800)) {
		t.Fatalf("expected 800, got %v", newCap)
	}

	runnerCap = cacheHand.Runner.AllocatedCapital()
	if !runnerCap.Equal(decimal.NewFromFloat(800)) {
		t.Fatalf("expected runner capital 800, got %v", runnerCap)
	}

	// 3. Error case: new capital <= 0
	_, err = svc.AllocateCapital(handID, decimal.NewFromFloat(-800))
	if err == nil {
		t.Fatal("expected error when new capital is <= 0, got nil")
	}

	// Capital remains unchanged (800)
	runnerCap = cacheHand.Runner.AllocatedCapital()
	if !runnerCap.Equal(decimal.NewFromFloat(800)) {
		t.Fatalf("expected runner capital 800 after failed reduction, got %v", runnerCap)
	}
}
