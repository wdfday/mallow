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
	SubjOrchResetHalt = "helm.helms.halt.reset"
	SubjOrchPortfolio = "helm.helms.portfolio"
	SubjOrchPositions = "helm.helms.positions"
	SubjOrchTrades    = "helm.helms.trades"
	SubjOrchOrders    = "helm.helms.orders"
	// SubjOrchStats returns a live stats snapshot of all runtimes + hands.
	// No caller_user_id required (aggregate view, no per-user filtering).
	SubjOrchStats = "helm.helms.stats"

	// Account lifecycle events (published by helm broker module).
	SubjAccountLinked   = "helm.accounts.linked"   // triggers auto-create helm
	SubjAccountUnlinked = "helm.accounts.unlinked" // triggers auto-delete helm

	SubjHandList   = "helm.hands.list"
	SubjHandGet    = "helm.hands.get"
	SubjHandCreate = "helm.hands.create"
	SubjHandUpdate = "helm.hands.update"
	// Hands are never hard-deleted — they are kept for review. Lifecycle is
	// stop / kill / release only. (No helm.hands.delete, .pause, or .resume subject.)
	SubjHandStart = "helm.hands.start"
	SubjHandStop  = "helm.hands.stop"
	SubjHandKill  = "helm.hands.kill"

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

	// Signal audit stream — only herald-originated signals (see PublishSignal doc).
	// Format with helm_id: fmt.Sprintf(SubjSignalReceived, helmID)
	SubjSignalReceived = "helm.signals.%s"
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
	AvailableCash  decimal.Decimal     `json:"available_cash"`
	Equity         decimal.Decimal     `json:"equity"`
	Positions      []SyncedPositionMsg `json:"positions"`
	Transactions   []TransactionMsg    `json:"transactions"` // filled orders since last sync
	SyncedAt       time.Time           `json:"synced_at"`
}

// PublishTradeFill publishes txn to TRADE_FILLS JetStream stream (trade.filled.{accountID}).
// Nats-Msg-Id = TradeID for dedup across real-time and sync paths; falls back to helmID+orderID.
// The fallback is written back into txn.TradeID before marshaling so the persisted payload
// (filllog's Postgres `fills` table, UNIQUE INDEX on trade_id) carries the same value as the
// dedup key — otherwise every fill with no exchange trade ID (poll/kill/limit-timeout paths)
// would marshal with trade_id="", and the second such fill anywhere in the table would violate
// the unique index and be silently dropped by filllog's `ON CONFLICT (trade_id) DO NOTHING`.
func PublishTradeFill(js nats.JetStreamContext, txn TransactionMsg) {
	dedupKey := txn.TradeID
	if dedupKey == "" {
		dedupKey = txn.HelmID + "-" + txn.OrderID
		txn.TradeID = dedupKey
	}
	data, _ := json.Marshal(txn)
	natMsg := &nats.Msg{
		Subject: fmt.Sprintf(SubjTradeFilled, txn.AccountID),
		Data:    data,
		Header:  nats.Header{},
	}
	natMsg.Header.Set(nats.MsgIdHdr, dedupKey)
	if _, err := js.PublishMsg(natMsg); err != nil {
		slog.Warn("trade fill publish failed", "subject", natMsg.Subject, "trade_id", txn.TradeID, "err", err)
	}
}

