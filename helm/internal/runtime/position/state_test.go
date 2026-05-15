package position_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/poslog"
	"mallow/helm/internal/runtime/position"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func d(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }

// mkPlaced creates an order_placed event.
// positionID = opening order_id of the leg (same as orderID for initial entries).
func mkPlaced(orderID, positionID, symbol, side string, sl, tp decimal.Decimal, isClose, isPyramidAdd bool) poslog.Event {
	p := poslog.OrderPlacedPayload{
		OrderID:      orderID,
		Symbol:       symbol,
		Side:         side,
		StopLoss:     sl.String(),
		TakeProfit:   tp.String(),
		IsClose:      isClose,
		IsPyramidAdd: isPyramidAdd,
	}
	b, _ := json.Marshal(p)
	return poslog.Event{
		ID:         orderID,
		PositionID: positionID,
		Kind:       poslog.KindOrderPlaced,
		Payload:    b,
		At:         time.Now(),
	}
}

// mkFilled creates an order_filled event.
func mkFilled(orderID, positionID string, price, qty decimal.Decimal) poslog.Event {
	p := poslog.OrderFilledPayload{
		OrderID:   orderID,
		FillPrice: price.String(),
		FillQty:   qty.String(),
		Source:    "ws",
	}
	b, _ := json.Marshal(p)
	return poslog.Event{
		ID:         orderID + "_filled",
		PositionID: positionID,
		Kind:       poslog.KindOrderFilled,
		Payload:    b,
		At:         time.Now(),
	}
}

// mkCancelled creates an order_cancelled event.
func mkCancelled(orderID, positionID string) poslog.Event {
	p := poslog.OrderCancelledPayload{OrderID: orderID, Reason: "cancelled"}
	b, _ := json.Marshal(p)
	return poslog.Event{
		ID:         orderID + "_cancelled",
		PositionID: positionID,
		Kind:       poslog.KindOrderCancelled,
		Payload:    b,
		At:         time.Now(),
	}
}

// mkPositionClosed creates a position_closed event (external close or kill).
func mkPositionClosed(positionID string, pnl decimal.Decimal) poslog.Event {
	p := poslog.PositionClosedPayload{
		OrderID:     positionID,
		ClosePrice:  "0",
		RealizedPnL: pnl.String(),
		Source:      "external",
	}
	b, _ := json.Marshal(p)
	return poslog.Event{
		ID:         positionID + "_ext_closed",
		PositionID: positionID,
		Kind:       poslog.KindPositionClosed,
		Payload:    b,
		At:         time.Now(),
	}
}

// mkSLUpdated creates an sl_updated event.
func mkSLUpdated(positionID string, sl, tp decimal.Decimal) poslog.Event {
	p := poslog.SLUpdatedPayload{
		OrderID: positionID,
		NewSL:   sl.String(),
		NewTP:   tp.String(),
		Reason:  "trailing",
	}
	b, _ := json.Marshal(p)
	return poslog.Event{
		ID:         positionID + "_sl_updated",
		PositionID: positionID,
		Kind:       poslog.KindSLUpdated,
		Payload:    b,
		At:         time.Now(),
	}
}

// mkOrphaned creates a position_orphaned event.
func mkOrphaned(positionID, symbol string) poslog.Event {
	p := poslog.PositionOrphanedPayload{Symbol: symbol, Source: "release"}
	b, _ := json.Marshal(p)
	return poslog.Event{
		ID:         positionID + "_orphaned",
		PositionID: positionID,
		Kind:       poslog.KindPositionOrphaned,
		Payload:    b,
		At:         time.Now(),
	}
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestReplayHand_EmptyLog(t *testing.T) {
	hp := position.ReplayHand(nil, false, 1)
	if !hp.IsFlat() {
		t.Fatal("empty log: want flat")
	}
}

// Crash scenario P1: process died after order_placed but before order_filled.
// On restart the reconciler sees PhaseEntering and must query the exchange.
func TestReplayHand_CrashAfterPlaced(t *testing.T) {
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", d("29000"), d("32000"), false, false),
	}
	hp := position.ReplayHand(events, false, 1)

	if hp.IsFlat() {
		t.Fatal("want not flat — hand has a pending entry order")
	}
	leg := hp.PrimaryLeg()
	if leg == nil {
		t.Fatal("want primary leg")
	}
	if leg.Phase != position.PhaseEntering {
		t.Fatalf("phase: want PhaseEntering, got %s", leg.Phase)
	}
	if leg.PendingOrderID != "ord1" {
		t.Fatalf("PendingOrderID: want ord1, got %s", leg.PendingOrderID)
	}
}

