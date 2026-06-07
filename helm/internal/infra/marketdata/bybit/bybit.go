package bybit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

const (
	wsURL         = "wss://stream.bybit.com/v5/public/spot"
	reconnectWait = 5 * time.Second
	pingInterval  = 20 * time.Second
)

// Listener streams real-time trade prices from Bybit public WebSocket.
// No API key required.
type Listener struct{}

// New creates a new Bybit market data listener.
func New() *Listener { return &Listener{} }

func (l *Listener) Name() string { return "bybit" }

// Subscribe connects to Bybit public WebSocket and calls onTick for each trade.
// Symbols should be Bybit trading pairs, e.g. "BTCUSDT".
// Blocks until ctx is cancelled.
func (l *Listener) Subscribe(ctx context.Context, symbols []string, onTick func(string, decimal.Decimal)) error {
	if len(symbols) == 0 {
		return fmt.Errorf("bybit listener: no symbols provided")
	}
	for {
		if err := l.connect(ctx, symbols, onTick); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("bybit listener: disconnected, reconnecting", "err", err, "wait", reconnectWait)
			select {
			case <-time.After(reconnectWait):
			case <-ctx.Done():
				return nil
			}
		}
	}
}

func (l *Listener) connect(ctx context.Context, symbols []string, onTick func(string, decimal.Decimal)) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Subscribe to publicTrade for each symbol.
	args := make([]string, len(symbols))
	for i, s := range symbols {
		args[i] = "publicTrade." + s
	}
	sub := subRequest{Op: "subscribe", Args: args}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	slog.Info("bybit listener: connected", "symbols", symbols)

	// Ping goroutine (Bybit disconnects after ~30s idle).
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

// handleMessage parses a Bybit WebSocket message and calls onTick for trades.
func handleMessage(msg []byte, onTick func(string, decimal.Decimal)) {
	var push tradePush
	if err := json.Unmarshal(msg, &push); err != nil {
		return
	}
	if push.Topic == "" || len(push.Data) == 0 {
		return
	}

	for _, t := range push.Data {
		px, err := decimal.NewFromString(t.Price)
		if err != nil || !px.IsPositive() {
			continue
		}
		onTick(t.Symbol, px)
	}
}

func pinger(ctx context.Context, conn *websocket.Conn) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("goroutine panic recovered", "recover", r)
		}
	}()
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			ping := map[string]string{"op": "ping"}
			if err := conn.WriteJSON(ping); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// --- Bybit WS types ---

type subRequest struct {
	Op   string   `json:"op"`
	Args []string `json:"args"`
}

type tradePush struct {
	Topic string     `json:"topic"`
	Data  []tradeMsg `json:"data"`
}

type tradeMsg struct {
	Symbol string `json:"S"`
	Price  string `json:"p"`
	Size   string `json:"v"`
	Ts     int64  `json:"T"`
}
