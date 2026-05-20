package natsapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"
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

	// Account lifecycle events (published by helm broker module).
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

	// JetStream subjects — all retained for audit / query.
	// Format with account_id: fmt.Sprintf(SubjTradeFilled, accountID)
	// Format with account_id: fmt.Sprintf(SubjPortfolioSynced, accountID)
	SubjTradeFilled     = "trade.filled.%s"
	SubjPortfolioSynced = "portfolio.synced.%s"

	// Real-time behavior event stream for a helm. Published on every significant
	// hand/helm lifecycle event (signal, order, fill, pause, …). Fire-and-forget;
	// clients subscribe to `helm.events.{helm_id}` for live activity feed.
	// Format with helm_id: fmt.Sprintf(SubjHelmEvents, helmID)
	SubjHelmEvents = "helm.events.%s"
)

// AccountLinkedEvent is published to helm.accounts.linked when a broker account is linked.
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

// CredentialsFetchResp carries decrypted broker credentials and exchange metadata
// needed to spawn a helm runtime. Returned directly by BrokerConnectionService.GetCredentialsByAccountID.
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

// AccountUnlinkedEvent is published to helm.accounts.unlinked when a broker account is removed.
type AccountUnlinkedEvent struct {
	AccountID string `json:"account_id"`
	UserID    string `json:"user_id"`
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
	// Routing
	HelmID    string `json:"helm_id"`
	AccountID string `json:"account_id"`
	UserID    string `json:"user_id"`
	HandID    string `json:"hand_id,omitempty"`

	// Fill data
	TradeID  string          `json:"trade_id"` // dedup key; exchange fill ID for fills
	OrderID  string          `json:"order_id"` // groups all events for the same order
	Kind     string          `json:"kind"`     // "open_order" | "fill" | "cancel"
	Symbol   string          `json:"symbol"`
	Side     string          `json:"side"`
	Qty      decimal.Decimal `json:"qty"`
	AvgPrice decimal.Decimal `json:"avg_price,omitzero"`
	Fee      decimal.Decimal `json:"fee,omitzero"`
	FilledAt time.Time       `json:"filled_at"`
}

// PortfolioSyncEvent is published to portfolio.synced.{account_id} after each
// REST sync (on-create, on-enable, or periodic poll). The investment service
// subscribes and updates its account snapshot and transaction history.
type PortfolioSyncEvent struct {
	OrchestratorID string              `json:"helm_id"`
	AccountID      string              `json:"account_id"`
	UserID         string              `json:"user_id"`
	Cash           decimal.Decimal     `json:"cash"`
	Equity         decimal.Decimal     `json:"equity"`
	Positions      []SyncedPositionMsg `json:"positions"`
	Transactions   []TransactionMsg    `json:"transactions"` // filled orders since last sync
	SyncedAt       time.Time           `json:"synced_at"`
}

// PublishTradeFill publishes txn to TRADE_FILLS JetStream stream (trade.filled.{accountID}).
// Nats-Msg-Id = TradeID for dedup across real-time and sync paths; falls back to helmID+orderID.
func PublishTradeFill(js nats.JetStreamContext, txn TransactionMsg) {
	data, _ := json.Marshal(txn)
	natMsg := &nats.Msg{
		Subject: fmt.Sprintf(SubjTradeFilled, txn.AccountID),
		Data:    data,
		Header:  nats.Header{},
	}
	dedupKey := txn.TradeID
	if dedupKey == "" {
		dedupKey = txn.HelmID + "-" + txn.OrderID
	}
	natMsg.Header.Set(nats.MsgIdHdr, dedupKey)
	if _, err := js.PublishMsg(natMsg); err != nil {
		slog.Warn("trade fill publish failed", "subject", natMsg.Subject, "trade_id", txn.TradeID, "err", err)
	}
}

// PublishPortfolioSync publishes a PortfolioSyncEvent to the PORTFOLIO_SYNC JetStream stream
// (portfolio.synced.{accountID}). Durable with 1-day retention so consumers that
// were briefly offline receive missed sync notifications on reconnect.
func PublishPortfolioSync(js nats.JetStreamContext, orchID, accountID, userID string, cash, equity decimal.Decimal, positions []SyncedPositionMsg, transactions []TransactionMsg, syncedAt time.Time) {
	ev := PortfolioSyncEvent{
		OrchestratorID: orchID,
		AccountID:      accountID,
		UserID:         userID,
		Cash:           cash,
		Equity:         equity,
		Positions:      positions,
		Transactions:   transactions,
		SyncedAt:       syncedAt,
	}
	data, _ := json.Marshal(ev)
	subj := fmt.Sprintf(SubjPortfolioSynced, accountID)
	msg := nats.NewMsg(subj)
	msg.Data = data
	msg.Header.Set(nats.MsgIdHdr, fmt.Sprintf("%s-%d", accountID, syncedAt.UnixMilli()))
	if _, err := js.PublishMsg(msg); err != nil {
		slog.Warn("portfolio sync: jetstream publish failed", "subject", subj, "err", err)
	}
}

// HelmEvent is published to helm.events.{helm_id} on every significant hand/helm
// lifecycle event. Clients subscribe to this subject for a real-time activity feed.
// HandID is empty for helm-level events (pause, resume, sync, …).
type HelmEvent struct {
	HelmID    string          `json:"helm_id"`
	HandID    string          `json:"hand_id,omitempty"`
	At        time.Time       `json:"at"`
	Code      int             `json:"code"`
	Symbol    string          `json:"symbol,omitempty"`
	Direction string          `json:"direction,omitempty"`
	Side      string          `json:"side,omitempty"`
	Qty       decimal.Decimal `json:"qty,omitzero"`
	Price     decimal.Decimal `json:"price,omitzero"`
	OrderID   string          `json:"order_id,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Msg       string          `json:"msg"`
}

// PublishHelmEvent publishes a HelmEvent to the HELM_EVENTS JetStream stream
// (helm.events.{helmID}). Durable — clients that were offline can replay recent events.
func PublishHelmEvent(js nats.JetStreamContext, helmID string, ev HelmEvent) {
	data, _ := json.Marshal(ev)
	subj := fmt.Sprintf(SubjHelmEvents, helmID)
	msg := nats.NewMsg(subj)
	msg.Data = data
	// Dedup key: helm + code + ts-ms so burst retries within 1 min don't duplicate.
	msg.Header.Set(nats.MsgIdHdr, fmt.Sprintf("%s-%d-%d", helmID, ev.Code, ev.At.UnixMilli()))
	if _, err := js.PublishMsg(msg); err != nil {
		slog.Warn("helm event: jetstream publish failed", "helm_id", helmID, "code", ev.Code, "err", err)
	}
}