// Normal flow: entry order placed → filled → PhaseOpen with correct fields.
func TestReplayHand_EntryFill(t *testing.T) {
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", d("29000"), d("32000"), false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
	}
	hp := position.ReplayHand(events, false, 1)

	leg := hp.PrimaryLeg()
	if leg == nil {
		t.Fatal("want primary leg")
	}
	if leg.Phase != position.PhaseOpen {
		t.Fatalf("phase: want PhaseOpen, got %s", leg.Phase)
	}
	if !leg.EntryPrice.Equal(d("30000")) {
		t.Fatalf("EntryPrice: want 30000, got %s", leg.EntryPrice)
	}
	if !leg.Qty.Equal(d("0.1")) {
		t.Fatalf("Qty: want 0.1, got %s", leg.Qty)
	}
	if !leg.StopLoss.Equal(d("29000")) {
		t.Fatalf("StopLoss: want 29000, got %s", leg.StopLoss)
	}
	if !leg.TakeProfit.Equal(d("32000")) {
		t.Fatalf("TakeProfit: want 32000, got %s", leg.TakeProfit)
	}
}

// Entry order cancelled → flat.
func TestReplayHand_EntryCancelled(t *testing.T) {
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", decimal.Zero, decimal.Zero, false, false),
		mkCancelled("ord1", "ord1"),
	}
	hp := position.ReplayHand(events, false, 1)
	if !hp.IsFlat() {
		t.Fatal("want flat after entry cancel")
	}
}

// Crash scenario P2: close order placed but not yet filled.
// Reconciler sees PhaseExiting and must check whether close order filled or cancelled.
func TestReplayHand_CrashAfterClosePlaced(t *testing.T) {
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", decimal.Zero, decimal.Zero, false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
		mkPlaced("ord2", "ord1", "", "", decimal.Zero, decimal.Zero, true, false), // close order
	}
	hp := position.ReplayHand(events, false, 1)

	leg := hp.PrimaryLeg()
	if leg == nil {
		t.Fatal("want primary leg")
	}
	if leg.Phase != position.PhaseExiting {
		t.Fatalf("phase: want PhaseExiting, got %s", leg.Phase)
	}
	if leg.PendingOrderID != "ord2" {
		t.Fatalf("PendingOrderID: want ord2, got %s", leg.PendingOrderID)
	}
}

// Close order cancelled → position reverts to PhaseOpen.
func TestReplayHand_CancelledCloseReverts(t *testing.T) {
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", decimal.Zero, decimal.Zero, false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
		mkPlaced("ord2", "ord1", "", "", decimal.Zero, decimal.Zero, true, false),
		mkCancelled("ord2", "ord1"),
	}
	hp := position.ReplayHand(events, false, 1)

	leg := hp.PrimaryLeg()
	if leg == nil {
		t.Fatal("want primary leg")
	}
	if leg.Phase != position.PhaseOpen {
		t.Fatalf("phase: want PhaseOpen after cancel, got %s", leg.Phase)
	}
	if leg.PendingOrderID != "" {
		t.Fatalf("PendingOrderID: want empty, got %s", leg.PendingOrderID)
	}
}

// Full roundtrip: entry + fill + close + fill → flat, correct PnL (long).
func TestReplayHand_FullRoundtrip_PnLLong(t *testing.T) {
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", decimal.Zero, decimal.Zero, false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
		mkPlaced("ord2", "ord1", "", "", decimal.Zero, decimal.Zero, true, false),
		mkFilled("ord2", "ord1", d("31000"), d("0.1")),
	}
	hp := position.ReplayHand(events, false, 1)

	if !hp.IsFlat() {
		t.Fatal("want flat after full roundtrip")
	}
	// (31000 - 30000) * 0.1 = 100
	want := d("100")
	if !hp.RealizedPnL.Equal(want) {
		t.Fatalf("PnL long: want %s, got %s", want, hp.RealizedPnL)
	}
}

