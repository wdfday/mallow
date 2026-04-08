package runtime

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"orchestrator/internal/infra/exchange"
	"orchestrator/internal/infra/natsapi"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
	"orchestrator/internal/runtime/core/portfolio"
	"orchestrator/internal/runtime/core/strategy"
	"orchestrator/internal/runtime/core/tactics"
	"orchestrator/internal/runtime/orderbook"
)

// OrchestratorRuntime is the live in-memory state for one orchestrator instance.
// Holds account-level shared resources: Exchange, Portfolio, OrderBook, RiskManager.
// Per-bot resources (strategy, tactician) live on the Bot itself.
//
// Lifecycle:
//
//	Spawn()  → runtime created, status = running
//	Pause()  → all bots stopped, signals rejected
//	Resume() → previously-running bots restarted
//	Teardown() → runtime destroyed
//
// RiskManager is the interface for account-level risk controls consumed by OrchestratorRuntime.
type RiskManager interface {
	IsHalted() bool
}

type OrchestratorRuntime struct {
	OrchestratorID uuid.UUID
	AccountID      uuid.UUID
	UserID         uuid.UUID
	BrokerType     string

	Portfolio *portfolio.Portfolio
	RiskMgr   RiskManager
	OrderBook orderbook.OrderBook
	Exchange  exchange.Exchange

	// fillCh decouples the broker WS goroutine from NATS publishing.
	// The WS callback drops fills here non-blocking; a dedicated goroutine drains and publishes.
	fillCh chan exchange.FillEvent

	mu     sync.RWMutex
	bots   map[string]*Bot
	paused bool

	// lastSyncAtNano stores the last successful sync time as UnixNano (0 = never).
	// Atomic so Sync() and LastSyncAt() need no extra lock.
	lastSyncAtNano atomic.Int64

	pricesMu sync.RWMutex
	prices   map[string]float64 // last known price per symbol

	tradeMu      sync.Mutex
	requestCount atomic.Int64
	resetTicker  *time.Ticker
}

