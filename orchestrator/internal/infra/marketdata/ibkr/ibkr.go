package ibkr

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	pollInterval = 3 * time.Second
)

// Listener polls real-time prices from IBKR Client Portal API.
// Requires IB Gateway running locally with an authenticated session.
type Listener struct {
	BaseURL string // default: https://localhost:5000/v1/api
	client  *http.Client
}

// New creates a new IBKR market data listener.
func New(baseURL string) *Listener {
	if baseURL == "" {
		baseURL = "https://localhost:5000/v1/api"
	}
	return &Listener{
		BaseURL: baseURL,
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // IB Gateway self-signed cert
			},
		},
	}
}

func (l *Listener) Name() string { return "ibkr" }

// Subscribe polls IBKR market data snapshots and calls onTick for each update.
// Symbols should be IBKR conids as strings (e.g. "265598" for AAPL).
// Blocks until ctx is cancelled.
func (l *Listener) Subscribe(ctx context.Context, symbols []string, onTick func(string, decimal.Decimal)) error {
	if len(symbols) == 0 {
		return fmt.Errorf("ibkr listener: no symbols (conids) provided")
	}

	conids := strings.Join(symbols, ",")
	slog.Info("ibkr listener: starting price polling", "conids", conids, "interval", pollInterval)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.poll(ctx, conids, onTick)
		case <-ctx.Done():
			return nil
		}
	}
}

func (l *Listener) poll(ctx context.Context, conids string, onTick func(string, decimal.Decimal)) {
	// Field 31 = last price, field 84 = bid, field 86 = ask
	url := fmt.Sprintf("%s/iserver/marketdata/snapshot?conids=%s&fields=31", l.BaseURL, conids)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Debug("ibkr listener: request error", "err", err)
		return
	}

	resp, err := l.client.Do(req)
	if err != nil {
		slog.Debug("ibkr listener: poll error", "err", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	if resp.StatusCode != http.StatusOK {
		slog.Debug("ibkr listener: bad status", "status", resp.StatusCode, "body", string(body))
		return
	}

	var snapshots []snapshotResponse
	if err := json.Unmarshal(body, &snapshots); err != nil {
		slog.Debug("ibkr listener: parse error", "err", err)
		return
	}

	for _, snap := range snapshots {
		if snap.LastPrice > 0 && snap.ConID != "" {
			onTick(snap.ConID, decimal.NewFromFloat(snap.LastPrice))
		}
	}
}

// snapshotResponse represents a market data snapshot from IBKR.
type snapshotResponse struct {
	ConID     string  `json:"conid"`
	LastPrice float64 `json:"31"` // field 31 = last price
}
