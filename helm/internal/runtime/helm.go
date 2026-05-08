package runtime

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/infra/poslog"
	orchdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/core/orderbook"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

// RiskManager is the interface for account-level risk controls.
type RiskManager interface {
	IsHalted() bool
	ResetHalt()
	UpdateConfig(cfg risk.Config)
}

// HelmRuntime is the live in-memory state for one helm instance.
// Holds account-level shared resources: Exchange, Portfolio, OrderBook, RiskManager.
// Per-hand resources (strategy, tactician) live on the Hand itself.
type HelmRuntime struct {
	HelmID     uuid.UUID
	AccountID  uuid.UUID
	UserID     uuid.UUID
	BrokerType string

	Portfolio *portfolio.Portfolio
	RiskMgr   RiskManager
	OrderBook orderbook.OrderBook
	Exchange  exchange.Exchange
	Creds     exchange.Credentials // per-account credentials passed to all Exchange calls

	// orderCh decouples the broker WS goroutine from NATS publishing.
	orderCh chan exchange.OrderEvent

	mu     sync.RWMutex
	bots   map[string]*Hand
	paused bool

	// lastSyncAtNano stores the last successful sync time as UnixNano (0 = never).
	lastSyncAtNano atomic.Int64

	pricesMu sync.RWMutex
	prices   map[string]decimal.Decimal // last known price per symbol

	l2Mu    sync.RWMutex
	l2Books map[string]exchange.L2Snapshot // latest books5 snapshot per symbol

	tradeMu      sync.Mutex
	requestCount atomic.Int64
	resetTicker  *time.Ticker

	// PosLog is the durable position event log. nil = NATS unavailable (dev/test).
	PosLog poslog.Log
}

