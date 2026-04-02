package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"stream-data/internal/model"
)

const (
	wsURL     = "wss://stream.data.alpaca.markets/v2/iex"
	clockURL  = "https://api.alpaca.markets/v2/clock"
	reconnect = 5 * time.Second
)

// Provider streams 1-minute OHLCV bars from Alpaca IEX feed (free tier).
// Subscribes to the "bars" channel — server-side aggregated, emitted at minute close.
// Implements stream.BarProvider.
type Provider struct {
	APIKey    string
	APISecret string
}

func New(apiKey, apiSecret string) *Provider {
	return &Provider{APIKey: apiKey, APISecret: apiSecret}
}

func (p *Provider) Name() string { return "alpaca" }

func (p *Provider) StreamBars(ctx context.Context, symbols []string) (<-chan model.Bar, error) {
	if len(symbols) == 0 {
		return nil, fmt.Errorf("alpaca: no symbols provided")
	}
	out := make(chan model.Bar, 256)
	go p.loop(ctx, symbols, out)
	return out, nil
}

func (p *Provider) loop(ctx context.Context, symbols []string, out chan<- model.Bar) {
	defer close(out)
	for {
		// Wait until market is open; get close time so we know when to disconnect.
		clock, err := p.waitForMarketOpen(ctx)
		if err != nil {
			return // ctx cancelled
		}

		// Derive a context that cancels at market close — clean daily lifecycle.
		sessionCtx, cancelSession := context.WithDeadline(ctx, clock.NextClose)

		slog.Info("alpaca: market open, connecting", "closes_at", clock.NextClose.Format(time.RFC3339))
		if err := p.connect(sessionCtx, symbols, out); err != nil {
			cancelSession()
			if ctx.Err() != nil {
				return
			}
			slog.Warn("alpaca: disconnected, reconnecting", "err", err, "wait", reconnect)
			select {
			case <-time.After(reconnect):
			case <-ctx.Done():
				return
			}
			continue
		}
		cancelSession()

		// connect returned nil = session context expired (market closed) → loop to next day.
		slog.Info("alpaca: market closed, waiting for next session")
	}
}

// waitForMarketOpen polls the Alpaca clock API until market is open.
// Returns the clock (with NextClose) so the caller can set a session deadline.
func (p *Provider) waitForMarketOpen(ctx context.Context) (*alpacaClock, error) {
	for {
		clock, err := p.fetchClock(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			slog.Warn("alpaca: clock fetch failed, retrying", "err", err)
			select {
			case <-time.After(reconnect):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
		if clock.IsOpen {
			return clock, nil
		}
		wait := time.Until(clock.NextOpen)
		if wait <= 0 {
			return clock, nil
		}
		slog.Info("alpaca: market closed, waiting for open",
			"next_open", clock.NextOpen.Format(time.RFC3339),
			"wait", wait.Round(time.Minute))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (p *Provider) fetchClock(ctx context.Context) (*alpacaClock, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clockURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("APCA-API-KEY-ID", p.APIKey)
	req.Header.Set("APCA-API-SECRET-KEY", p.APISecret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var clock alpacaClock
	if err := json.NewDecoder(resp.Body).Decode(&clock); err != nil {
		return nil, err
	}
	return &clock, nil
}

type alpacaClock struct {
	IsOpen    bool      `json:"is_open"`
	NextOpen  time.Time `json:"next_open"`
	NextClose time.Time `json:"next_close"`
}

func (p *Provider) connect(ctx context.Context, symbols []string, out chan<- model.Bar) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Close connection when ctx is cancelled so ReadMessage unblocks.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	// Alpaca lifecycle: connected → auth → authenticated → subscribe → bars
	if err := p.handshake(conn, symbols); err != nil {
		return err
	}
	slog.Info("alpaca: connected", "symbols", len(symbols))

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}

		var msgs []alpacaMsg
		if err := json.Unmarshal(msg, &msgs); err != nil {
			continue
		}
		for _, m := range msgs {
			if m.Type != "b" {
				continue
			}
			out <- model.Bar{
				OpenTime: m.Timestamp.UnixMilli(),
				Symbol:   m.Symbol,
				Open:     m.Open,
				High:     m.High,
				Low:      m.Low,
				Close:    m.Close,
				Volume:   m.Volume,
			}
		}
	}
}

// handshake performs the Alpaca WebSocket auth + subscribe sequence and
// validates each server acknowledgement before proceeding.
func (p *Provider) handshake(conn *websocket.Conn, symbols []string) error {
	// 1. Wait for "connected"
	if err := p.expectMsg(conn, "connected"); err != nil {
		return fmt.Errorf("handshake connected: %w", err)
	}

	// 2. Authenticate
	if err := conn.WriteJSON(map[string]any{
		"action": "auth",
		"key":    p.APIKey,
		"secret": p.APISecret,
	}); err != nil {
		return fmt.Errorf("auth send: %w", err)
	}
	if err := p.expectMsg(conn, "authenticated"); err != nil {
		return fmt.Errorf("handshake auth: %w", err)
	}

	// 3. Subscribe to bars
	if err := conn.WriteJSON(map[string]any{
		"action": "subscribe",
		"bars":   symbols,
	}); err != nil {
		return fmt.Errorf("subscribe send: %w", err)
	}
	// Server replies with a subscription confirmation (T=subscription), not "success"
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("subscribe ack: %w", err)
	}
	var acks []alpacaMsg
	if err := json.Unmarshal(msg, &acks); err != nil || len(acks) == 0 {
		return fmt.Errorf("subscribe ack parse: %s", msg)
	}
	if acks[0].Type == "error" {
		return fmt.Errorf("subscribe error: code=%d msg=%s", acks[0].Code, acks[0].Msg)
	}
	return nil
}

// expectMsg reads the next message and checks that it contains the expected msg field.
func (p *Provider) expectMsg(conn *websocket.Conn, want string) error {
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	var msgs []alpacaMsg
	if err := json.Unmarshal(raw, &msgs); err != nil || len(msgs) == 0 {
		return fmt.Errorf("unexpected payload: %s", raw)
	}
	m := msgs[0]
	if m.Type == "error" {
		return fmt.Errorf("server error: code=%d msg=%s", m.Code, m.Msg)
	}
	if m.Msg != want {
		return fmt.Errorf("expected %q, got %q", want, m.Msg)
	}
	return nil
}

type alpacaMsg struct {
	Type      string    `json:"T"`
	Msg       string    `json:"msg"`
	Code      int       `json:"code"`
	Symbol    string    `json:"S"`
	Open      float64   `json:"o"`
	High      float64   `json:"h"`
	Low       float64   `json:"l"`
	Close     float64   `json:"c"`
	Volume    float64   `json:"v"`
	Timestamp time.Time `json:"t"`
}
