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

	"orchestrator/internal/infra/exchange"
	"orchestrator/internal/infra/natsapi"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
	"orchestrator/internal/runtime/core/orderbook"
	"orchestrator/internal/runtime/core/portfolio"
	"orchestrator/internal/runtime/core/risk"
	"orchestrator/internal/runtime/core/strategy"
	"orchestrator/internal/runtime/core/tactics"
)

// RiskManager is the interface for account-level risk controls.
type RiskManager interface {
	IsHalted() bool
	ResetHalt()
	UpdateConfig(cfg risk.Config)
}

// Orchestrator is the live in-memory state for one orchestrator instance.
// Holds account-level shared resources: Exchange, Portfolio, OrderBook, RiskManager.
// Per-bot resources (strategy, tactician) live on the Bot itself.
type Orchestrator struct {
	OrchestratorID uuid.UUID
	AccountID      uuid.UUID
	UserID         uuid.UUID
	BrokerType     string

	Portfolio *portfolio.Portfolio
	RiskMgr   RiskManager
	OrderBook orderbook.OrderBook
	Exchange  exchange.Exchange
	Creds     exchange.Credentials // per-account credentials passed to all Exchange calls

	// orderCh decouples the broker WS goroutine from NATS publishing.
	orderCh chan exchange.OrderEvent

	mu     sync.RWMutex
	bots   map[string]*Bot
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
}

// LastSyncAt returns the timestamp of the most recent portfolio sync, or zero if never synced.
func (r *Orchestrator) LastSyncAt() time.Time {
	ns := r.lastSyncAtNano.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

func (r *Orchestrator) storeSyncAt(t time.Time) {
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
func (r *Orchestrator) Sync(ctx context.Context, nc *nats.Conn, js nats.JetStreamContext) error {
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
			OrderID:  t.OrderID,
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
		"orchestrator_id", r.OrchestratorID,
		"positions", len(pfPositions),
		"transactions", len(natsTxns),
		"new_transactions", len(newTxns),
	)

	if nc != nil {
		natsapi.PublishPortfolioSync(nc, r.OrchestratorID.String(), r.AccountID.String(), snap.Cash, snap.Equity, natsPositions, natsTxns, now)

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

// NewOrchestrator creates an Orchestrator and starts its circuit-breaker reset ticker.
func NewOrchestrator(
	orchID, accountID, userID uuid.UUID,
	brokerType string,
	pf *portfolio.Portfolio,
	riskMgr RiskManager,
	ob orderbook.OrderBook,
	ex exchange.Exchange,
	creds exchange.Credentials,
	lastSyncedAt *time.Time,
) *Orchestrator {
	rt := &Orchestrator{
		OrchestratorID: orchID,
		AccountID:      accountID,
		UserID:         userID,
		BrokerType:     brokerType,
		Portfolio:      pf,
		RiskMgr:        riskMgr,
		OrderBook:      ob,
		Exchange:       ex,
		Creds:          creds,
		orderCh:        make(chan exchange.OrderEvent, 128),
		bots:           make(map[string]*Bot),
		prices:         make(map[string]decimal.Decimal),
		l2Books:        make(map[string]exchange.L2Snapshot),
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
	Price  decimal.Decimal // optional: resolved from price cache when zero
	ATR    decimal.Decimal
}

// ProcessTrade validates a trade against account-level guards and sizes via the bot's tactician.
func (r *Orchestrator) ProcessTrade(
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

// TrackOrder records a placed order in the orderbook.
func (r *Orchestrator) TrackOrder(order orderbook.PendingOrder) {
	r.OrderBook.TrackOrder(order)
}

// ReportFill applies a fill to the portfolio and removes the order from orderbook.
func (r *Orchestrator) ReportFill(fill orchdomain.FillReport) {
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
		"bot_id", fill.BotID,
		"symbol", fill.Symbol,
		"side", fill.Side,
		"qty", fill.Qty,
		"price", fill.Price,
	)
}

// UpdatePrice stores the latest market price for a symbol and forwards it to the portfolio.
func (r *Orchestrator) UpdatePrice(symbol string, price decimal.Decimal) {
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
// must not block; OnL2 on each bot must be fast.
func (r *Orchestrator) UpdateL2(snap exchange.L2Snapshot) {
	r.l2Mu.Lock()
	r.l2Books[snap.Symbol] = snap
	r.l2Mu.Unlock()

	r.mu.RLock()
	for _, bot := range r.bots {
		if bot.Symbol == snap.Symbol && bot.IsRunning() {
			bot.OnL2(snap)
		}
	}
	r.mu.RUnlock()
}

// LatestL2 returns the most recent books5 snapshot for a symbol.
// ok=false if no snapshot has been received yet.
func (r *Orchestrator) LatestL2(symbol string) (exchange.L2Snapshot, bool) {
	r.l2Mu.RLock()
	s, ok := r.l2Books[symbol]
	r.l2Mu.RUnlock()
	return s, ok
}

func (r *Orchestrator) lastKnownPrice(symbol string) decimal.Decimal {
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
func (r *Orchestrator) EnqueueOrderEvent(ev exchange.OrderEvent) {
	select {
	case r.orderCh <- ev:
	default:
		slog.Error("order channel full, dropping event",
			"orchestrator_id", r.OrchestratorID,
			"type", ev.Type,
			"order_id", ev.OrderID,
			"symbol", ev.Symbol,
		)
	}
}

// Stop cleans up the circuit-breaker ticker.
func (r *Orchestrator) Stop() {
	if r.resetTicker != nil {
		r.resetTicker.Stop()
	}
}

func (r *Orchestrator) IsPaused() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.paused
}

// Pause suspends the runtime — all bots will ignore incoming signals.
func (r *Orchestrator) Pause() []string {
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
func (r *Orchestrator) Resume() []string {
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

// ResetHalt clears the risk-manager halt flag on this runtime.
func (r *Orchestrator) ResetHalt() {
	r.RiskMgr.ResetHalt()
}

// UpdateRiskConfig replaces the live risk parameters (portfolio + risk sliders).
func (r *Orchestrator) UpdateRiskConfig(cfg risk.Config) {
	r.RiskMgr.UpdateConfig(cfg)
}

// AddBot registers a bot with this runtime.
func (r *Orchestrator) AddBot(bot *Bot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bots[bot.id] = bot
}

func (r *Orchestrator) RemoveBot(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bots, id)
}

func (r *Orchestrator) BotIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.bots))
	for id := range r.bots {
		ids = append(ids, id)
	}
	return ids
}

func (r *Orchestrator) RunningBotIDs() []string {
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

// DispatchBotSignal routes a signal to the named bot owned by this orchestrator.
// Returns false if the bot is not found. Logs and skips if the bot is paused.
func (r *Orchestrator) DispatchBotSignal(botID string, sig Signal) bool {
	r.mu.RLock()
	bot, ok := r.bots[botID]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	if bot.IsPaused() {
		slog.Debug("orchestrator: bot paused, signal skipped",
			"bot_id", botID, "symbol", sig.Symbol, "direction", sig.Direction)
		return true
	}
	bot.DeliverSignal(sig)
	return true
}
