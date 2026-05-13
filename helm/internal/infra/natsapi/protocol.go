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
	SubjectPrefix = "helm."

	// Orchestrator CRUD via NATS (user-scoped request/reply from strategist/gateway)
	SubjOrchList      = "helm.helms.list"
	SubjOrchGet       = "helm.helms.get"
	SubjOrchUpdate    = "helm.helms.update"
	SubjOrchEnable    = "helm.helms.enable"
	SubjOrchDisable   = "helm.helms.disable"
	SubjOrchPause     = "helm.helms.pause"
	SubjOrchResume    = "helm.helms.resume"
	SubjOrchKill      = "helm.helms.kill"
	SubjOrchResetHalt = "helm.helms.halt.reset"
	SubjOrchPortfolio = "helm.helms.portfolio"
	SubjOrchPositions = "helm.helms.positions"
	SubjOrchTrades    = "helm.helms.trades"
	SubjOrchOrders    = "helm.helms.orders"

	// Events published by identity/investment service (fire-and-forget).
	// Keep them under helm.> so they match the helm NATS ACL.
	SubjAccountLinked   = "helm.accounts.linked"   // triggers auto-create helm
	SubjAccountUnlinked = "helm.accounts.unlinked" // triggers auto-delete helm

	SubjHandList    = "helm.hands.list"
	SubjHandGet     = "helm.hands.get"
	SubjHandCreate  = "helm.hands.create"
	SubjHandUpdate  = "helm.hands.update"
	SubjHandDelete  = "helm.hands.delete"
	SubjHandStart   = "helm.hands.start"
	SubjHandStop    = "helm.hands.stop"
	SubjHandRestart = "helm.hands.restart"
	SubjHandPause   = "helm.hands.pause"
	SubjHandResume  = "helm.hands.resume"
	SubjHandKill    = "helm.hands.kill"

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

// AccountLinkedEvent is published to helm.accounts.linked when a broker account is linked.
// Helm subscribes and auto-creates an HelmConfig for that account.
// Credentials are NOT included — helm fetches them on demand via investment.accounts.credentials.
type AccountLinkedEvent struct {
	AccountID   string          `json:"account_id"`
	UserID      string          `json:"user_id"`
	Name        string          `json:"name"`
	Capital     decimal.Decimal `json:"capital"`
	BrokerType  string          `json:"broker_type"`
	AccountType string          `json:"account_type,omitempty"` // spot | futures | unified
	AccountRef  string          `json:"account_ref,omitempty"`  // IBKR/Oanda account ID
	BaseURL     string          `json:"base_url,omitempty"`
	Paper       bool            `json:"paper,omitempty"` // true = paper/demo trading
}

// SubjCredentialsFetch is the NATS request/reply subject to fetch decrypted broker credentials
// from the investment service. Helm calls this before spawning a runtime.
const SubjCredentialsFetch = "investment.accounts.credentials"

// CredentialsFetchResp is the data payload returned by investment.accounts.credentials.
// Includes both credentials and exchange metadata so a single call is enough to spawn a runtime.
type CredentialsFetchResp struct {
	APIKey     string `json:"api_key"`
	APISecret  string `json:"api_secret"`
	Passphrase string `json:"passphrase,omitempty"`
	// Exchange metadata
	BrokerType  string `json:"broker_type,omitempty"`
	AccountType string `json:"account_type,omitempty"` // spot | futures | unified
	AccountRef  string `json:"account_ref,omitempty"`  // IBKR / Oanda account ID
	BaseURL     string `json:"base_url,omitempty"`
	Paper       bool   `json:"paper,omitempty"` // true = paper/demo trading
}

