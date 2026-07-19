package market

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

const (
	bybitWsURL         = "wss://stream.bybit.com/v5/public/spot"
	bybitReconnectWait = 5 * time.Second
	bybitPingInterval  = 20 * time.Second
	bybitBookDepth     = "50" // orderbook.50.{symbol} — Bybit spot depth tiers: 1, 50, 200, 500
)

// streamBybit connects to Bybit's public spot WebSocket and blocks until ctx
// is cancelled, reconnecting automatically on failure. Symbols should be
// Bybit trading pairs, e.g. "BTCUSDT".
//
// One connection carries both the "publicTrade" channel and the
// "orderbook.50" channel for every symbol — this is the only WebSocket helm
// opens against Bybit. Unlike Binance/OKX, Bybit's orderbook channel is
// snapshot+delta, not a pre-flattened top-N push, so this maintains a small
// local per-symbol order book (bybitBook) and re-derives the top-5 on every
// message.
func streamBybit(ctx context.Context, symbols []string, onTick tickHandler, onBook bookHandler) error {
	if len(symbols) == 0 {
		return fmt.Errorf("bybit listener: no symbols provided")
	}
	for {
		if err := bybitConnect(ctx, symbols, onTick, onBook); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("bybit listener: disconnected, reconnecting", "err", err, "wait", bybitReconnectWait)
			select {
			case <-time.After(bybitReconnectWait):
			case <-ctx.Done():
				return nil
			}
		}
	}
}

func bybitConnect(ctx context.Context, symbols []string, onTick tickHandler, onBook bookHandler) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, bybitWsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Subscribe to both publicTrade and orderbook.50 topics for each symbol.
	args := make([]string, 0, len(symbols)*2)
	for _, s := range symbols {
		args = append(args, "publicTrade."+s, "orderbook."+bybitBookDepth+"."+s)
	}
	sub := bybitSubRequest{Op: "subscribe", Args: args}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	slog.Info("bybit listener: connected", "symbols", symbols)

	// Ping goroutine (Bybit disconnects after ~30s idle).
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go bybitPinger(pingCtx, conn)

	// Local order-book merge state, one per symbol, scoped to this connection —
	// a fresh reconnect starts clean and waits for the next "snapshot" message
	// before trusting any "delta".
	books := make(map[string]*bybitBook, len(symbols))

	for {
		if ctx.Err() != nil {
			return nil
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		bybitHandleMessage(msg, books, onTick, onBook)
	}
}

// bybitHandleMessage parses a single Bybit WebSocket message and dispatches
// it to onTick (publicTrade topic) or onBook (orderbook topic, after
// applying the snapshot/delta to the local merged book).
func bybitHandleMessage(msg []byte, books map[string]*bybitBook, onTick tickHandler, onBook bookHandler) {
	var push bybitPush
	if err := json.Unmarshal(msg, &push); err != nil {
		return
	}

	switch {
	case len(push.Topic) >= len("publicTrade.") && push.Topic[:len("publicTrade.")] == "publicTrade.":
		var trades []bybitTradeMsg
		if err := json.Unmarshal(push.Data, &trades); err != nil {
			return
		}
		for _, t := range trades {
			px, err := decimal.NewFromString(t.Price)
			if err != nil || !px.IsPositive() {
				continue
			}
			onTick(t.Symbol, px)
		}

	case len(push.Topic) >= len("orderbook.") && push.Topic[:len("orderbook.")] == "orderbook.":
		var book bybitOrderbookMsg
		if err := json.Unmarshal(push.Data, &book); err != nil || book.Symbol == "" {
			return
		}
		b, ok := books[book.Symbol]
		if !ok {
			b = newBybitBook()
			books[book.Symbol] = b
		}
		switch push.Type {
		case "snapshot":
			b.applySnapshot(book)
		case "delta":
			b.applyDelta(book)
		default:
			return
		}
		onBook(book.Symbol, b.top5(book.Symbol))
	}
}

