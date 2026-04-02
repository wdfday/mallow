package action

import (
	"fmt"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
)

// Bar is a simplified OHLCV candle.
type Bar struct {
	Timestamp time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	VWAP      float64
}

// Quote holds best bid/ask for a symbol.
type Quote struct {
	Timestamp time.Time
	BidPrice  float64
	BidSize   float64
	AskPrice  float64
	AskSize   float64
}

// Snapshot is a point-in-time view of a symbol: latest trade, quote, and daily bar.
type Snapshot struct {
	Symbol        string
	LatestTrade   float64
	BidPrice      float64
	AskPrice      float64
	DailyOpen     float64
	DailyHigh     float64
	DailyLow      float64
	DailyClose    float64
	DailyVolume   float64
	PrevDayClose  float64
	ChangePercent float64
}

// GetLatestBar returns the most recent completed bar for a symbol.
func (c *Client) GetLatestBar(symbol string, timeframe marketdata.TimeFrame) (*Bar, error) {
	feed := feedFor(symbol)
	bars, err := c.md.GetBars(symbol, marketdata.GetBarsRequest{
		TimeFrame: timeframe,
		Feed:      feed,
	})
	if err != nil {
		return nil, fmt.Errorf("alpaca get latest bar %s: %w", symbol, err)
	}
	if len(bars) == 0 {
		return nil, fmt.Errorf("alpaca get latest bar %s: no data", symbol)
	}
	b := bars[len(bars)-1]
	return &Bar{
		Timestamp: b.Timestamp,
		Open:      b.Open,
		High:      b.High,
		Low:       b.Low,
		Close:     b.Close,
		Volume:    float64(b.Volume),
		VWAP:      b.VWAP,
	}, nil
}

// GetBars returns historical bars for a symbol in the given time range.
func (c *Client) GetBars(symbol string, timeframe marketdata.TimeFrame, start, end time.Time) ([]Bar, error) {
	feed := feedFor(symbol)
	raw, err := c.md.GetBars(symbol, marketdata.GetBarsRequest{
		TimeFrame: timeframe,
		Start:     start,
		End:       end,
		Feed:      feed,
	})
	if err != nil {
		return nil, fmt.Errorf("alpaca get bars %s: %w", symbol, err)
	}
	bars := make([]Bar, len(raw))
	for i, b := range raw {
		bars[i] = Bar{
			Timestamp: b.Timestamp,
			Open:      b.Open,
			High:      b.High,
			Low:       b.Low,
			Close:     b.Close,
			Volume:    float64(b.Volume),
			VWAP:      b.VWAP,
		}
	}
	return bars, nil
}

// GetLatestQuote returns the current best bid/ask for a symbol.
func (c *Client) GetLatestQuote(symbol string) (*Quote, error) {
	feed := feedFor(symbol)
	q, err := c.md.GetLatestQuote(symbol, marketdata.GetLatestQuoteRequest{Feed: feed})
	if err != nil {
		return nil, fmt.Errorf("alpaca get quote %s: %w", symbol, err)
	}
	return &Quote{
		Timestamp: q.Timestamp,
		BidPrice:  q.BidPrice,
		BidSize:   float64(q.BidSize),
		AskPrice:  q.AskPrice,
		AskSize:   float64(q.AskSize),
	}, nil
}

// GetSnapshot returns a point-in-time market snapshot for a symbol.
func (c *Client) GetSnapshot(symbol string) (*Snapshot, error) {
	feed := feedFor(symbol)
	snap, err := c.md.GetSnapshot(symbol, marketdata.GetSnapshotRequest{Feed: feed})
	if err != nil {
		return nil, fmt.Errorf("alpaca get snapshot %s: %w", symbol, err)
	}
	s := &Snapshot{Symbol: symbol}
	if snap.LatestTrade != nil {
		s.LatestTrade = snap.LatestTrade.Price
	}
	if snap.LatestQuote != nil {
		s.BidPrice = snap.LatestQuote.BidPrice
		s.AskPrice = snap.LatestQuote.AskPrice
	}
	if snap.DailyBar != nil {
		s.DailyOpen = snap.DailyBar.Open
		s.DailyHigh = snap.DailyBar.High
		s.DailyLow = snap.DailyBar.Low
		s.DailyClose = snap.DailyBar.Close
		s.DailyVolume = float64(snap.DailyBar.Volume)
	}
	if snap.PrevDailyBar != nil && snap.PrevDailyBar.Close > 0 && snap.DailyBar != nil {
		s.PrevDayClose = snap.PrevDailyBar.Close
		s.ChangePercent = (snap.DailyBar.Close - snap.PrevDailyBar.Close) / snap.PrevDailyBar.Close * 100
	}
	return s, nil
}

// GetSnapshots returns point-in-time snapshots for multiple symbols in one call.
func (c *Client) GetSnapshots(symbols []string) (map[string]*Snapshot, error) {
	feed := ""
	if len(symbols) > 0 {
		feed = feedFor(symbols[0])
	}
	raw, err := c.md.GetSnapshots(symbols, marketdata.GetSnapshotRequest{Feed: feed})
	if err != nil {
		return nil, fmt.Errorf("alpaca get snapshots: %w", err)
	}
	result := make(map[string]*Snapshot, len(raw))
	for sym, snap := range raw {
		s := &Snapshot{Symbol: sym}
		if snap.LatestTrade != nil {
			s.LatestTrade = snap.LatestTrade.Price
		}
		if snap.LatestQuote != nil {
			s.BidPrice = snap.LatestQuote.BidPrice
			s.AskPrice = snap.LatestQuote.AskPrice
		}
		if snap.DailyBar != nil {
			s.DailyOpen = snap.DailyBar.Open
			s.DailyHigh = snap.DailyBar.High
			s.DailyLow = snap.DailyBar.Low
			s.DailyClose = snap.DailyBar.Close
			s.DailyVolume = float64(snap.DailyBar.Volume)
		}
		if snap.PrevDailyBar != nil && snap.PrevDailyBar.Close > 0 && snap.DailyBar != nil {
			s.PrevDayClose = snap.PrevDailyBar.Close
			s.ChangePercent = (snap.DailyBar.Close - snap.PrevDailyBar.Close) / snap.PrevDailyBar.Close * 100
		}
		result[sym] = s
	}
	return result, nil
}