// LastSyncAt returns the timestamp of the most recent portfolio sync, or zero if never synced.
func (r *OrchestratorRuntime) LastSyncAt() time.Time {
	ns := r.lastSyncAtNano.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

// storeSyncAt atomically advances lastSyncAtNano; ignores t if it is older than the current value.
func (r *OrchestratorRuntime) storeSyncAt(t time.Time) {
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
// No-op if the exchange does not implement AccountSyncer.
func (r *OrchestratorRuntime) Sync(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext) error {
	syncer, ok := r.Exchange.(exchange.AccountSyncer)
	if !ok {
		return nil
	}
	var since *time.Time
	if prev := r.LastSyncAt(); !prev.IsZero() {
		since = &prev
	}
	snap, err := syncer.SyncAccount(ctx, since)
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

	// Seed price cache from synced positions so ProcessTrade has a price immediately.
	r.pricesMu.Lock()
	for _, p := range snap.Positions {
		if p.CurPrice > 0 {
			r.prices[p.Symbol] = p.CurPrice
		}
	}
	r.pricesMu.Unlock()

	prevSyncAt := r.LastSyncAt()

	var newTxns []natsapi.TransactionMsg
	natsTxns := make([]natsapi.TransactionMsg, 0, len(snap.Transactions))
	for _, t := range snap.Transactions {
		msg := natsapi.TransactionMsg{
			OrderID:  t.OrderID,
			Symbol:   t.Symbol,
			Side:     t.Side,
			Qty:      t.Qty,
			AvgPrice: t.AvgPrice,
			Fee:      t.Fee,
			FilledAt: t.FilledAt,
		}
		natsTxns = append(natsTxns, msg)
		// Only forward transactions not yet published (first sync or after lastSyncAt).
		if prevSyncAt.IsZero() || t.FilledAt.After(prevSyncAt) {
			newTxns = append(newTxns, msg)
		}
	}

	now := time.Now().UTC()
	r.storeSyncAt(now)

	slog.Info("runtime: synced from exchange",
		"orchestrator_id", r.OrchestratorID,
		"positions", len(pfPositions),
		"transactions", len(natsTxns),
		"new_transactions", len(newTxns),
	)

	if nc != nil {
		natsapi.PublishPortfolioSync(nc, r.OrchestratorID.String(), r.AccountID.String(), snap.Cash, snap.Equity, natsPositions, natsTxns, now)

		// Publish new transactions to the investment JetStream stream for event sourcing.
		if js != nil && len(newTxns) > 0 {
			orchID := r.OrchestratorID.String()
			accountID := r.AccountID.String()
			userID := r.UserID.String()
			for _, t := range newTxns {
				natsapi.PublishInvestmentTransaction(js, orchID, accountID, userID, "", r.BrokerType, t)
			}
		}
	}
	return nil
}

// NewOrchestratorRuntime creates a runtime and starts its circuit-breaker reset ticker.
// lastSyncedAt, if non-nil, seeds the sync cursor so restarts replay only the gap.
func NewOrchestratorRuntime(
	orchID, accountID, userID uuid.UUID,
	brokerType string,
	pf *portfolio.Portfolio,
	riskMgr RiskManager,
	ob orderbook.OrderBook,
	ex exchange.Exchange,
	lastSyncedAt *time.Time,
) *OrchestratorRuntime {
	rt := &OrchestratorRuntime{
		OrchestratorID: orchID,
		AccountID:      accountID,
		UserID:         userID,
		BrokerType:     brokerType,
		Portfolio:      pf,
		RiskMgr:        riskMgr,
		OrderBook:      ob,
		Exchange:       ex,
		fillCh:         make(chan exchange.FillEvent, 128),
		bots:           make(map[string]*Bot),
		prices:         make(map[string]float64),
		resetTicker:    time.NewTicker(1 * time.Minute),
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
	Price  float64 // optional: resolved from price cache when zero
	ATR    float64
}

// ProcessTrade validates a trade against account-level guards and sizes via the bot's tactician.
// Pipeline: circuit breaker → risk check → price resolve → tactics (per-bot) → place order.
// OrderBook is tracked for reference only — it does not block execution.
// The tactician is owned by the bot but runs here under the account-level lock
// to guarantee consistent reads of portfolio equity and position state.
func (r *OrchestratorRuntime) ProcessTrade(
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

	// Resolve price: caller may omit it; fall back to last known price from cache/portfolio,
	// then on-demand via PriceFetcher if the exchange supports it.
	price := proposal.Price
	if price <= 0 {
		price = r.lastKnownPrice(proposal.Symbol)
	}
	if price <= 0 {
		if pf, ok := r.Exchange.(exchange.PriceFetcher); ok {
			if p, err := pf.GetCurrentPrice(ctx, proposal.Symbol); err == nil && p > 0 {
				price = p
				r.pricesMu.Lock()
				r.prices[proposal.Symbol] = p
				r.pricesMu.Unlock()
			}
		}
	}
	if price <= 0 {
		return orchdomain.TradeReply{Approved: false, Reason: "no price available for " + proposal.Symbol}
	}

	posQty := 0.0
	if pos := r.Portfolio.GetPosition(proposal.Symbol); pos != nil {
		posQty = pos.Qty
	}
	tact.UpdateEquity(r.Portfolio.Equity())
	plan := tact.Plan(proposal.Intent, tactics.MarketContext{
		Price:       price,
		ATR:         proposal.ATR,
		PositionQty: posQty,
	})

	if plan.Qty <= 0 {
		return orchdomain.TradeReply{Approved: false, Reason: "tactics: zero quantity after sizing"}
	}

	slog.Info("runtime: trade approved",
		"bot_id", proposal.BotID,
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

// TrackOrder records a placed order in the orderbook for duplicate detection.
func (r *OrchestratorRuntime) TrackOrder(order orderbook.PendingOrder) {
	r.OrderBook.TrackOrder(order)
}

// ReportFill applies a fill to the portfolio and removes the order from orderbook.
func (r *OrchestratorRuntime) ReportFill(fill orchdomain.FillReport) {
	r.tradeMu.Lock()
	defer r.tradeMu.Unlock()

	r.OrderBook.RemoveOrder(fill.AccountID, fill.OrderID)

	if fill.Price > 0 {
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
		Commission: 0,
	})

	slog.Info("runtime: fill applied",
		"bot_id", fill.BotID,
		"symbol", fill.Symbol,
		"side", fill.Side,
		"qty", fill.Qty,
		"price", fill.Price,
	)
}

// UpdatePrice stores the latest market price for a symbol and forwards it to the portfolio.
// Called by the market data listener and fill events so ProcessTrade always has a price.
func (r *OrchestratorRuntime) UpdatePrice(symbol string, price float64) {
	if price <= 0 {
		return
	}
	r.pricesMu.Lock()
	r.prices[symbol] = price
	r.pricesMu.Unlock()
	r.Portfolio.UpdatePrice(symbol, price)
}

// lastKnownPrice returns the last price seen for symbol, falling back to the portfolio position price.
func (r *OrchestratorRuntime) lastKnownPrice(symbol string) float64 {
	r.pricesMu.RLock()
	p := r.prices[symbol]
	r.pricesMu.RUnlock()
	if p > 0 {
		return p
	}
	if pos := r.Portfolio.GetPosition(symbol); pos != nil {
		return pos.CurrentPrice
	}
	return 0
}

// EnqueueFill drops a broker fill event into the runtime's fill channel non-blocking.
// Called from the broker WS goroutine — must never block.
func (r *OrchestratorRuntime) EnqueueFill(ev exchange.FillEvent) {
	select {
	case r.fillCh <- ev:
	default:
		slog.Error("fill channel full, dropping fill event",
			"orchestrator_id", r.OrchestratorID,
			"order_id", ev.OrderID,
			"symbol", ev.Symbol,
		)
	}
}

// Stop cleans up the circuit-breaker ticker.
func (r *OrchestratorRuntime) Stop() {
	if r.resetTicker != nil {
		r.resetTicker.Stop()
	}
}

func (r *OrchestratorRuntime) IsPaused() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.paused
}

// Pause suspends the runtime — all bots will ignore incoming signals.
// Returns IDs of bots that were running, so the caller can stop their goroutines.
func (r *OrchestratorRuntime) Pause() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = true
	var wasRunning []string
	for id, bot := range r.bots {
		bot.WasRunning = bot.IsRunning()
		if bot.WasRunning {
			wasRunning = append(wasRunning, id)
		}
	}
	return wasRunning
}

// Resume unpauses the runtime and returns IDs of bots that should be restarted.
func (r *OrchestratorRuntime) Resume() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = false
	var toRestart []string
	for id, bot := range r.bots {
		if bot.WasRunning {
			bot.WasRunning = false
			toRestart = append(toRestart, id)
		}
	}
	return toRestart
}

// AddBot registers a bot with this runtime. The bot's ID is used as the map key.
func (r *OrchestratorRuntime) AddBot(bot *Bot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bots[bot.id] = bot
}

func (r *OrchestratorRuntime) RemoveBot(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bots, id)
}

func (r *OrchestratorRuntime) BotIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.bots))
	for id := range r.bots {
		ids = append(ids, id)
	}
	return ids
}

func (r *OrchestratorRuntime) RunningBotIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var ids []string
	for id, bot := range r.bots {
		if bot.IsRunning() {
			ids = append(ids, id)
		}
	}
	return ids
}

// DispatchBotSignal routes a signal to the named bot owned by this runtime.
// Returns true when the bot exists, even if the bot later filters the signal.
func (r *OrchestratorRuntime) DispatchBotSignal(botID string, sig Signal) bool {
	r.mu.RLock()
	bot, ok := r.bots[botID]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	bot.DeliverSignal(sig)
	return true
}
