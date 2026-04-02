package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"stream-data/internal/model"
)

const (
	wsURL         = "wss://ws.okx.com:8443/ws/v5/business"
	reconnectWait = 5 * time.Second
	pingInterval  = 25 * time.Second
)

// Provider streams 1-minute OHLCV bars from OKX candle1m channel.
// No API key required (public channel).
// Implements stream.BarProvider.
type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "okx" }

// StreamBars subscribes to candle1m for each symbol and emits a Bar only when
// the candle is confirmed closed (data[8] == "1").
func (p *Provider) StreamBars(ctx context.Context, symbols []string) (<-chan model.Bar, error) {
	if len(symbols) == 0 {
		return nil, fmt.Errorf("okx: no symbols provided")
	}
	out := make(chan model.Bar, 256)
	go p.loop(ctx, symbols, out)
	return out, nil
}

func (p *Provider) loop(ctx context.Context, symbols []string, out chan<- model.Bar) {
	defer close(out)
	for {
		if err := p.connect(ctx, symbols, out); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("okx: disconnected, reconnecting", "err", err, "wait", reconnectWait)
			select {
			case <-time.After(reconnectWait):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (p *Provider) connect(ctx context.Context, symbols []string, out chan<- model.Bar) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	args := make([]subArg, len(symbols))
	for i, s := range symbols {
		args[i] = subArg{Channel: "candle1m", InstID: s}
	}
	if err := conn.WriteJSON(subRequest{Op: "subscribe", Args: args}); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	slog.Info("okx: connected", "symbols", symbols)

	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go p.pinger(pingCtx, conn)

	// Close connection when ctx is cancelled so ReadMessage unblocks.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		p.handleCandle(msg, out)
	}
}

func (p *Provider) handleCandle(msg []byte, out chan<- model.Bar) {
	if string(msg) == "pong" {
		return
	}

	var push candlePush
	if err := json.Unmarshal(msg, &push); err != nil {
		return
	}
	if push.Arg.Channel != "candle1m" || len(push.Data) == 0 {
		return
	}

	for _, d := range push.Data {
		// d: [ts, open, high, low, close, vol, volCcy, volCcyQuote, confirm]
		if len(d) < 9 || d[8] != "1" {
			continue // skip in-progress candles
		}
		ts, _ := strconv.ParseInt(d[0], 10, 64)
		bar := model.Bar{
			OpenTime: ts,
			Symbol:   push.Arg.InstID,
			Open:     parseFloat(d[1]),
			High:     parseFloat(d[2]),
			Low:      parseFloat(d[3]),
			Close:    parseFloat(d[4]),
			Volume:   parseFloat(d[5]),
		}
		slog.Debug("okx: bar closed", "symbol", bar.Symbol, "close", bar.Close)
		out <- bar
	}
}

func (p *Provider) pinger(ctx context.Context, conn *websocket.Conn) {
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

type subArg struct {
	Channel string `json:"channel"`
	InstID  string `json:"instId"`
}

type subRequest struct {
	Op   string   `json:"op"`
	Args []subArg `json:"args"`
}

type candlePush struct {
	Arg  subArg     `json:"arg"`
	Data [][]string `json:"data"`
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
