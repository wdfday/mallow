package market

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

const (
	okxWsURL         = "wss://ws.okx.com:8443/ws/v5/public"
	okxReconnectWait = 5 * time.Second
	okxPingInterval  = 25 * time.Second
)

// streamOKX connects to OKX's public WebSocket and blocks until ctx is
// cancelled, reconnecting automatically on failure. Symbols should be OKX
// instrument IDs, e.g. "BTC-USDT".
//
// One connection carries both the "trades" channel and the "books5" L2
// top-5 channel for every symbol — this is the only WebSocket helm opens
// against OKX.
func streamOKX(ctx context.Context, symbols []string, onTick tickHandler, onBook bookHandler) error {
	if len(symbols) == 0 {
		return fmt.Errorf("okx listener: no symbols provided")
	}
	for {
		if err := okxConnect(ctx, symbols, onTick, onBook); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("okx listener: disconnected, reconnecting", "err", err, "wait", okxReconnectWait)
			select {
			case <-time.After(okxReconnectWait):
			case <-ctx.Done():
				return nil
			}
		}
	}
}

func okxConnect(ctx context.Context, symbols []string, onTick tickHandler, onBook bookHandler) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, okxWsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Subscribe to both trades and books5 channels for each instrument.
	args := make([]okxSubArg, 0, len(symbols)*2)
	for _, s := range symbols {
		args = append(args, okxSubArg{Channel: "trades", InstID: s}, okxSubArg{Channel: "books5", InstID: s})
	}
	sub := okxSubRequest{Op: "subscribe", Args: args}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	slog.Info("okx listener: connected", "symbols", symbols)

	// Ping goroutine to keep connection alive.
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go okxPinger(pingCtx, conn)

	for {
		if ctx.Err() != nil {
			return nil
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		okxHandleMessage(msg, onTick, onBook)
	}
}

// okxHandleMessage parses a single OKX WebSocket message and dispatches it to
// onTick (trades channel) or onBook (books5 channel).
func okxHandleMessage(msg []byte, onTick tickHandler, onBook bookHandler) {
	raw := strings.TrimSpace(string(msg))
	if raw == "pong" {
		return
	}

	var envelope struct {
		Arg  okxSubArg       `json:"arg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(msg, &envelope); err != nil {
		slog.Debug("okx listener: parse error", "err", err, "msg", raw)
		return
	}

	switch envelope.Arg.Channel {
	case "trades":
		var trades []okxTradeMsg
		if err := json.Unmarshal(envelope.Data, &trades); err != nil {
			return
		}
		for _, t := range trades {
			px, err := decimal.NewFromString(t.Px)
			if err != nil || !px.IsPositive() {
				continue
			}
			onTick(t.InstID, px)
		}

	case "books5":
		var books []okxBooks5Msg
		if err := json.Unmarshal(envelope.Data, &books); err != nil {
			return
		}
		for _, b := range books {
			if snap, ok := okxToL2Snapshot(b); ok {
				onBook(b.InstID, snap)
			}
		}
	}
}

// okxToL2Snapshot converts a books5 push — already a flattened top-5
// snapshot from OKX, no local merge needed — into an exchange.L2Snapshot.
func okxToL2Snapshot(b okxBooks5Msg) (exchange.L2Snapshot, bool) {
	snap := exchange.L2Snapshot{Symbol: b.InstID, Timestamp: okxParseTs(b.Ts)}
	n := 0
	for i := 0; i < 5 && i < len(b.Bids); i++ {
		lvl, ok := parseOKXLevel(b.Bids[i])
		if !ok {
			continue
		}
		snap.Bids[i] = lvl
		n++
	}
	for i := 0; i < 5 && i < len(b.Asks); i++ {
		lvl, ok := parseOKXLevel(b.Asks[i])
		if !ok {
			continue
		}
		snap.Asks[i] = lvl
		n++
	}
	return snap, n > 0
}

func okxParseTs(ts string) time.Time {
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return time.Now().UTC()
	}
	return time.UnixMilli(ms).UTC()
}

// parseOKXLevel reads a books5 level [price, size, deprecatedLiquidatedCount, numOrders].
func parseOKXLevel(level []string) (exchange.L2Level, bool) {
	if len(level) < 2 {
		return exchange.L2Level{}, false
	}
	price, err := decimal.NewFromString(level[0])
	if err != nil {
		return exchange.L2Level{}, false
	}
	size, err := decimal.NewFromString(level[1])
	if err != nil {
		return exchange.L2Level{}, false
	}
	return exchange.L2Level{Price: price, Size: size}, true
}

func okxPinger(ctx context.Context, conn *websocket.Conn) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("goroutine panic recovered", "recover", r)
		}
	}()
	ticker := time.NewTicker(okxPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// --- OKX API message types ---

type okxSubArg struct {
	Channel string `json:"channel"`
	InstID  string `json:"instId"`
}

type okxSubRequest struct {
	Op   string      `json:"op"`
	Args []okxSubArg `json:"args"`
}

type okxTradeMsg struct {
	InstID  string `json:"instId"`
	TradeID string `json:"tradeId"`
	Px      string `json:"px"`
	Sz      string `json:"sz"`
	Side    string `json:"side"`
	Ts      string `json:"ts"`
}

// okxBooks5Msg is one element of a books5 push's "data" array — always a
// full top-5 snapshot, not an incremental diff, so no local merge is required.
type okxBooks5Msg struct {
	InstID string     `json:"instId"`
	Asks   [][]string `json:"asks"`
	Bids   [][]string `json:"bids"`
	Ts     string     `json:"ts"`
	SeqID  int64      `json:"seqId"`
}
