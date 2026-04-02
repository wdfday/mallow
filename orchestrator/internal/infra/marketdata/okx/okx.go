package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsURL         = "wss://ws.okx.com:8443/ws/v5/public"
	reconnectWait = 5 * time.Second
	pingInterval  = 25 * time.Second
)

// Listener streams real-time trade prices from OKX public WebSocket.
// No API key required.
type Listener struct{}

// New creates a new OKX market data listener.
func New() *Listener { return &Listener{} }

func (l *Listener) Name() string { return "okx" }

// Subscribe connects to OKX public WebSocket and calls onTick for each trade.
// Symbols should be OKX instrument IDs, e.g. "BTC-USDT".
// Blocks until ctx is cancelled. Reconnects automatically on failure.
func (l *Listener) Subscribe(ctx context.Context, symbols []string, onTick func(string, float64)) error {
	if len(symbols) == 0 {
		return fmt.Errorf("okx listener: no symbols provided")
	}
	for {
		if err := l.connect(ctx, symbols, onTick); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("okx listener: disconnected, reconnecting", "err", err, "wait", reconnectWait)
			select {
			case <-time.After(reconnectWait):
			case <-ctx.Done():
				return nil
			}
		}
	}
}

func (l *Listener) connect(ctx context.Context, symbols []string, onTick func(string, float64)) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Subscribe to trades channel for each symbol.
	args := make([]subArg, len(symbols))
	for i, s := range symbols {
		args[i] = subArg{Channel: "trades", InstID: s}
	}
	sub := subRequest{Op: "subscribe", Args: args}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	slog.Info("okx listener: connected", "symbols", symbols)

	// Ping goroutine to keep connection alive.
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go pinger(pingCtx, conn)

	for {
		if ctx.Err() != nil {
			return nil
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		handleMessage(msg, onTick)
	}
}

// handleMessage parses a single OKX WebSocket message and calls onTick for trades.
func handleMessage(msg []byte, onTick func(string, float64)) {
	raw := strings.TrimSpace(string(msg))
	if raw == "pong" {
		return
	}

	var push tradePush
	if err := json.Unmarshal(msg, &push); err != nil {
		slog.Debug("okx listener: parse error", "err", err, "msg", raw)
		return
	}

	if push.Arg.Channel != "trades" || len(push.Data) == 0 {
		return
	}

	for _, t := range push.Data {
		px, err := strconv.ParseFloat(t.Px, 64)
		if err != nil || px <= 0 {
			continue
		}
		onTick(t.InstID, px)
	}
}

func pinger(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(pingInterval)
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

type subArg struct {
	Channel string `json:"channel"`
	InstID  string `json:"instId"`
}

type subRequest struct {
	Op   string   `json:"op"`
	Args []subArg `json:"args"`
}

type tradePush struct {
	Arg  subArg     `json:"arg"`
	Data []tradeMsg `json:"data"`
}

type tradeMsg struct {
	InstID  string `json:"instId"`
	TradeID string `json:"tradeId"`
	Px      string `json:"px"`
	Sz      string `json:"sz"`
	Side    string `json:"side"`
	Ts      string `json:"ts"`
}