// FetchCredentials requests decrypted broker credentials from the investment service via NATS.
// The accountID is the investment Account UUID (not the broker connection UUID).
func FetchCredentials(nc *nats.Conn, accountID string) (CredentialsFetchResp, error) {
	req, _ := json.Marshal(map[string]string{"account_id": accountID})
	msg, err := nc.Request(SubjCredentialsFetch, req, 10*time.Second)
	if err != nil {
		return CredentialsFetchResp{}, fmt.Errorf("fetch credentials: %w", err)
	}
	var reply Reply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return CredentialsFetchResp{}, fmt.Errorf("parse credentials reply: %w", err)
	}
	if !reply.OK {
		return CredentialsFetchResp{}, fmt.Errorf("credentials fetch: %s", reply.Error)
	}
	var data CredentialsFetchResp
	if err := json.Unmarshal(reply.Data, &data); err != nil {
		return CredentialsFetchResp{}, fmt.Errorf("parse credentials data: %w", err)
	}
	return data, nil
}

// AccountUnlinkedEvent is published to helm.accounts.unlinked when a broker account is removed.
type AccountUnlinkedEvent struct {
	AccountID string `json:"account_id"`
	UserID    string `json:"user_id"`
}

// FillNotification is published to trade.filled.{orchestrator_id} when an
// order is confirmed filled via the exchange's private account stream.
type FillNotification struct {
	OrchestratorID string          `json:"helm_id"`
	BotID          string          `json:"hand_id"`
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

// TransactionMsg represents one atomic transaction event in the investment ledger.
//
// Kind values:
//
//	"open_order" — order placed; locks capital on the cash side.
//	"fill"       — partial or full fill; settles qty into position, releases reserved cash.
//	"cancel"     — order cancelled/expired; releases any remaining reserved capital.
//
// TradeID is the dedup key (JetStream Nats-Msg-Id). For fills it is the
// exchange-assigned fill/trade ID (unique per partial fill). For open_order and
// cancel events it is orderID+"_open" / orderID+"_cancel".
type TransactionMsg struct {
	TradeID  string          `json:"trade_id"` // dedup key; exchange fill ID for fills
	OrderID  string          `json:"order_id"` // groups all events for the same order
	Kind     string          `json:"kind"`     // "open_order" | "fill" | "cancel"
	Symbol   string          `json:"symbol"`
	Side     string          `json:"side"`                // "buy" | "sell"
	Qty      decimal.Decimal `json:"qty"`                 // fill qty (fills) or original order qty (open/cancel)
	AvgPrice decimal.Decimal `json:"avg_price,omitempty"` // fill price; zero for open_order/cancel
	Fee      decimal.Decimal `json:"fee,omitempty"`
	FilledAt time.Time       `json:"filled_at"`
}

// PortfolioSyncEvent is published to portfolio.synced.{account_id} after each
// REST sync (on-create, on-enable, or periodic poll). The investment service
// subscribes and updates its account snapshot and transaction history.
type PortfolioSyncEvent struct {
	OrchestratorID string              `json:"helm_id"`
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

	// Map Kind to investment transaction type.
	txType := txn.Kind
	if txType == "" || txType == "fill" {
		txType = txn.Side // "buy" | "sell" for fills
	}

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
		ExternalID:      txn.OrderID,
		TradeID:         txn.TradeID,
		Kind:            txn.Kind,
		Source:          "hand",
		BotID:           botID,
	}
	data, _ := json.Marshal(msg)

	natMsg := &nats.Msg{
		Subject: fmt.Sprintf(SubjInvestmentTransactions, accountID),
		Data:    data,
		Header:  nats.Header{},
	}
	// Dedup key = TradeID (exchange fill ID), falls back to orchID+orderID for open/cancel events.
	dedupKey := txn.TradeID
	if dedupKey == "" {
		dedupKey = orchID + "-" + txn.OrderID
	}
	natMsg.Header.Set(nats.MsgIdHdr, dedupKey)

	if _, err := js.PublishMsg(natMsg); err != nil {
		slog.Warn("investment transaction publish failed",
			"subject", natMsg.Subject,
			"trade_id", txn.TradeID,
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
