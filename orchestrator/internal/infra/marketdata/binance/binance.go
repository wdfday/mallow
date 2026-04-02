package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsBase        = "wss://stream.binance.com:9443/stream"
	reconnectWait = 5 * time.Second
)

// Listener streams real-time trade prices from Binance public WebSocket.
// No API key required.
type Listener struct{}

// New creates a new Binance market data listener.
func New() *Listener { return &Listener{} }

func (l *Listener) Name() string { return "binance" }

// Subscribe connects to Binance combined stream and calls onTick for each trade.
// Symbols should be Binance trading pairs, e.g. "BTCUSDT", "ETHUSDT".
// Blocks until ctx is cancelled.
func (l *Listener) Subscribe(ctx context.Context, symbols []string, onTick func(string, float64)) error {
	if len(symbols) == 0 {
		return fmt.Errorf("binance listener: no symbols provided")
	}
	for {
		if err := l.connect(ctx, symbols, onTick); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("binance listener: disconnected, reconnecting", "err", err, "wait", reconnectWait)
			select {
			case <-time.After(reconnectWait):
			case <-ctx.Done():
				return nil
			}
		}
	}
}

func (l *Listener) connect(ctx context.Context, symbols []string, onTick func(string, float64)) error {
	// Build combined stream URL: btcusdt@trade/ethusdt@trade/...
	streams := make([]string, len(symbols))
	for i, s := range symbols {
		streams[i] = strings.ToLower(s) + "@trade"
	}
	url := wsBase + "?streams=" + strings.Join(streams, "/")

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
		handleMessage(msg, onTick)
	}
}

// handleMessage parses a Binance combined stream message and calls onTick.
func handleMessage(msg []byte, onTick func(string, float64)) {
	var env struct {
		Stream string   `json:"stream"`
		Data   tradeMsg `json:"data"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		return
	}
	if env.Data.EventType != "trade" || env.Data.Symbol == "" {
		return
	}

	px := parseFloat(env.Data.Price)
	if px > 0 {
		onTick(strings.ToUpper(env.Data.Symbol), px)
	}
}

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

type tradeMsg struct {
	EventType string `json:"e"`
	Symbol    string `json:"s"`
	Price     string `json:"p"`
	Qty       string `json:"q"`
	TradeTime int64  `json:"T"`
}
