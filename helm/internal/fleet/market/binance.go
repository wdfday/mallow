package market

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

const (
	binanceWsBase        = "wss://stream.binance.com:9443/stream"
	binanceReconnectWait = 5 * time.Second
)

// streamBinance connects to Binance's combined public WebSocket stream and
// blocks until ctx is cancelled, reconnecting automatically on failure.
// Symbols should be Binance trading pairs, e.g. "BTCUSDT", "ETHUSDT".
//
// One connection carries both the trade-price channel (@trade) and the L2
// top-5 book channel (@depth5@100ms) for every symbol — this is the only
// WebSocket helm opens against Binance.
func streamBinance(ctx context.Context, symbols []string, onTick tickHandler, onBook bookHandler) error {
	if len(symbols) == 0 {
		return fmt.Errorf("binance listener: no symbols provided")
	}
	for {
		if err := binanceConnect(ctx, symbols, onTick, onBook); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("binance listener: disconnected, reconnecting", "err", err, "wait", binanceReconnectWait)
			select {
			case <-time.After(binanceReconnectWait):
			case <-ctx.Done():
				return nil
			}
		}
	}
}

func binanceConnect(ctx context.Context, symbols []string, onTick tickHandler, onBook bookHandler) error {
	// Build combined stream URL: btcusdt@trade/btcusdt@depth5@100ms/...
	streams := make([]string, 0, len(symbols)*2)
	for _, s := range symbols {
		lower := strings.ToLower(s)
		streams = append(streams, lower+"@trade", lower+"@depth5@100ms")
	}
	url := binanceWsBase + "?streams=" + strings.Join(streams, "/")

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	slog.Info("binance listener: connected", "symbols", symbols)

	for {
		if ctx.Err() != nil {
			return nil
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		binanceHandleMessage(msg, onTick, onBook)
	}
}

// binanceHandleMessage parses a Binance combined stream message and dispatches
// it to onTick (trade) or onBook (partial depth) based on the stream name.
func binanceHandleMessage(msg []byte, onTick tickHandler, onBook bookHandler) {
	var env struct {
		Stream string          `json:"stream"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		return
	}

	switch {
	case strings.HasSuffix(env.Stream, "@trade"):
		var trade binanceTradeMsg
		if err := json.Unmarshal(env.Data, &trade); err != nil || trade.EventType != "trade" || trade.Symbol == "" {
			return
		}
		if px, err := decimal.NewFromString(trade.Price); err == nil && px.IsPositive() {
			onTick(strings.ToUpper(trade.Symbol), px)
		}

	case strings.Contains(env.Stream, "@depth5"):
		var depth binanceDepthMsg
		if err := json.Unmarshal(env.Data, &depth); err != nil {
			return
		}
		// Stream name carries the symbol (data itself has no symbol field):
		// "btcusdt@depth5@100ms" → "BTCUSDT".
		sym := strings.ToUpper(strings.SplitN(env.Stream, "@", 2)[0])
		if snap, ok := binanceToL2Snapshot(sym, depth); ok {
			onBook(sym, snap)
		}
	}
}

// binanceToL2Snapshot converts a partial-depth (depth5) push — already a
// flattened top-5 snapshot from Binance, no local merge needed — into an
// exchange.L2Snapshot.
func binanceToL2Snapshot(symbol string, d binanceDepthMsg) (exchange.L2Snapshot, bool) {
	snap := exchange.L2Snapshot{Symbol: symbol, Timestamp: time.Now().UTC()}
	n := 0
	for i := 0; i < 5 && i < len(d.Bids); i++ {
		lvl, ok := parseBinanceLevel(d.Bids[i])
		if !ok {
			continue
		}
		snap.Bids[i] = lvl
		n++
	}
	for i := 0; i < 5 && i < len(d.Asks); i++ {
		lvl, ok := parseBinanceLevel(d.Asks[i])
		if !ok {
			continue
		}
		snap.Asks[i] = lvl
		n++
	}
	return snap, n > 0
}

func parseBinanceLevel(pair [2]string) (exchange.L2Level, bool) {
	price, err := decimal.NewFromString(pair[0])
	if err != nil {
		return exchange.L2Level{}, false
	}
	size, err := decimal.NewFromString(pair[1])
	if err != nil {
		return exchange.L2Level{}, false
	}
	return exchange.L2Level{Price: price, Size: size}, true
}

type binanceTradeMsg struct {
	EventType string `json:"e"`
	Symbol    string `json:"s"`
	Price     string `json:"p"`
	Qty       string `json:"q"`
	TradeTime int64  `json:"T"`
}

// binanceDepthMsg is the partial-depth (depth5/depth10/depth20 @100ms or
// @1000ms) push payload — always a full top-N snapshot, not an incremental
// diff, so no local order-book merge is required.
type binanceDepthMsg struct {
	LastUpdateID int64       `json:"lastUpdateId"`
	Bids         [][2]string `json:"bids"`
	Asks         [][2]string `json:"asks"`
}