// LastSyncAt returns the timestamp of the most recent portfolio sync, or zero if never synced.
func (r *HelmRuntime) LastSyncAt() time.Time {
	ns := r.lastSyncAtNano.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

func (r *HelmRuntime) storeSyncAt(t time.Time) {
	ns := t.UnixNano()
	for {
		cur := r.lastSyncAtNano.Load()
		if ns <= cur {
			return
		}
		if r.lastSyncAtNano.CompareAndSwap(cur, ns) {
			return
		}
	}
}

// Sync fetches current account state from the exchange REST API, updates the in-memory
// portfolio, and publishes a portfolio.synced event to NATS for the investment service.
func (r *HelmRuntime) Sync(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext) error {
	syncer, ok := r.Exchange.(exchange.AccountSyncer)
	if !ok {
		return nil
	}
	var since *time.Time
	if prev := r.LastSyncAt(); !prev.IsZero() {
		since = &prev
	}
	snap, err := syncer.SyncAccount(ctx, r.Creds, since)
	if err != nil {
		return err
	}

	pfPositions := make([]portfolio.SyncedPosition, len(snap.Positions))
	natsPositions := make([]natsapi.SyncedPositionMsg, len(snap.Positions))
	for i, p := range snap.Positions {
		pfPositions[i] = portfolio.SyncedPosition{
			Symbol:   p.Symbol,
			Qty:      p.Qty,
			AvgPrice: p.AvgPrice,
			CurPrice: p.CurPrice,
		}
		natsPositions[i] = natsapi.SyncedPositionMsg{
			Symbol:   p.Symbol,
			Qty:      p.Qty,
			AvgPrice: p.AvgPrice,
			CurPrice: p.CurPrice,
		}
	}
	r.Portfolio.ApplySync(snap.Cash, pfPositions)

	r.pricesMu.Lock()
	for _, p := range snap.Positions {
		if p.CurPrice.IsPositive() {
			r.prices[p.Symbol] = p.CurPrice
		}
	}
	r.pricesMu.Unlock()

	prevSyncAt := r.LastSyncAt()

	var newTxns []natsapi.TransactionMsg
	natsTxns := make([]natsapi.TransactionMsg, 0, len(snap.Transactions))
	for _, t := range snap.Transactions {
		msg := natsapi.TransactionMsg{
			TradeID:  t.TradeID,
			OrderID:  t.OrderID,
			Kind:     "fill",
			Symbol:   t.Symbol,
			Side:     t.Side,
			Qty:      t.Qty,
			AvgPrice: t.AvgPrice,
			Fee:      t.Fee,
			FilledAt: t.FilledAt,
		}
		natsTxns = append(natsTxns, msg)
		if prevSyncAt.IsZero() || t.FilledAt.After(prevSyncAt) {
			newTxns = append(newTxns, msg)
		}
	}

	now := time.Now().UTC()
	r.storeSyncAt(now)

	slog.Info("runtime: synced from exchange",
		"helm_id", r.HelmID,
		"positions", len(pfPositions),
		"transactions", len(natsTxns),
		"new_transactions", len(newTxns),
	)

	if nc != nil {
		natsapi.PublishPortfolioSync(nc, r.HelmID.String(), r.AccountID.String(), snap.Cash, snap.Equity, natsPositions, natsTxns, now)

		if js != nil && len(newTxns) > 0 {
			orchID := r.HelmID.String()
			accountID := r.AccountID.String()
			userID := r.UserID.String()
			for _, t := range newTxns {
				natsapi.PublishInvestmentTransaction(js, orchID, accountID, userID, "", r.BrokerType, t)
			}
		}
	}
	return nil
}

// NewHelmRuntime creates a HelmRuntime and starts its circuit-breaker reset ticker.
func NewHelmRuntime(
	orchID, accountID, userID uuid.UUID,
	brokerType string,
	pf *portfolio.Portfolio,
	riskMgr RiskManager,
	ob orderbook.OrderBook,
	ex exchange.Exchange,
	creds exchange.Credentials,
	lastSyncedAt *time.Time,
) *HelmRuntime {
	rt := &HelmRuntime{
		HelmID:      orchID,
		AccountID:   accountID,
		UserID:      userID,
		BrokerType:  brokerType,
		Portfolio:   pf,
		RiskMgr:     riskMgr,
		OrderBook:   ob,
		Exchange:    ex,
		Creds:       creds,
		orderCh:     make(chan exchange.OrderEvent, 128),
		bots:        make(map[string]*Hand),
		prices:      make(map[string]decimal.Decimal),
		l2Books:     make(map[string]exchange.L2Snapshot),
		resetTicker: time.NewTicker(1 * time.Minute),
	}
	if lastSyncedAt != nil {
		rt.lastSyncAtNano.Store(lastSyncedAt.UnixNano())
	}
	go func() {
		for range rt.resetTicker.C {
			rt.requestCount.Store(0)
		}
	}()
	return rt
}

// TradeProposal is a bot's request for account-level trade validation.
type TradeProposal struct {
	BotID  string
	Symbol string
	Intent strategy.Intent
	Price  decimal.Decimal // optional: resolved from price cache when zero
	ATR    decimal.Decimal
}

// ProcessTrade validates a trade against account-level guards and sizes via the bot's tactician.
func (r *HelmRuntime) ProcessTrade(
	ctx context.Context,
	proposal TradeProposal,
	tact *tactics.Tactician,
) orchdomain.TradeReply {
	if proposal.Intent.Action == strategy.ActionDoNothing {
		return orchdomain.TradeReply{Approved: false, Reason: "strategy: do_nothing"}
	}

	count := r.requestCount.Add(1)
	if count > 100 {
		return orchdomain.TradeReply{Approved: false, Reason: "circuit breaker: too many requests"}
	}

	r.tradeMu.Lock()
	defer r.tradeMu.Unlock()

	if r.RiskMgr.IsHalted() {
		return orchdomain.TradeReply{Approved: false, Reason: "risk: trading halted"}
	}

	price := proposal.Price
	if price.IsZero() {
		price = r.lastKnownPrice(proposal.Symbol)
	}
	if price.IsZero() {
		if pf, ok := r.Exchange.(exchange.PriceFetcher); ok {
			if p, err := pf.GetCurrentPrice(ctx, r.Creds, proposal.Symbol); err == nil && p.IsPositive() {
				price = p
				r.pricesMu.Lock()
				r.prices[proposal.Symbol] = p
				r.pricesMu.Unlock()
			}
		}
	}
	if price.IsZero() {
		return orchdomain.TradeReply{Approved: false, Reason: "no price available for " + proposal.Symbol}
	}

	posQty := decimal.Zero
	if pos := r.Portfolio.GetPosition(proposal.Symbol); pos != nil {
		posQty = pos.Qty
	}
	tact.UpdateEquity(r.Portfolio.Equity())
	plan := tact.Plan(proposal.Intent, tactics.MarketContext{
		Price:       price,
		ATR:         proposal.ATR,
		PositionQty: posQty,
	})

	if !plan.Qty.IsPositive() {
		return orchdomain.TradeReply{Approved: false, Reason: "tactics: zero quantity after sizing"}
	}

	slog.Info("runtime: trade approved",
		"hand_id", proposal.BotID,
		"symbol", proposal.Symbol,
		"action", proposal.Intent.Action,
		"side", plan.Side,
		"qty", plan.Qty,
		"price", price,
	)

	return orchdomain.TradeReply{
		Approved:     true,
		Qty:          plan.Qty,
		Side:         plan.Side,
		EntryType:    string(plan.EntryType),
		LimitPrice:   plan.LimitPrice,
		StopLoss:     plan.StopLoss,
		TakeProfit:   plan.TakeProfit,
		TrailingStop: plan.TrailingStop,
	}
}

// TrackOrder records a placed order in the orderbook.
func (r *HelmRuntime) TrackOrder(order orderbook.PendingOrder) {
	r.OrderBook.TrackOrder(order)
}

// ReportFill applies a fill to the portfolio and removes the order from orderbook.
func (r *HelmRuntime) ReportFill(fill orchdomain.FillReport) {
	r.tradeMu.Lock()
	defer r.tradeMu.Unlock()

	r.OrderBook.RemoveOrder(fill.OrchestratorID, fill.OrderID)

	if fill.Price.IsPositive() {
		r.pricesMu.Lock()
		r.prices[fill.Symbol] = fill.Price
		r.pricesMu.Unlock()
	}

	pfSide := portfolio.SideBuy
	if fill.Side == "sell" {
		pfSide = portfolio.SideSell
	}

	r.Portfolio.ApplyFill(portfolio.Fill{
		Timestamp:  fill.Timestamp,
		Symbol:     fill.Symbol,
		Side:       pfSide,
		Qty:        fill.Qty,
		Price:      fill.Price,
		Commission: decimal.Zero,
	})

	slog.Info("runtime: fill applied",
		"hand_id", fill.BotID,
		"symbol", fill.Symbol,
		"side", fill.Side,
		"qty", fill.Qty,
		"price", fill.Price,
	)
}

// UpdatePrice stores the latest market price for a symbol and forwards it to the portfolio.
func (r *HelmRuntime) UpdatePrice(symbol string, price decimal.Decimal) {
	if !price.IsPositive() {
		return
	}
	r.pricesMu.Lock()
	r.prices[symbol] = price
	r.pricesMu.Unlock()
	r.Portfolio.UpdatePrice(symbol, price)
}

// UpdateL2 caches the latest books5 snapshot and pushes it to running bots
// watching that symbol. Called from the shared OKX market streamer goroutine —
// must not block; OnL2 on each hand must be fast.
func (r *HelmRuntime) UpdateL2(snap exchange.L2Snapshot) {
	r.l2Mu.Lock()
	r.l2Books[snap.Symbol] = snap
	r.l2Mu.Unlock()

	r.mu.RLock()
	for _, hand := range r.bots {
		if hand.Symbol == snap.Symbol && hand.IsRunning() {
			hand.OnL2(snap)
		}
	}
	r.mu.RUnlock()
}

// LatestL2 returns the most recent books5 snapshot for a symbol.
// ok=false if no snapshot has been received yet.
func (r *HelmRuntime) LatestL2(symbol string) (exchange.L2Snapshot, bool) {
	r.l2Mu.RLock()
	s, ok := r.l2Books[symbol]
	r.l2Mu.RUnlock()
	return s, ok
}

func (r *HelmRuntime) lastKnownPrice(symbol string) decimal.Decimal {
	r.pricesMu.RLock()
	p := r.prices[symbol]
	r.pricesMu.RUnlock()
	if p.IsPositive() {
		return p
	}
	if pos := r.Portfolio.GetPosition(symbol); pos != nil {
		return pos.CurrentPrice
	}
	return decimal.Zero
}

// EnqueueOrderEvent drops a broker order event into the runtime's channel non-blocking.
func (r *HelmRuntime) EnqueueOrderEvent(ev exchange.OrderEvent) {
	select {
	case r.orderCh <- ev:
	default:
		slog.Error("order channel full, dropping event",
			"helm_id", r.HelmID,
			"type", ev.Type,
			"order_id", ev.OrderID,
			"symbol", ev.Symbol,
		)
	}
}

// Stop cleans up the circuit-breaker ticker.
func (r *HelmRuntime) Stop() {
	if r.resetTicker != nil {
		r.resetTicker.Stop()
	}
}

func (r *HelmRuntime) IsPaused() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.paused
}

