package orderlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/poslog"
)

const (
	stream           = "HELM_POSITIONS"
	subjectFilter    = "helm.pos.>"
	persisterDurable = "helm-orders-persister"
)

// Persister drains order lifecycle events from helm.pos.> and projects them into
// the `orders` table. It is the only writer to that table. One durable consumer,
// at-least-once with NAK-on-error so a transient PG outage doesn't drop events.
type Persister struct {
	js nats.JetStreamContext
	db *sql.DB
}

// NewPersister wires the subscriber. Call Run() to start the loop.
func NewPersister(js nats.JetStreamContext, db *sql.DB) *Persister {
	return &Persister{js: js, db: db}
}

// Run consumes helm.pos.> until ctx is cancelled.
func (p *Persister) Run(ctx context.Context) {
	msgCh := make(chan *nats.Msg, 256)
	sub, err := p.js.ChanSubscribe(
		subjectFilter,
		msgCh,
		nats.Durable(persisterDurable),
		nats.DeliverAll(),
		nats.AckExplicit(),
		nats.AckWait(30*time.Second),
		nats.MaxDeliver(-1),
		nats.BindStream(stream),
	)
	if err != nil {
		slog.Error("orders persister: subscribe failed", "err", err)
		return
	}
	defer sub.Unsubscribe() //nolint:errcheck

	slog.Info("orders persister: started — helm.pos.> → orders table")

	for {
		select {
		case <-ctx.Done():
			slog.Info("orders persister: stopped")
			return
		case msg := <-msgCh:
			if err := p.handle(ctx, msg); err != nil {
				slog.Warn("orders persister: apply failed — NAK for retry", "err", err)
				_ = msg.Nak()
				continue
			}
			_ = msg.Ack()
		}
	}
}

// handle decodes one poslog event and upserts the corresponding orders row.
// Non-order kinds (sl_updated, position_closed, bracket_placed, …) are acked and
// skipped. Unmarshal failures are acked too — a poison message must not block the
// stream.
func (p *Persister) handle(ctx context.Context, msg *nats.Msg) error {
	var e poslog.Event
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		slog.Warn("orders persister: bad event — skipping", "err", err)
		return nil // ack: don't retry a malformed message forever
	}

	switch e.Kind {
	case poslog.KindOrderPlaced:
		var pl poslog.OrderPlacedPayload
		if err := json.Unmarshal(e.Payload, &pl); err != nil {
			return nil
		}
		return p.upsertPlaced(ctx, e, pl)
	case poslog.KindOrderFilled:
		var pl poslog.OrderFilledPayload
		if err := json.Unmarshal(e.Payload, &pl); err != nil {
			return nil
		}
		return p.markFilled(ctx, pl)
	case poslog.KindOrderCancelled:
		var pl poslog.OrderCancelledPayload
		if err := json.Unmarshal(e.Payload, &pl); err != nil {
			return nil
		}
		return p.markCancelled(ctx, pl)
	default:
		return nil // not an order event
	}
}

func (p *Persister) upsertPlaced(ctx context.Context, e poslog.Event, pl poslog.OrderPlacedPayload) error {
	if pl.OrderID == "" {
		return nil
	}
	const q = `
		INSERT INTO orders
			(exchange_order_id, client_order_id, helm_id, hand_id, position_id,
			 symbol, side, order_type, qty, price, status, is_close, pattern_kind,
			 placed_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'placed',$11,$12,$13,NOW())
		ON CONFLICT (exchange_order_id) DO UPDATE SET
			client_order_id = EXCLUDED.client_order_id,
			position_id     = EXCLUDED.position_id,
			symbol          = EXCLUDED.symbol,
			side            = EXCLUDED.side,
			order_type      = EXCLUDED.order_type,
			qty             = EXCLUDED.qty,
			price           = EXCLUDED.price,
			is_close        = EXCLUDED.is_close,
			pattern_kind    = EXCLUDED.pattern_kind,
			placed_at       = EXCLUDED.placed_at,
			updated_at      = NOW()
		WHERE orders.status NOT IN ('filled','cancelled')`
	tCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := p.db.ExecContext(tCtx, q,
		pl.OrderID,
		nullableText(pl.ClientOrderID),
		e.HelmID,
		nullableUUID(e.HandID),
		nullableText(e.PositionID),
		pl.Symbol,
		nullableText(pl.Side),
		nullableText(pl.OrderType),
		nullableNumeric(pl.Qty),
		nullableNumeric(pl.Price),
		pl.IsClose,
		nullableText(pl.PatternKind),
		e.At,
	)
	return err
}

func (p *Persister) markFilled(ctx context.Context, pl poslog.OrderFilledPayload) error {
	if pl.OrderID == "" {
		return nil
	}
	const q = `
		UPDATE orders
		SET status = 'filled', filled_qty = $2, filled_price = $3, updated_at = NOW()
		WHERE exchange_order_id = $1`
	tCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := p.db.ExecContext(tCtx, q, pl.OrderID, nullableNumeric(pl.FillQty), nullableNumeric(pl.FillPrice))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Placed event not yet projected (gap or out-of-order) — log and let it be;
		// the placed row will arrive with its own status. Not an error.
		slog.Debug("orders persister: fill with no placed row", "order_id", pl.OrderID)
	}
	return nil
}

func (p *Persister) markCancelled(ctx context.Context, pl poslog.OrderCancelledPayload) error {
	if pl.OrderID == "" {
		return nil
	}
	const q = `
		UPDATE orders
		SET status = 'cancelled', reason = $2, updated_at = NOW()
		WHERE exchange_order_id = $1 AND orders.status <> 'filled'`
	tCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := p.db.ExecContext(tCtx, q, pl.OrderID, nullableText(pl.Reason))
	return err
}