// Short position: sell at 30000, buy back at 29000 → profit 100.
func TestReplayHand_FullRoundtrip_PnLShort(t *testing.T) {
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "sell", decimal.Zero, decimal.Zero, false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
		mkPlaced("ord2", "ord1", "", "", decimal.Zero, decimal.Zero, true, false),
		mkFilled("ord2", "ord1", d("29000"), d("0.1")),
	}
	hp := position.ReplayHand(events, false, 1)

	if !hp.IsFlat() {
		t.Fatal("want flat")
	}
	// short: (30000 - 29000) * 0.1 = 100
	want := d("100")
	if !hp.RealizedPnL.Equal(want) {
		t.Fatalf("PnL short: want %s, got %s", want, hp.RealizedPnL)
	}
}

// External close event (liquidation / manual): leg goes flat with given PnL.
func TestReplayHand_ExternalClose(t *testing.T) {
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", decimal.Zero, decimal.Zero, false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
		mkPositionClosed("ord1", d("50")),
	}
	hp := position.ReplayHand(events, false, 1)

	if !hp.IsFlat() {
		t.Fatal("want flat after external close")
	}
	if !hp.RealizedPnL.Equal(d("50")) {
		t.Fatalf("PnL: want 50, got %s", hp.RealizedPnL)
	}
}

// position_closed applied twice (reconciler re-run) must be idempotent.
func TestReplayHand_ExternalClose_Idempotent(t *testing.T) {
	extClose := mkPositionClosed("ord1", d("50"))
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", decimal.Zero, decimal.Zero, false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
		extClose,
		extClose, // duplicate — simulates reconciler running twice before dedup takes effect
	}
	hp := position.ReplayHand(events, false, 1)

	if !hp.IsFlat() {
		t.Fatal("want flat")
	}
	if !hp.RealizedPnL.Equal(d("50")) {
		t.Fatalf("PnL must not double-count: want 50, got %s", hp.RealizedPnL)
	}
}

// SL/TP levels updated via sl_updated event.
func TestReplayHand_SLUpdated(t *testing.T) {
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", d("29000"), d("32000"), false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
		mkSLUpdated("ord1", d("29500"), d("32500")),
	}
	hp := position.ReplayHand(events, false, 1)

	leg := hp.PrimaryLeg()
	if leg == nil {
		t.Fatal("want primary leg")
	}
	if !leg.StopLoss.Equal(d("29500")) {
		t.Fatalf("StopLoss: want 29500, got %s", leg.StopLoss)
	}
	if !leg.TakeProfit.Equal(d("32500")) {
		t.Fatalf("TakeProfit: want 32500, got %s", leg.TakeProfit)
	}
}

// Pyramid mode: add 0.1 @ 32000 onto existing 0.1 @ 30000 → avg=31000, qty=0.2.
func TestReplayHand_PyramidAdd(t *testing.T) {
	addPayload := poslog.OrderPlacedPayload{
		OrderID:      "ord2",
		IsPyramidAdd: true,
		StopLoss:     d("28000").String(),
		TakeProfit:   d("36000").String(),
	}
	addBytes, _ := json.Marshal(addPayload)

	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", d("29000"), d("34000"), false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
		{ID: "ord2", PositionID: "ord1", Kind: poslog.KindOrderPlaced, Payload: addBytes, At: time.Now()},
		mkFilled("ord2", "ord1", d("32000"), d("0.1")),
	}
	hp := position.ReplayHand(events, true, 3)

	leg := hp.PrimaryLeg()
	if leg == nil {
		t.Fatal("want primary leg")
	}
	if leg.Phase != position.PhaseOpen {
		t.Fatalf("phase: want PhaseOpen, got %s", leg.Phase)
	}
	if !leg.Qty.Equal(d("0.2")) {
		t.Fatalf("Qty: want 0.2, got %s", leg.Qty)
	}
	// avg_entry = (0.1*30000 + 0.1*32000) / 0.2 = 31000
	if !leg.EntryPrice.Equal(d("31000")) {
		t.Fatalf("AvgEntry: want 31000, got %s", leg.EntryPrice)
	}
	// SL/TP from the add signal (replaces original)
	if !leg.StopLoss.Equal(d("28000")) {
		t.Fatalf("StopLoss after add: want 28000, got %s", leg.StopLoss)
	}
	if hp.EntryCount() != 2 {
		t.Fatalf("EntryCount: want 2, got %d", hp.EntryCount())
	}
}