// SignalMsg is an audit record of a herald-originated signal a hand processed.
// Internal signals (checkExits' local SL/TP monitor, checkPositionDesync's orphan
// detection) are never published here — only what herald actually told us, so the
// audit trail reflects the strategy's real inputs, not helm's own reactive machinery.
// ExitKind is "" for entry signals or "signal" for herald-originated exits — see
// strategy.ExitKind's doc for why "tp"/"sl"/"orphaned" never reach this struct.
type SignalMsg struct {
	ID          string  `json:"id"` // fresh UUID per signal — dedup key; signals have no natural idempotent key like fills/orders do
	HelmID      string  `json:"helm_id"`
	HandID      string  `json:"hand_id"`
	UserID      string  `json:"user_id"`
	Symbol      string  `json:"symbol"`
	Direction   string  `json:"direction"`
	ExitKind    string  `json:"exit_kind,omitempty"`
	Strength    float64 `json:"strength"`
	Price       string  `json:"price,omitempty"`
	TargetPrice string  `json:"target_price,omitempty"`
	StopPrice   string  `json:"stop_price,omitempty"`
	IsOffset    bool    `json:"is_offset,omitempty"`
	ATR         string  `json:"atr,omitempty"`
	// Reason is a human-readable explanation from the herald script (audit log only).
	Reason      string    `json:"reason,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
	ReceivedAt  time.Time `json:"received_at"`
}

// PublishSignal publishes msg to the HELM_SIGNALS JetStream stream (helm.signals.{helmID}).
// Nats-Msg-Id = msg.ID (a fresh UUID minted by the caller at publish time).
func PublishSignal(js nats.JetStreamContext, msg SignalMsg) {
	if msg.ID == "" {
		msg.ID = uuid.NewString()
	}
	data, _ := json.Marshal(msg)
	natMsg := &nats.Msg{
		Subject: fmt.Sprintf(SubjSignalReceived, msg.HelmID),
		Data:    data,
		Header:  nats.Header{},
	}
	natMsg.Header.Set(nats.MsgIdHdr, msg.ID)
	if _, err := js.PublishMsg(natMsg); err != nil {
		slog.Warn("signal publish failed", "subject", natMsg.Subject, "id", msg.ID, "err", err)
	}
}

// PublishPortfolioSync publishes a PortfolioSyncEvent to the PORTFOLIO_SYNC JetStream stream
// (portfolio.synced.{accountID}). Durable with 1-day retention so consumers that
// were briefly offline receive missed sync notifications on reconnect.
func PublishPortfolioSync(js nats.JetStreamContext, orchID, accountID, userID string, cash, availableCash, equity decimal.Decimal, positions []SyncedPositionMsg, transactions []TransactionMsg, syncedAt time.Time) {
	ev := PortfolioSyncEvent{
		OrchestratorID: orchID,
		AccountID:      accountID,
		UserID:         userID,
		Cash:           cash,
		AvailableCash:  availableCash,
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
//
// Position-level fields (PositionID, EntryPrice, PnLPct) are populated only for
// position lifecycle events (CodePositionOpened, CodePositionAdded, CodePositionClosed).
// Order-level fields (OrderID, Price) always refer to the triggering order.
type HelmEvent struct {
	HelmID          string          `json:"helm_id"`
	HandID          string          `json:"hand_id,omitempty"`
	UserID          string          `json:"user_id,omitempty"` // populated by EmitEvent; used by eventlog persister
	At              time.Time       `json:"at"`
	Code            int             `json:"code"`
	Symbol          string          `json:"symbol,omitempty"`
	Direction       string          `json:"direction,omitempty"`
	Side            string          `json:"side,omitempty"`
	Qty             decimal.Decimal `json:"qty,omitzero"`
	Price           decimal.Decimal `json:"price,omitzero"`
	OrderID         string          `json:"order_id,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	Msg             string          `json:"msg"`
	PnL             decimal.Decimal `json:"pnl,omitzero"`
	AvailableCash   decimal.Decimal `json:"available_cash,omitzero"`
	DeployedCapital decimal.Decimal `json:"deployed_capital,omitzero"`
	Equity          decimal.Decimal `json:"equity,omitzero"`

	// Position-level fields — populated for CodePosition* events only.
	PositionID string          `json:"position_id,omitempty"`
	EntryPrice decimal.Decimal `json:"entry_price,omitzero"` // avg entry price of the position
	PnLPct     decimal.Decimal `json:"pnl_pct,omitzero"`     // realized PnL as a fraction of deployed capital (FE ×100 for %)
}

// PublishHelmEvent publishes a HelmEvent to the HELM_EVENTS JetStream stream
// (helm.events.{helmID}). Durable — clients that were offline can replay recent events.
func PublishHelmEvent(js nats.JetStreamContext, helmID string, ev HelmEvent) {
	data, _ := json.Marshal(ev)
	subj := fmt.Sprintf(SubjHelmEvents, helmID)
	msg := nats.NewMsg(subj)
	msg.Data = data
	// Dedup key: helm + hand (empty string for helm-level events) + code + ts-ms.
	// hand_id is included so two hands under the same helm emitting the same code
	// at the same millisecond are NOT deduplicated by JetStream.
	msg.Header.Set(nats.MsgIdHdr, fmt.Sprintf("%s-%s-%d-%d", helmID, ev.HandID, ev.Code, ev.At.UnixMilli()))
	if _, err := js.PublishMsg(msg); err != nil {
		slog.Warn("helm event: jetstream publish failed", "helm_id", helmID, "code", ev.Code, "err", err)
	}
}
