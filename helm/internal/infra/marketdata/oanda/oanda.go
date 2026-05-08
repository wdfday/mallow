package oanda

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	reconnectWait = 5 * time.Second
)

// Listener streams real-time prices from OANDA v20 Streaming API (SSE).
// Requires a personal access token.
type Listener struct {
	Token     string
	AccountID string
	StreamURL string // default: https://stream-fxpractice.oanda.com
}

// New creates a new OANDA market data listener.
func New(token, accountID, streamURL string) *Listener {
	if streamURL == "" {
		streamURL = "https://stream-fxpractice.oanda.com"
	}
	return &Listener{Token: token, AccountID: accountID, StreamURL: streamURL}
}

func (l *Listener) Name() string { return "oanda" }

// Subscribe connects to OANDA streaming prices and calls onTick for each price update.
// Symbols should be OANDA instrument IDs, e.g. "EUR_USD", "GBP_USD".
// Blocks until ctx is cancelled. Reconnects automatically on failure.
func (l *Listener) Subscribe(ctx context.Context, symbols []string, onTick func(string, decimal.Decimal)) error {
	if len(symbols) == 0 {
		return fmt.Errorf("oanda listener: no symbols provided")
	}
	for {
		if err := l.connect(ctx, symbols, onTick); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("oanda listener: disconnected, reconnecting", "err", err, "wait", reconnectWait)
			select {
			case <-time.After(reconnectWait):
			case <-ctx.Done():
				return nil
			}
		}
	}
}

func (l *Listener) connect(ctx context.Context, symbols []string, onTick func(string, decimal.Decimal)) error {
	instruments := strings.Join(symbols, ",")
	url := fmt.Sprintf("%s/v3/accounts/%s/pricing/stream?instruments=%s",
		l.StreamURL, l.AccountID, instruments)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+l.Token)

	client := &http.Client{Timeout: 0} // no timeout for streaming
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%d", resp.StatusCode)
	}

	slog.Info("oanda listener: connected", "instruments", instruments)

	// OANDA streaming sends one JSON object per line (newline-delimited).
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		line := scanner.Text()
		if line == "" {
			continue
		}
		handleMessage([]byte(line), onTick)
	}
	return scanner.Err()
}

// handleMessage parses a single OANDA streaming price line.
func handleMessage(msg []byte, onTick func(string, decimal.Decimal)) {
	var price priceMsg
	if err := json.Unmarshal(msg, &price); err != nil {
		return
	}

	// Skip heartbeats.
	if price.Type != "PRICE" || price.Instrument == "" {
		return
	}

	// Use mid-price: (bid + ask) / 2
	mid := midPrice(price.Bids, price.Asks)
	if mid.IsPositive() {
		onTick(price.Instrument, mid)
	}
}

func midPrice(bids, asks []priceLevel) decimal.Decimal {
	if len(bids) == 0 || len(asks) == 0 {
		return decimal.Zero
	}
	bid, err1 := decimal.NewFromString(bids[0].Price)
	ask, err2 := decimal.NewFromString(asks[0].Price)
	if err1 != nil || err2 != nil || !bid.IsPositive() || !ask.IsPositive() {
		return decimal.Zero
	}
	return bid.Add(ask).Div(decimal.NewFromInt(2))
}

// --- OANDA streaming types ---

type priceMsg struct {
	Type       string       `json:"type"` // "PRICE" or "HEARTBEAT"
	Instrument string       `json:"instrument"`
	Bids       []priceLevel `json:"bids"`
	Asks       []priceLevel `json:"asks"`
	Time       string       `json:"time"`
}

type priceLevel struct {
	Price     string `json:"price"`
	Liquidity int    `json:"liquidity"`
}
