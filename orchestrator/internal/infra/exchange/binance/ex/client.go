// Package ex provides the shared market-data streaming client for Binance.
// One Client is created per broker type — all orchestrators using Binance
// register their UpdatePrice callback here and receive live prices.
package ex

import (
	"sync"
)

// PriceHandler is called with each live trade price.
type PriceHandler = func(symbol string, price float64)

// Client is a shared, broker-level market data WebSocket client for Binance.
// Uses the public WsCombinedMarketStatServe (or individual symbol streams)
// so no API key is needed for spot price streaming.
type Client struct {
	testnet bool

	mu            sync.RWMutex
	priceHandlers []PriceHandler
}

// New creates a shared Binance market data streaming client.
// Set testnet=true to connect to testnet WebSocket endpoints.
func New(testnet bool) *Client {
	return &Client{testnet: testnet}
}

// AddPriceHandler registers a callback fired on every live trade price.
func (c *Client) AddPriceHandler(h PriceHandler) {
	c.mu.Lock()
	c.priceHandlers = append(c.priceHandlers, h)
	c.mu.Unlock()
}

func (c *Client) dispatchPrice(symbol string, price float64) {
	c.mu.RLock()
	hs := c.priceHandlers
	c.mu.RUnlock()
	for _, h := range hs {
		h(symbol, price)
	}
}
