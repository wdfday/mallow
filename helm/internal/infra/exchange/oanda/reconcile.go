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

	"mallow/helm/internal/infra/exchange"
)

// ListOpenOrders returns all PENDING orders for the account.
// If symbol is non-empty, filters to that instrument only.
func (c *Client) ListOpenOrders(ctx context.Context, creds exchange.Credentials, symbol string) ([]exchange.OrderResult, error) {
	path := fmt.Sprintf("/v3/accounts/%s/orders?state=PENDING", creds.AccountID)
	if symbol != "" {
		path += "&instrument=" + symbol
	}

	var resp struct {
		Orders []struct {
			ID         string `json:"id"`
			Instrument string `json:"instrument"`
			Units      string `json:"units"`
			State      string `json:"state"`
			Type       string `json:"type"`
			Price      string `json:"price"`
		} `json:"orders"`
	}
	if err := c.doRequest(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("oanda list open orders: %w", err)
	}

	results := make([]exchange.OrderResult, 0, len(resp.Orders))
	for _, o := range resp.Orders {
		qty := decimal.NewFromFloat(parseFloat(o.Units))
		side := exchange.Buy
		if qty.IsNegative() {
			side = exchange.Sell
			qty = qty.Abs()
		}
		results = append(results, exchange.OrderResult{
			ID:     o.ID,
			Symbol: o.Instrument,
			Side:   side,
			Status: mapStatus(o.State),
			Qty:    qty,
		})
	}
	return results, nil
}

// ListPositions returns all open positions for the account as exchange.PositionResult.
// Each instrument with non-zero long or short units produces a separate entry.
func (c *Client) ListPositions(ctx context.Context, creds exchange.Credentials) ([]exchange.PositionResult, error) {
	path := fmt.Sprintf("/v3/accounts/%s/openPositions", creds.AccountID)
	var resp struct {
		Positions []struct {
			Instrument string `json:"instrument"`
			Long       struct {
				Units        string `json:"units"`
				AveragePrice string `json:"averagePrice"`
				UnrealizedPL string `json:"unrealizedPL"`
			} `json:"long"`
			Short struct {
				Units        string `json:"units"`
				AveragePrice string `json:"averagePrice"`
				UnrealizedPL string `json:"unrealizedPL"`
			} `json:"short"`
		} `json:"positions"`
	}
	if err := c.doRequest(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("oanda list positions: %w", err)
	}

	var results []exchange.PositionResult
	for _, p := range resp.Positions {
		longUnits := decimal.NewFromFloat(parseFloat(p.Long.Units))
		if longUnits.IsPositive() {
			results = append(results, exchange.PositionResult{
				Symbol:    p.Instrument,
				Side:      exchange.Buy,
				Qty:       longUnits,
				AvgPrice:  decimal.NewFromFloat(parseFloat(p.Long.AveragePrice)),
				UnrealPnL: decimal.NewFromFloat(parseFloat(p.Long.UnrealizedPL)),
			})
		}

		// Short units are negative strings from OANDA (e.g. "-1000")
		shortUnits := decimal.NewFromFloat(parseFloat(p.Short.Units)).Abs()
		if shortUnits.IsPositive() {
			results = append(results, exchange.PositionResult{
				Symbol:    p.Instrument,
				Side:      exchange.Sell,
				Qty:       shortUnits,
				AvgPrice:  decimal.NewFromFloat(parseFloat(p.Short.AveragePrice)),
				UnrealPnL: decimal.NewFromFloat(parseFloat(p.Short.UnrealizedPL)),
			})
		}
	}
	return results, nil
}

// SubscribeFills opens OANDA's streaming transactions endpoint and emits a FillEvent
// for each ORDER_FILL transaction. The channel is closed when ctx is cancelled.
// OANDA delivers fills synchronously via the REST response (FOK market orders), but
// this stream also catches any fills that arrive asynchronously (e.g. limit orders).
func (c *Client) SubscribeFills(ctx context.Context, creds exchange.Credentials) (<-chan exchange.FillEvent, error) {
	ch := make(chan exchange.FillEvent, 16)
	go func() {
		defer close(ch)
		for {
			if ctx.Err() != nil {
				return
			}
			err := c.streamFillsOnce(ctx, creds, ch)
			if ctx.Err() != nil {
				return
			}
			slog.Warn("oanda: transaction stream disconnected, reconnecting in 5s", "err", err)
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}()
	slog.Info("oanda: fill streaming started")
	return ch, nil
}

func (c *Client) streamFillsOnce(ctx context.Context, creds exchange.Credentials, ch chan<- exchange.FillEvent) error {
	path := fmt.Sprintf("/v3/accounts/%s/transactions/stream", creds.AccountID)
	url := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+creds.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var ev struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			OrderID    string `json:"orderID"`
			TradeID    string `json:"tradeID"`
			Instrument string `json:"instrument"`
			Units      string `json:"units"` // negative for sell
			Price      string `json:"price"` // fill price
			Time       string `json:"time"`  // RFC3339Nano
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Type != "ORDER_FILL" {
			continue
		}

		units := decimal.NewFromFloat(parseFloat(ev.Units))
		side := exchange.Buy
		if units.IsNegative() {
			side = exchange.Sell
			units = units.Abs()
		}

		ts, _ := time.Parse(time.RFC3339Nano, ev.Time)
		if ts.IsZero() {
			ts = time.Now().UTC()
		}

		price, _ := decimal.NewFromString(ev.Price)

		orderID := ev.OrderID
		if orderID == "" {
			orderID = ev.ID
		}

		select {
		case ch <- exchange.FillEvent{
			OrderID:   orderID,
			Symbol:    ev.Instrument,
			Side:      side,
			FilledQty: units,
			FillPrice: price,
			Timestamp: ts,
		}:
		case <-ctx.Done():
			return nil
		}
	}
	return scanner.Err()
}
