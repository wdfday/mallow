package twelvedata

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"stream-data/internal/model"
)

const (
	wsURL     = "wss://ws.twelvedata.com/v1/quotes/price"
	reconnect = 5 * time.Second
)

// Provider streams real-time forex (and stock) price ticks from Twelve Data.
// Free tier: up to 8 symbols, 1 connection.
type Provider struct {
	APIKey string
}

func New(apiKey string) *Provider { return &Provider{APIKey: apiKey} }

func (p *Provider) Name() string { return "twelvedata" }

func (p *Provider) Stream(ctx context.Context, symbols []string) (<-chan model.Tick, error) {
	if len(symbols) == 0 {
		return nil, fmt.Errorf("twelvedata: no symbols provided")
	}
	out := make(chan model.Tick, 256)
	go p.loop(ctx, symbols, out)
	return out, nil
}

func (p *Provider) loop(ctx context.Context, symbols []string, out chan<- model.Tick) {
	defer close(out)
	for {
		if err := p.connect(ctx, symbols, out); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("twelvedata: disconnected, reconnecting", "err", err, "wait", reconnect)
			select {
			case <-time.After(reconnect):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (p *Provider) connect(ctx context.Context, symbols []string, out chan<- model.Tick) error {
	url := wsURL + "?apikey=" + p.APIKey
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Subscribe
	sub := map[string]any{
		"action": "subscribe",
		"params": map[string]any{"symbols": strings.Join(symbols, ",")},
	}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	slog.Info("twelvedata: connected", "symbols", symbols)

	for {
		if ctx.Err() != nil {
			return nil
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var e priceEvent
		if err := json.Unmarshal(msg, &e); err != nil {
			continue
		}
		if e.Event != "price" {
			continue
		}
		out <- model.Tick{
			Timestamp: int64(e.Timestamp) * 1000, // s → ms
			Symbol:    e.Symbol,
			Class:     "forex",
			Source:    "twelvedata",
			Price:     e.Price,
			Bid:       e.Bid,
			Ask:       e.Ask,
			Volume:    float64(e.DayVolume),
		}
	}
}

type priceEvent struct {
	Event     string  `json:"event"`
	Symbol    string  `json:"symbol"`
	Timestamp int64   `json:"timestamp"`
	Price     float64 `json:"price"`
	Bid       float64 `json:"bid"`
	Ask       float64 `json:"ask"`
	DayVolume int64   `json:"day_volume"`
}
