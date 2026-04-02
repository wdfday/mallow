// Package ex provides the shared market-data streaming client for Bybit.
// One Client is created per broker type — all orchestrators using Bybit
// register their UpdatePrice callback here and receive live prices.
package ex

import (
	"sync"
)

// PriceHandler is called with each live price update.
type PriceHandler = func(symbol string, price float64)

// Client is a shared, broker-level market data WebSocket client for Bybit.
// Uses the public V5 WebSocket endpoints (no API key required).
type Client struct {
	testnet bool

	mu            sync.RWMutex
	priceHandlers []PriceHandler
}

// New creates a shared Bybit market data streaming client.
func New(testnet bool) *Client {
	return &Client{testnet: testnet}
}

// AddPriceHandler registers a callback fired on every live price update.
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
