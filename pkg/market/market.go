// Package market provides Alpaca market calendar and clock utilities.
// Fetch the calendar once at startup and use local lookups for IsOpen/NextOpen.
//
// Usage:
//
//	client := market.NewClient(os.Getenv("ALPACA_API_KEY"), os.Getenv("ALPACA_API_SECRET"))
//	cal, err := client.FetchCalendar(ctx, time.Now(), time.Now().AddDate(1, 0, 0))
//	open := market.IsOpen(cal, time.Now())
package market

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const baseURL = "https://paper-api.alpaca.markets/v2"

var nyc *time.Location

func init() {
	var err error
	nyc, err = time.LoadLocation("America/New_York")
	if err != nil {
		nyc = time.FixedZone("ET", -5*3600)
	}
}

// Client calls Alpaca REST API for market calendar and clock.
type Client struct {
	apiKey    string
	apiSecret string
	http      *http.Client
}

func NewClient(apiKey, apiSecret string) *Client {
	return &Client{
		apiKey:    apiKey,
		apiSecret: apiSecret,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

// TradingDay represents one trading session.
type TradingDay struct {
	Date  time.Time // calendar date (midnight ET)
	Open  time.Time // market open (ET)
	Close time.Time // market close (ET)
}

// Clock is the current market status from Alpaca.
type Clock struct {
	Timestamp time.Time
	IsOpen    bool
	NextOpen  time.Time
	NextClose time.Time
}

// FetchCalendar returns all trading days between start and end (inclusive).
// Call once at startup and cache the result.
func (c *Client) FetchCalendar(ctx context.Context, start, end time.Time) ([]TradingDay, error) {
	url := fmt.Sprintf("%s/calendar?start=%s&end=%s",
		baseURL,
		start.Format("2006-01-02"),
		end.Format("2006-01-02"),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calendar request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("calendar: unexpected status %d", resp.StatusCode)
	}

	var raw []struct {
		Date  string `json:"date"`
		Open  string `json:"open"`
		Close string `json:"close"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("calendar decode: %w", err)
	}

	days := make([]TradingDay, 0, len(raw))
	for _, r := range raw {
		date, err := time.ParseInLocation("2006-01-02", r.Date, nyc)
		if err != nil {
			continue
		}
		open, err := time.ParseInLocation("2006-01-02T15:04", r.Date+"T"+r.Open, nyc)
		if err != nil {
			continue
		}
		close, err := time.ParseInLocation("2006-01-02T15:04", r.Date+"T"+r.Close, nyc)
		if err != nil {
			continue
		}
		days = append(days, TradingDay{Date: date, Open: open, Close: close})
	}
	return days, nil
}

// FetchClock returns real-time market status from Alpaca.
// Use for edge cases around open/close time; prefer local calendar for general checks.
func (c *Client) FetchClock(ctx context.Context) (*Clock, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/clock", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clock request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clock: unexpected status %d", resp.StatusCode)
	}

	var raw struct {
		Timestamp time.Time `json:"timestamp"`
		IsOpen    bool      `json:"is_open"`
		NextOpen  time.Time `json:"next_open"`
		NextClose time.Time `json:"next_close"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("clock decode: %w", err)
	}

	return &Clock{
		Timestamp: raw.Timestamp,
		IsOpen:    raw.IsOpen,
		NextOpen:  raw.NextOpen,
		NextClose: raw.NextClose,
	}, nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("APCA-API-KEY-ID", c.apiKey)
	req.Header.Set("APCA-API-SECRET-KEY", c.apiSecret)
}

// IsOpen returns true if t falls within a trading session in the calendar.
func IsOpen(cal []TradingDay, t time.Time) bool {
	t = t.In(nyc)
	for _, d := range cal {
		if !t.Before(d.Open) && t.Before(d.Close) {
			return true
		}
	}
	return false
}

// NextOpen returns the next market open at or after t.
// Returns zero time if none found in the calendar.
func NextOpen(cal []TradingDay, t time.Time) time.Time {
	t = t.In(nyc)
	for _, d := range cal {
		if d.Open.After(t) {
			return d.Open
		}
	}
	return time.Time{}
}

// NextClose returns the close time of the current or next session at or after t.
func NextClose(cal []TradingDay, t time.Time) time.Time {
	t = t.In(nyc)
	for _, d := range cal {
		if d.Close.After(t) {
			return d.Close
		}
	}
	return time.Time{}
}

// TradingDaysUntil returns the number of trading days from now until target date.
func TradingDaysUntil(cal []TradingDay, target time.Time) int {
	now := time.Now().In(nyc)
	count := 0
	for _, d := range cal {
		if d.Date.After(now) && !d.Date.After(target) {
			count++
		}
	}
	return count
}

// IsTradingDay returns true if d falls on a scheduled trading session.
func IsTradingDay(cal []TradingDay, d time.Time) bool {
	d = d.In(nyc)
	for _, td := range cal {
		if td.Date.Year() == d.Year() && td.Date.Month() == d.Month() && td.Date.Day() == d.Day() {
			return true
		}
	}
	return false
}

// LastTradingDay returns the most recent trading day at or before t.
// Returns zero time if cal is empty or all days are after t.
func LastTradingDay(cal []TradingDay, t time.Time) time.Time {
	t = t.In(nyc)
	for i := len(cal) - 1; i >= 0; i-- {
		if !cal[i].Date.After(t) {
			return cal[i].Date
		}
	}
	return time.Time{}
}