// Pause suspends the runtime — all hands will ignore incoming signals.
func (r *HelmRuntime) Pause() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = true
	var wasRunning []string
	for id, hand := range r.bots {
		hand.WasRunning = hand.IsRunning()
		if hand.WasRunning {
			wasRunning = append(wasRunning, id)
		}
	}
	return wasRunning
}

// Resume unpauses the runtime and returns IDs of hands that should be restarted.
func (r *HelmRuntime) Resume() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = false
	var toRestart []string
	for id, hand := range r.bots {
		if hand.WasRunning {
			hand.WasRunning = false
			toRestart = append(toRestart, id)
		}
	}
	return toRestart
}

// ResetHalt clears the risk-manager halt flag on this runtime.
func (r *HelmRuntime) ResetHalt() {
	r.RiskMgr.ResetHalt()
}

// UpdateRiskConfig replaces the live risk parameters (portfolio + risk sliders).
func (r *HelmRuntime) UpdateRiskConfig(cfg risk.Config) {
	r.RiskMgr.UpdateConfig(cfg)
}

// AddBot registers a hand with this runtime.
func (r *HelmRuntime) AddHand(hand *Hand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bots[hand.id.String()] = hand
}

func (r *HelmRuntime) RemoveHand(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bots, id)
}

func (r *HelmRuntime) BotIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.bots))
	for id := range r.bots {
		ids = append(ids, id)
	}
	return ids
}

func (r *HelmRuntime) RunningBotIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var ids []string
	for id, hand := range r.bots {
		if hand.IsRunning() {
			ids = append(ids, id)
		}
	}
	return ids
}

// DispatchBotSignal routes a signal to the named hand owned by this orchestrator.
// Returns false if the hand is not found. Logs and skips if the hand is paused.
func (r *HelmRuntime) DispatchHandSignal(handID string, sig Signal) bool {
	r.mu.RLock()
	hand, ok := r.bots[handID]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	if hand.IsPaused() {
		slog.Debug("orchestrator: hand paused, signal skipped",
			"hand_id", handID, "symbol", sig.Symbol, "direction", sig.Direction)
		return true
	}
	hand.DeliverSignal(sig)
	return true
}
