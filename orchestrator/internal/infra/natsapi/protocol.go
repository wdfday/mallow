package natsapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"mallow/pkg/contracts"
)

// Reply is the standard NATS request/reply response envelope.
type Reply struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

func ReplyOK(v any) []byte {
	data, _ := json.Marshal(v)
	out, _ := json.Marshal(Reply{OK: true, Data: data})
	return out
}

func ReplyErr(msg string) []byte {
	out, _ := json.Marshal(Reply{OK: false, Error: msg})
	return out
}

// CallerMeta carries the caller identity for user-scoped NATS request/reply.
// Services embed this in their request payloads so the handler knows who is acting.
// NATS is a trusted internal network — no JWT verification is done on messages.
// The gateway or strategist is responsible for populating these fields from the
// validated JWT before publishing to NATS.
type CallerMeta struct {
	CallerUserID string `json:"caller_user_id,omitempty"` // user UUID from JWT sub
	CallerSvc    string `json:"caller_svc,omitempty"`     // e.g. "strategist", "gateway"
}

// ParseID extracts a uuid.UUID from a JSON `{"id":"..."}` payload.
func ParseID(data []byte) (uuid.UUID, error) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(req.ID)
}

// ParseCaller extracts CallerMeta from a raw JSON payload without full deserialization.
func ParseCaller(data []byte) CallerMeta {
	var c CallerMeta
	_ = json.Unmarshal(data, &c)
	return c
}

// NATS subject constants.
const (
	SubjectPrefix = "orchestrator."

	// Orchestrator CRUD via NATS (user-scoped request/reply from strategist/gateway)
	SubjOrchList      = "orchestrator.orchestrators.list"
	SubjOrchGet       = "orchestrator.orchestrators.get"
	SubjOrchUpdate    = "orchestrator.orchestrators.update"
	SubjOrchEnable    = "orchestrator.orchestrators.enable"
	SubjOrchDisable   = "orchestrator.orchestrators.disable"
	SubjOrchPause     = "orchestrator.orchestrators.pause"
	SubjOrchResume    = "orchestrator.orchestrators.resume"
	SubjOrchPortfolio = "orchestrator.orchestrators.portfolio"
	SubjOrchPositions = "orchestrator.orchestrators.positions"
	SubjOrchTrades    = "orchestrator.orchestrators.trades"
	SubjOrchOrders    = "orchestrator.orchestrators.orders"

	// Events published by identity/investment service (fire-and-forget).
	// Keep them under orchestrator.> so they match the orchestrator NATS ACL.
	SubjAccountLinked   = "orchestrator.accounts.linked"   // triggers auto-create orchestrator
	SubjAccountUnlinked = "orchestrator.accounts.unlinked" // triggers auto-delete orchestrator

	SubjBotList   = "orchestrator.bots.list"
	SubjBotGet    = "orchestrator.bots.get"
	SubjBotCreate = "orchestrator.bots.create"
	SubjBotUpdate = "orchestrator.bots.update"
	SubjBotDelete = "orchestrator.bots.delete"
	SubjBotStart  = "orchestrator.bots.start"
	SubjBotStop   = "orchestrator.bots.stop"
	SubjBotPause  = "orchestrator.bots.pause"
	SubjBotResume = "orchestrator.bots.resume"
	SubjBotKill   = "orchestrator.bots.kill"

	// Events (fire-and-forget, not request/reply)
	// Format with orchestrator_id: fmt.Sprintf(SubjTradeFilled, orchID)
	SubjTradeFilled = "trade.filled.%s"

	// Published to investment service after each portfolio sync (polling or on-enable).
	// Format with account_id: fmt.Sprintf(SubjPortfolioSynced, accountID)
	SubjPortfolioSynced = "portfolio.synced.%s"

	// JetStream subject for investment transaction events.
	// Format with account_id: fmt.Sprintf(SubjInvestmentTransactions, accountID)
	SubjInvestmentTransactions = "investment.transactions.%s"
)

// AccountLinkedEvent is published to orchestrator.accounts.linked when a broker account is linked.
// The orchestrator subscribes and auto-creates an OrchestratorConfig for that account.
type AccountLinkedEvent struct {
	AccountID  string          `json:"account_id"`
	UserID     string          `json:"user_id"`
	Name       string          `json:"name"`
	Capital    decimal.Decimal `json:"capital"`
	BrokerType string          `json:"broker_type"`
	APIKey     string          `json:"api_key,omitempty"`
	APISecret  string          `json:"api_secret,omitempty"`
	Passphrase string          `json:"passphrase,omitempty"`
	AccountRef string          `json:"account_ref,omitempty"` // IBKR/Oanda account ID
	BaseURL    string          `json:"base_url,omitempty"`
	Demo       bool            `json:"demo,omitempty"`
	Testnet    bool            `json:"testnet,omitempty"`
}

// AccountUnlinkedEvent is published to orchestrator.accounts.unlinked when a broker account is removed.
type AccountUnlinkedEvent struct {
	AccountID string `json:"account_id"`
	UserID    string `json:"user_id"`
}

