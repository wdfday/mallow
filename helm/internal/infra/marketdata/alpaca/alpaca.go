package alpaca

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	"github.com/shopspring/decimal"
)

// Listener streams real-time trade prices from Alpaca using the official SDK.
// Supports both stocks (IEX) and crypto feeds.
type Listener struct {
	APIKey    string
	APISecret string
	Crypto    bool // if true, use crypto feed instead of stocks
}

// New creates a new Alpaca market data listener.
func New(apiKey, apiSecret string, crypto bool) *Listener {
	return &Listener{APIKey: apiKey, APISecret: apiSecret, Crypto: crypto}
}

func (l *Listener) Name() string { return "alpaca" }

// Subscribe connects to Alpaca streaming and calls onTick for each trade.
// Blocks until ctx is cancelled.
func (l *Listener) Subscribe(ctx context.Context, symbols []string, onTick func(string, decimal.Decimal)) error {
	if len(symbols) == 0 {
		return fmt.Errorf("alpaca listener: no symbols provided")
	}

	opts := []stream.Option{
		stream.WithCredentials(l.APIKey, l.APISecret),
	}

	if l.Crypto {
		return l.subscribeCrypto(ctx, symbols, onTick, opts)
	}
	return l.subscribeStocks(ctx, symbols, onTick, opts)
}

func (l *Listener) subscribeStocks(ctx context.Context, symbols []string, onTick func(string, decimal.Decimal), opts []stream.Option) error {
	stockOpts := make([]stream.StockOption, len(opts))
	for i, o := range opts {
		stockOpts[i] = o.(stream.StockOption)
	}
	stockOpts = append(stockOpts, stream.WithTrades(func(t stream.Trade) {
		if t.Price > 0 {
			onTick(t.Symbol, decimal.NewFromFloat(t.Price))
		}
	}, symbols...))

	client := stream.NewStocksClient(marketdata.IEX, stockOpts...)
	slog.Info("alpaca listener: connecting to stocks stream", "symbols", symbols)

	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("alpaca stocks connect: %w", err)
	}

	// Block until terminated.
	if err := <-client.Terminated(); err != nil {
		return err
	}
	return nil
}

func (l *Listener) subscribeCrypto(ctx context.Context, symbols []string, onTick func(string, decimal.Decimal), opts []stream.Option) error {
	cryptoOpts := make([]stream.CryptoOption, len(opts))
	for i, o := range opts {
		cryptoOpts[i] = o.(stream.CryptoOption)
	}
	cryptoOpts = append(cryptoOpts, stream.WithCryptoTrades(func(t stream.CryptoTrade) {
		if t.Price > 0 {
			onTick(t.Symbol, decimal.NewFromFloat(t.Price))
		}
	}, symbols...))

	client := stream.NewCryptoClient(marketdata.US, cryptoOpts...)
	slog.Info("alpaca listener: connecting to crypto stream", "symbols", symbols)

	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("alpaca crypto connect: %w", err)
	}

	// Block until terminated.
	if err := <-client.Terminated(); err != nil {
		return err
	}
	return nil
}
