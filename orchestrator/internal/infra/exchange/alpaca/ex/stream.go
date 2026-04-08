package ex

import (
	"context"
	"log/slog"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	"github.com/shopspring/decimal"
)

// Subscribe connects to the Alpaca market data WebSocket and subscribes to
// live trade prices (and crypto orderbook) for the given symbols.
// Stocks and crypto are handled on separate connections automatically.
// Blocks until ctx is cancelled.
func (c *Client) Subscribe(ctx context.Context, symbols []string) error {
	var stocks, cryptos []string
	for _, s := range symbols {
		if isCrypto(s) {
			cryptos = append(cryptos, s)
		} else {
			stocks = append(stocks, s)
		}
	}

	errCh := make(chan error, 2)

	if len(stocks) > 0 {
		go func() { errCh <- c.runStocks(ctx, stocks) }()
	}
	if len(cryptos) > 0 {
		go func() { errCh <- c.runCrypto(ctx, cryptos) }()
	}

	// Wait for ctx or first fatal error.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func (c *Client) runStocks(ctx context.Context, symbols []string) error {
	slog.Info("alpaca/ex: starting stocks stream", "symbols", symbols)
	client := stream.NewStocksClient(
		marketdata.IEX,
		stream.WithCredentials(c.apiKey, c.apiSecret),
	)
	if err := client.Connect(ctx); err != nil {
		return err
	}
	if err := client.SubscribeToTrades(func(t stream.Trade) {
		c.dispatchPrice(t.Symbol, decimal.NewFromFloat(t.Price))
	}, symbols...); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (c *Client) runCrypto(ctx context.Context, symbols []string) error {
	slog.Info("alpaca/ex: starting crypto stream", "symbols", symbols)
	client := stream.NewCryptoClient(
		marketdata.US,
		stream.WithCredentials(c.apiKey, c.apiSecret),
	)
	if err := client.Connect(ctx); err != nil {
		return err
	}
	if err := client.SubscribeToTrades(func(t stream.CryptoTrade) {
		c.dispatchPrice(t.Symbol, decimal.NewFromFloat(t.Price))
	}, symbols...); err != nil {
		return err
	}
	// Orderbook — crypto only.
	if err := client.SubscribeToOrderbooks(func(ob stream.CryptoOrderbook) {
		u := OrderbookUpdate{Symbol: ob.Symbol}
		for _, e := range ob.Bids {
			u.Bids = append(u.Bids, OrderbookEntry{Price: e.Price, Size: e.Size})
		}
		for _, e := range ob.Asks {
			u.Asks = append(u.Asks, OrderbookEntry{Price: e.Price, Size: e.Size})
		}
		c.dispatchOrderbook(u)
	}, symbols...); err != nil {
		slog.Warn("alpaca/ex: orderbook subscribe failed", "err", err)
	}
	<-ctx.Done()
	return ctx.Err()
}