// FillNotification is published to trade.filled.{orchestrator_id} when an
// order is confirmed filled via the exchange's private account stream.
type FillNotification struct {
	OrchestratorID string          `json:"orchestrator_id"`
	BotID          string          `json:"bot_id"`
	OrderID        string          `json:"order_id"`
	Symbol         string          `json:"symbol"`
	Side           string          `json:"side"`
	FilledQty      decimal.Decimal `json:"filled_qty"`
	FilledAvg      decimal.Decimal `json:"filled_avg"`
	Timestamp      time.Time       `json:"timestamp"`
}

// SyncedPositionMsg is one position inside PortfolioSyncEvent.
type SyncedPositionMsg struct {
	Symbol   string          `json:"symbol"`
	Qty      decimal.Decimal `json:"qty"`
	AvgPrice decimal.Decimal `json:"avg_price"`
	CurPrice decimal.Decimal `json:"cur_price"`
}

// TransactionMsg is one filled order inside PortfolioSyncEvent.
type TransactionMsg struct {
	OrderID  string          `json:"order_id"`
	Symbol   string          `json:"symbol"`
	Side     string          `json:"side"` // "buy" | "sell"
	Qty      decimal.Decimal `json:"qty"`
	AvgPrice decimal.Decimal `json:"avg_price"`
	Fee      decimal.Decimal `json:"fee"`
	FilledAt time.Time       `json:"filled_at"`
}

// PortfolioSyncEvent is published to portfolio.synced.{account_id} after each
// REST sync (on-create, on-enable, or periodic poll). The investment service
// subscribes and updates its account snapshot and transaction history.
type PortfolioSyncEvent struct {
	OrchestratorID string              `json:"orchestrator_id"`
	AccountID      string              `json:"account_id"`
	Cash           decimal.Decimal     `json:"cash"`
	Equity         decimal.Decimal     `json:"equity"`
	Positions      []SyncedPositionMsg `json:"positions"`
	Transactions   []TransactionMsg    `json:"transactions"` // filled orders since last sync
	SyncedAt       time.Time           `json:"synced_at"`
}

// brokerMeta returns the assetType, currency, and exchange name for a given broker type.
func brokerMeta(brokerType string) (assetType, currency, exchangeName string) {
	switch brokerType {
	case "binance":
		return "crypto", "USDT", "BINANCE"
	case "okx":
		return "crypto", "USD", "OKX"
	case "bybit":
		return "crypto", "USDT", "BYBIT"
	case "ibkr":
		return "stock", "USD", "IBKR"
	case "oanda":
		return "forex", "USD", "OANDA"
	default: // alpaca and unknown
		return "stock", "USD", "ALPACA"
	}
}

// PublishInvestmentTransaction publishes a single fill as an InvestmentTransactionMsg to the
// INVESTMENT_TRANSACTIONS JetStream stream (investment.transactions.{accountID}).
// Uses Nats-Msg-Id = "{orchID}-{orderID}" for deduplication across real-time and sync paths.
func PublishInvestmentTransaction(js nats.JetStreamContext, orchID, accountID, userID, botID, brokerType string, txn TransactionMsg) {
	assetType, currency, exchangeName := brokerMeta(brokerType)
	txType := txn.Side // "buy" | "sell"

	msg := contracts.InvestmentTransactionMsg{
		AccountID:       accountID,
		UserID:          userID,
		Type:            txType,
		Symbol:          txn.Symbol,
		AssetType:       assetType,
		Exchange:        exchangeName,
		Currency:        currency,
		Quantity:        txn.Qty.InexactFloat64(),
		PricePerUnit:    txn.AvgPrice.InexactFloat64(),
		Amount:          txn.Qty.Mul(txn.AvgPrice).InexactFloat64(),
		Fees:            txn.Fee.InexactFloat64(),
		TransactionDate: txn.FilledAt,
		Broker:          brokerType,
		ExternalID:      txn.OrderID,
		Source:          "sync",
		BotID:           botID,
	}
	data, _ := json.Marshal(msg)

	natMsg := &nats.Msg{
		Subject: fmt.Sprintf(SubjInvestmentTransactions, accountID),
		Data:    data,
		Header:  nats.Header{},
	}
	natMsg.Header.Set(nats.MsgIdHdr, orchID+"-"+txn.OrderID)

	if _, err := js.PublishMsg(natMsg); err != nil {
		slog.Warn("investment transaction publish failed",
			"subject", natMsg.Subject,
			"order_id", txn.OrderID,
			"err", err,
		)
	}
}

// PublishPortfolioSync publishes a PortfolioSyncEvent to portfolio.synced.{accountID}.
func PublishPortfolioSync(nc *nats.Conn, orchID, accountID string, cash, equity decimal.Decimal, positions []SyncedPositionMsg, transactions []TransactionMsg, syncedAt time.Time) {
	ev := PortfolioSyncEvent{
		OrchestratorID: orchID,
		AccountID:      accountID,
		Cash:           cash,
		Equity:         equity,
		Positions:      positions,
		Transactions:   transactions,
		SyncedAt:       syncedAt,
	}
	data, _ := json.Marshal(ev)
	subj := fmt.Sprintf(SubjPortfolioSynced, accountID)
	if err := nc.Publish(subj, data); err != nil {
		slog.Warn("portfolio sync: nats publish failed", "subject", subj, "err", err)
	}
}
