package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"stream-data/internal/model"
)

const (
	wsBase    = "wss://stream.binance.com:9443/stream"
	reconnect = 5 * time.Second
)

// Provider streams 1-minute OHLCV bars from Binance @kline_1m combined stream.
// No API key required (public WebSocket).
// Implements stream.BarProvider.
type Provider struct{}

func New() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "binance" }

// StreamBars subscribes to @kline_1m for each symbol and emits a Bar only when
// the kline is closed (k.x == true), i.e. exactly once per completed minute.
func (p *Provider) StreamBars(ctx context.Context, symbols []string) (<-chan model.Bar, error) {
	if len(symbols) == 0 {
		return nil, fmt.Errorf("binance: no symbols provided")
	}
	out := make(chan model.Bar, 256)
	go p.loop(ctx, symbols, out)
	return out, nil
}

func (p *Provider) loop(ctx context.Context, symbols []string, out chan<- model.Bar) {
	defer close(out)

	streams := make([]string, len(symbols))
	for i, s := range symbols {
		streams[i] = strings.ToLower(s) + "@kline_1m"
	}
	url := wsBase + "?streams=" + strings.Join(streams, "/")

	for {
		if err := p.connect(ctx, url, out); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("binance: disconnected, reconnecting", "err", err, "wait", reconnect)
			select {
			case <-time.After(reconnect):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (p *Provider) connect(ctx context.Context, url string, out chan<- model.Bar) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	slog.Info("binance: connected", "streams", url)

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

		var env struct {
			Data klineMsg `json:"data"`
		}
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}

		k := env.Data.Kline
		if !k.Closed {
			continue // skip in-progress candles
		}

		bar := model.Bar{
			OpenTime: k.OpenTime,
			Symbol:   strings.ToUpper(env.Data.Symbol),
			Open:     float64(k.Open),
			High:     float64(k.High),
			Low:      float64(k.Low),
			Close:    float64(k.Close),
			Volume:   float64(k.Volume),
		}
		slog.Debug("binance: bar closed", "symbol", bar.Symbol, "close", bar.Close)
		out <- bar
	}
}

type klineMsg struct {
	Symbol string `json:"s"`
	Kline  struct {
		// NOTE: Go's encoding/json does case-insensitive key matching, so "T" (close time)
		// would silently overwrite "t" (open time) if CloseTime is not declared here.
		// Both fields must be present to force exact matching.
		OpenTime  int64     `json:"t"`
		CloseTime int64     `json:"T"`
		Open      flexFloat `json:"o"`
		High      flexFloat `json:"h"`
		Low       flexFloat `json:"l"`
		Close     flexFloat `json:"c"`
		Volume    flexFloat `json:"v"`
		Closed    bool      `json:"x"`
	} `json:"k"`
}

// flexFloat unmarshals a JSON value that may be either a number or a numeric string.
// Binance WebSocket API has been observed to return OHLCV fields as both types.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		*f = flexFloat(n)
		return nil
	}
	var n float64
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*f = flexFloat(n)
	return nil
}