func bybitPinger(ctx context.Context, conn *websocket.Conn) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("goroutine panic recovered", "recover", r)
		}
	}()
	ticker := time.NewTicker(bybitPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := conn.WriteJSON(bybitPingMsg{Op: "ping"}); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// ── bybitBook: local snapshot+delta merge ─────────────────────────────────────

// bybitBook is a per-symbol local order book built from Bybit's
// snapshot+delta orderbook channel. Bybit sends one "snapshot" message
// (full book at subscribe time) followed by "delta" messages (incremental
// upserts/removals); a level with size "0" in a delta means "remove this
// price". Keyed by price string (not decimal) to avoid float/decimal
// hashing pitfalls — the exchange's own string representation is the
// natural, unambiguous map key.
type bybitBook struct {
	bids map[string]decimal.Decimal // price string → size
	asks map[string]decimal.Decimal
}

func newBybitBook() *bybitBook {
	return &bybitBook{
		bids: make(map[string]decimal.Decimal),
		asks: make(map[string]decimal.Decimal),
	}
}

// applySnapshot replaces the entire book with the pushed levels.
func (b *bybitBook) applySnapshot(msg bybitOrderbookMsg) {
	b.bids = make(map[string]decimal.Decimal, len(msg.Bids))
	b.asks = make(map[string]decimal.Decimal, len(msg.Asks))
	b.applyDelta(msg)
}

// applyDelta upserts each level; a zero size removes the price level.
func (b *bybitBook) applyDelta(msg bybitOrderbookMsg) {
	applyLevels(b.bids, msg.Bids)
	applyLevels(b.asks, msg.Asks)
}

func applyLevels(book map[string]decimal.Decimal, levels [][2]string) {
	for _, lvl := range levels {
		priceStr, sizeStr := lvl[0], lvl[1]
		size, err := decimal.NewFromString(sizeStr)
		if err != nil {
			continue
		}
		if size.IsZero() {
			delete(book, priceStr)
			continue
		}
		book[priceStr] = size
	}
}

// top5 extracts the current top-5 bids (descending) and asks (ascending)
// from the merged book into an exchange.L2Snapshot.
func (b *bybitBook) top5(symbol string) exchange.L2Snapshot {
	snap := exchange.L2Snapshot{Symbol: symbol, Timestamp: time.Now().UTC()}
	bids := sortedLevels(b.bids, true)
	asks := sortedLevels(b.asks, false)
	for i := 0; i < 5 && i < len(bids); i++ {
		snap.Bids[i] = bids[i]
	}
	for i := 0; i < 5 && i < len(asks); i++ {
		snap.Asks[i] = asks[i]
	}
	return snap
}

// sortedLevels parses and sorts a price→size map into L2Levels.
// descending=true for bids (best bid = highest price first), false for asks
// (best ask = lowest price first).
func sortedLevels(book map[string]decimal.Decimal, descending bool) []exchange.L2Level {
	out := make([]exchange.L2Level, 0, len(book))
	for priceStr, size := range book {
		price, err := decimal.NewFromString(priceStr)
		if err != nil {
			continue
		}
		out = append(out, exchange.L2Level{Price: price, Size: size})
	}
	sort.Slice(out, func(i, j int) bool {
		if descending {
			return out[i].Price.GreaterThan(out[j].Price)
		}
		return out[i].Price.LessThan(out[j].Price)
	})
	return out
}

// ── Bybit API message types ───────────────────────────────────────────────────

type bybitSubRequest struct {
	Op   string   `json:"op"`
	Args []string `json:"args"`
}

type bybitPingMsg struct {
	Op string `json:"op"`
}

type bybitPush struct {
	Topic string          `json:"topic"`
	Type  string          `json:"type"`
	Ts    int64           `json:"ts"`
	Data  json.RawMessage `json:"data"`
}

type bybitTradeMsg struct {
	Timestamp int64  `json:"T"`
	Symbol    string `json:"s"`
	Side      string `json:"S"`
	Qty       string `json:"v"`
	Price     string `json:"p"`
}

// bybitOrderbookMsg is the "data" payload of an orderbook.{depth}.{symbol}
// push — either a full "snapshot" or an incremental "delta" (see bybitPush.Type).
// A level's size of "0" in a delta means the price level is removed.
type bybitOrderbookMsg struct {
	Symbol string      `json:"s"`
	Bids   [][2]string `json:"b"`
	Asks   [][2]string `json:"a"`
	Update int64       `json:"u"`
	Seq    int64       `json:"seq"`
}