// Pyramid add cancelled → leg stays open, qty/entry unchanged.
func TestReplayHand_PyramidAddCancelled(t *testing.T) {
	addPayload := poslog.OrderPlacedPayload{
		OrderID:      "ord2",
		IsPyramidAdd: true,
		StopLoss:     d("28000").String(),
		TakeProfit:   d("36000").String(),
	}
	addBytes, _ := json.Marshal(addPayload)

	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", d("29000"), d("33000"), false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
		{ID: "ord2", PositionID: "ord1", Kind: poslog.KindOrderPlaced, Payload: addBytes, At: time.Now()},
		mkCancelled("ord2", "ord1"),
	}
	hp := position.ReplayHand(events, true, 3)

	leg := hp.PrimaryLeg()
	if leg == nil {
		t.Fatal("want primary leg")
	}
	if leg.Phase != position.PhaseOpen {
		t.Fatalf("phase: want PhaseOpen, got %s", leg.Phase)
	}
	if !leg.Qty.Equal(d("0.1")) {
		t.Fatalf("Qty unchanged: want 0.1, got %s", leg.Qty)
	}
	if !leg.EntryPrice.Equal(d("30000")) {
		t.Fatalf("EntryPrice unchanged: want 30000, got %s", leg.EntryPrice)
	}
	if leg.PendingOrderID != "" {
		t.Fatalf("PendingOrderID: want empty, got %s", leg.PendingOrderID)
	}
}

// Non-pyramid mode: two independent legs coexist.
func TestReplayHand_NonPyramid_TwoLegs(t *testing.T) {
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", decimal.Zero, decimal.Zero, false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
		mkPlaced("ord2", "ord2", "BTCUSDT", "buy", decimal.Zero, decimal.Zero, false, false),
		mkFilled("ord2", "ord2", d("31000"), d("0.1")),
	}
	hp := position.ReplayHand(events, false, 2)

	if hp.ActiveCount() != 2 {
		t.Fatalf("ActiveCount: want 2, got %d", hp.ActiveCount())
	}
	if hp.EntryCount() != 2 {
		t.Fatalf("EntryCount: want 2, got %d", hp.EntryCount())
	}
}

// position_orphaned removes the leg — reconciler never restores it.
func TestReplayHand_Orphaned(t *testing.T) {
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", decimal.Zero, decimal.Zero, false, false),
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
		mkOrphaned("ord1", "BTCUSDT"),
	}
	hp := position.ReplayHand(events, false, 1)

	if !hp.IsFlat() {
		t.Fatal("want flat after orphaned — hand must not reclaim this position on restart")
	}
}

// Invalid event in the middle (e.g. corrupted log entry) is skipped — replay is best-effort.
func TestReplayHand_InvalidEventSkipped(t *testing.T) {
	bad := poslog.Event{
		ID:         "bad",
		PositionID: "nonexistent",
		Kind:       poslog.KindOrderFilled, // no preceding order_placed
		Payload:    []byte(`{}`),
		At:         time.Now(),
	}
	events := []poslog.Event{
		mkPlaced("ord1", "ord1", "BTCUSDT", "buy", decimal.Zero, decimal.Zero, false, false),
		bad,
		mkFilled("ord1", "ord1", d("30000"), d("0.1")),
	}
	hp := position.ReplayHand(events, false, 1)

	// The good events should still apply — bad event is skipped.
	if hp.IsFlat() {
		t.Fatal("want not flat — good events should apply despite bad one")
	}
	leg := hp.PrimaryLeg()
	if leg == nil || leg.Phase != position.PhaseOpen {
		t.Fatalf("want PhaseOpen, got %v", leg)
	}
}
