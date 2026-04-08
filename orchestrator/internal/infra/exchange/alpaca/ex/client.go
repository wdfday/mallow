// Package ex provides the shared market-data streaming client for Alpaca.
// One Client is created per broker type (not per account) — all orchestrators
// using Alpaca register their UpdatePrice callback here and receive live prices
// for the symbols their bots trade.
package ex

import (
	"strings"
	"sync"

	"github.com/shopspring/decimal"
)

// PriceHandler is called with each live trade price received from the exchange.
type PriceHandler = func(symbol string, price decimal.Decimal)

// OrderbookEntry is a single price level in the orderbook.
type OrderbookEntry struct {
	Price float64
	Size  float64
}

// OrderbookUpdate is a snapshot of the best bid/ask received from the exchange.
type OrderbookUpdate struct {
	Symbol string
	Bids   []OrderbookEntry
	Asks   []OrderbookEntry
}

// OrderbookHandler is called with each orderbook update.
type OrderbookHandler func(update OrderbookUpdate)

// Client is a shared, broker-level market data WebSocket client.
// It maintains one stocks connection and one crypto connection.
// Handlers are fan-out: all registered listeners receive every event.
type Client struct {
	apiKey    string
	apiSecret string

	mu                sync.RWMutex
	priceHandlers     []PriceHandler
	orderbookHandlers []OrderbookHandler
}

// New creates a shared Alpaca market data streaming client.
// Credentials are used for SIP feed; IEX (free) works without them.
func New(apiKey, apiSecret string) *Client {
	return &Client{apiKey: apiKey, apiSecret: apiSecret}
}

// AddPriceHandler registers a callback that fires on every live trade price.
func (c *Client) AddPriceHandler(h PriceHandler) {
	c.mu.Lock()
	c.priceHandlers = append(c.priceHandlers, h)
	c.mu.Unlock()
}

// AddOrderbookHandler registers a callback that fires on crypto orderbook updates.
func (c *Client) AddOrderbookHandler(h OrderbookHandler) {
	c.mu.Lock()
	c.orderbookHandlers = append(c.orderbookHandlers, h)
	c.mu.Unlock()
}

func (c *Client) dispatchPrice(symbol string, price decimal.Decimal) {
	c.mu.RLock()
	hs := c.priceHandlers
	c.mu.RUnlock()
	for _, h := range hs {
		h(symbol, price)
	}
}

func (c *Client) dispatchOrderbook(u OrderbookUpdate) {
	c.mu.RLock()
	hs := c.orderbookHandlers
	c.mu.RUnlock()
	for _, h := range hs {
		h(u)
	}
}

// isCrypto returns true for crypto symbols like "BTC/USD".
func isCrypto(symbol string) bool {
	return strings.Contains(symbol, "/")
}
