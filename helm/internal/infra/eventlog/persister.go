package eventlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/safe"
)

// Persister subscribes to the HELM_EVENTS JetStream stream with a durable
// push consumer and writes each event to PostgreSQL. It runs as a background
// goroutine and exits when ctx is cancelled.
//
// This is the only writer to helm_events. HelmRuntime stays clean — it publishes
// to JetStream as before; Persister handles the Postgres fan-out independently.
type Persister struct {
	js  nats.JetStreamContext
	db  *sql.DB
	log Log
}

// NewPersister creates a Persister. Call Run() to start it.
func NewPersister(js nats.JetStreamContext, db *sql.DB) *Persister {
	return &Persister{
		js:  js,
		db:  db,
		log: New(db),
	}
}

// Run subscribes to HELM_EVENTS and persists each message until ctx is cancelled.
// The JetStream push consumer is ephemeral (no durable name) — on restart the
// persister will re-consume from the last ack'd sequence. Use a durable name
// so missed events during downtime are replayed from JetStream retention (7d).
func (p *Persister) Run(ctx context.Context) {
	defer safe.Recover()
	sub, err := p.js.Subscribe(
		"helm.events.>",
		p.handleMsg,
		nats.Durable("helm-events-persister"),
		nats.DeliverNew(),
		nats.AckExplicit(),
		nats.MaxDeliver(3),
		nats.BindStream("HELM_EVENTS"),
	)
	if err != nil {
		slog.Error("eventlog persister: subscribe failed", "err", err)
		return
	}
	defer sub.Unsubscribe() //nolint:errcheck

	slog.Info("eventlog persister: started — helm.events.> → postgres")
	<-ctx.Done()
	slog.Info("eventlog persister: stopped")
}

// Event codes that require side-effects beyond inserting into helm_events.
// Defined locally to avoid importing the runtime package (circular dep).
const (
	codeHelmHalted   = 10303
	codeHelmUnhalted = 10304
)

func (p *Persister) handleMsg(msg *nats.Msg) {
	var ev natsapi.HelmEvent
	if err := json.Unmarshal(msg.Data, &ev); err != nil {
		slog.Warn("eventlog persister: unmarshal failed", "err", err)
		_ = msg.Ack()
		return
	}

	helmID, err := uuid.Parse(ev.HelmID)
	if err != nil {
		_ = msg.Ack() // malformed: ack and skip
		return
	}

	// UserID is populated by HelmRuntime.EmitEvent from r.UserID.
	userID, _ := uuid.Parse(ev.UserID)

	var handID *uuid.UUID
	if ev.HandID != "" {
		if parsed, err := uuid.Parse(ev.HandID); err == nil {
			handID = &parsed
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Wrap insert + optional status update in a single transaction so both
	// succeed or both fail — avoids a window where the event is persisted but
	// the helms.status update is missed on a mid-handler crash.
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("eventlog persister: begin tx failed — will retry", "helm_id", ev.HelmID, "err", err)
		_ = msg.Nak()
		return
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO helm_events
			(at, helm_id, hand_id, user_id, code, symbol, direction, side, qty, price, order_id, reason, msg)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (helm_id, COALESCE(hand_id, '00000000-0000-0000-0000-000000000000'), code, at) DO NOTHING`,
		ev.At,
		helmID,
		handID,
		userID,
		ev.Code,
		nullStr(ev.Symbol),
		nullStr(ev.Direction),
		nullStr(ev.Side),
		nullDecimal(ev.Qty),
		nullDecimal(ev.Price),
		nullStr(ev.OrderID),
		nullStr(ev.Reason),
		nullStr(ev.Msg),
	)
	if err != nil {
		_ = tx.Rollback()
		slog.Warn("eventlog persister: insert failed — will retry", "helm_id", ev.HelmID, "code", ev.Code, "err", err)
		_ = msg.Nak()
		return
	}

	// Side-effect: sync halted/active status to helms table so FE reflects the
	// correct state and survives process restarts without a runtime query.
	// Runs inside the same transaction — either both land or neither does.
	switch ev.Code {
	case codeHelmHalted:
		if _, err := tx.ExecContext(ctx,
			`UPDATE helms SET status = 'halted', updated_at = now() WHERE id = $1 AND status = 'active'`,
			helmID,
		); err != nil {
			_ = tx.Rollback()
			slog.Warn("eventlog persister: failed to persist halted status — will retry", "helm_id", ev.HelmID, "err", err)
			_ = msg.Nak()
			return
		}
	case codeHelmUnhalted:
		if _, err := tx.ExecContext(ctx,
			`UPDATE helms SET status = 'active', updated_at = now() WHERE id = $1 AND status = 'halted'`,
			helmID,
		); err != nil {
			_ = tx.Rollback()
			slog.Warn("eventlog persister: failed to persist unhalted status — will retry", "helm_id", ev.HelmID, "err", err)
			_ = msg.Nak()
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Warn("eventlog persister: commit failed — will retry", "helm_id", ev.HelmID, "err", err)
		_ = msg.Nak()
		return
	}

	_ = msg.Ack()
}
