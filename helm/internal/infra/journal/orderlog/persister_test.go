package orderlog

// Unit tests for the orders persister projection: each poslog order event must issue
// the right SQL with the right key. Uses go-sqlmock — no real Postgres.
//
// go test ./internal/infra/orderlog/ -run TestPersister -count=1

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/journal/poslog"
)

func eventMsg(t *testing.T, e poslog.Event) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return &nats.Msg{Data: data}
}

func payload(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

// order_placed → INSERT INTO orders, keyed by exchange order id, carrying the clid.
func TestPersister_OrderPlaced_Inserts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := NewPersister(nil, db)

	helmID := uuid.New()
	handID := uuid.New()
	e := poslog.Event{
		HelmID:  helmID.String(),
		HandID:  handID.String(),
		TradeID: "ex-1",
		Kind:    poslog.KindOrderPlaced,
		At:      time.Now().UTC(),
		Payload: payload(t, poslog.OrderPlacedPayload{
			OrderID:       "ex-1",
			ClientOrderID: "mlwabc123",
			Symbol:        "BTCUSDT",
			Side:          "buy",
			Qty:           "0.5",
			Price:         "0",
			OrderType:     "market",
		}),
	}

	mock.ExpectExec("INSERT INTO orders").
		WithArgs(
			"ex-1", "mlwabc123", helmID.String(), handID.String(), "ex-1",
			"BTCUSDT", "buy", "market", "0.5", nil, false, e.At,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := p.handle(context.Background(), eventMsg(t, e)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// order_filled → UPDATE keyed by exchange order id (the payload carries no clid).
func TestPersister_OrderFilled_Updates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := NewPersister(nil, db)

	e := poslog.Event{
		HelmID: uuid.New().String(),
		Kind:   poslog.KindOrderFilled,
		At:     time.Now().UTC(),
		Payload: payload(t, poslog.OrderFilledPayload{
			OrderID:   "ex-1",
			FillQty:   "0.5",
			FillPrice: "50000",
		}),
	}

	mock.ExpectExec("UPDATE orders").
		WithArgs("ex-1", "0.5", "50000").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := p.handle(context.Background(), eventMsg(t, e)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// order_cancelled → UPDATE status='cancelled' keyed by exchange order id.
func TestPersister_OrderCancelled_Updates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := NewPersister(nil, db)

	e := poslog.Event{
		HelmID: uuid.New().String(),
		Kind:   poslog.KindOrderCancelled,
		At:     time.Now().UTC(),
		Payload: payload(t, poslog.OrderCancelledPayload{
			OrderID: "ex-1",
			Reason:  "rejected",
		}),
	}

	mock.ExpectExec("UPDATE orders").
		WithArgs("ex-1", "rejected").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := p.handle(context.Background(), eventMsg(t, e)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// filled with no matching placed row (0 rows affected) must NOT error — the gap is
// tolerated, not retried forever.
func TestPersister_FilledWithNoRow_NoError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := NewPersister(nil, db)

	e := poslog.Event{
		HelmID:  uuid.New().String(),
		Kind:    poslog.KindOrderFilled,
		At:      time.Now().UTC(),
		Payload: payload(t, poslog.OrderFilledPayload{OrderID: "ghost", FillQty: "1", FillPrice: "10"}),
	}
	mock.ExpectExec("UPDATE orders").
		WithArgs("ghost", "1", "10").
		WillReturnResult(sqlmock.NewResult(0, 0)) // no row matched

	if err := p.handle(context.Background(), eventMsg(t, e)); err != nil {
		t.Fatalf("handle should tolerate missing row, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// Non-order kinds (sl_updated, position_closed, …) and malformed payloads are ignored —
// no SQL issued, no error. sqlmock with zero expectations fails if any query runs.
func TestPersister_NonOrderKinds_Ignored(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := NewPersister(nil, db)

	cases := []poslog.Event{
		{HelmID: uuid.New().String(), Kind: poslog.KindPositionClosed, At: time.Now().UTC(), Payload: []byte(`{}`)},
		{HelmID: uuid.New().String(), Kind: poslog.KindSLUpdated, At: time.Now().UTC(), Payload: []byte(`{}`)},
		{HelmID: uuid.New().String(), Kind: poslog.KindOrderPlaced, At: time.Now().UTC(), Payload: []byte(`{bad json`)},
	}
	for _, e := range cases {
		if err := p.handle(context.Background(), eventMsg(t, e)); err != nil {
			t.Fatalf("handle(%s) should be a no-op, got: %v", e.Kind, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expected no SQL for non-order/malformed events: %v", err)
	}
}
