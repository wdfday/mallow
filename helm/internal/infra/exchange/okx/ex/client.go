// Package ex provides the shared market-data streaming client for OKX.
// One Client is created per broker type — all helms using OKX
// register their price/quote callbacks here and receive live data.
package ex

import (
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// PriceHandler is called with each live trade price.
type PriceHandler = func(symbol string, price decimal.Decimal)

// QuoteHandler is called with each best-bid/offer update.
type QuoteHandler = func(symbol string, bid, ask float64)

// BookHandler is called with each L2 order book update.
type BookHandler = func(symbol string, book L2Book)

// BBO holds the best bid and ask for a symbol.
type BBO struct {
	Bid float64
	Ask float64
}

// Level is a single price level in the order book.
type Level struct {
	Price float64
	Size  float64
}

// L2Book holds the top-5 bid/ask levels for a symbol.
type L2Book struct {
	Bids [5]Level // descending: best bid first
	Asks [5]Level // ascending: best ask first
}

// Client is a shared, broker-level market data WebSocket client for OKX.
// Uses the public WS endpoint (no API key required).
type Client struct {
	paper bool

	mu            sync.RWMutex
	priceHandlers []PriceHandler
	quoteHandlers []QuoteHandler
	bookHandlers  []BookHandler
	bboCache      map[string]BBO    // instId → latest BBO
	l2Cache       map[string]L2Book // instId → latest L2 book
}

// New creates a shared OKX market data streaming client.
// Set paper=true to use the OKX paper trading WS endpoint.
func New(paper bool) *Client {
	return &Client{
		paper:    paper,
		bboCache: make(map[string]BBO),
		l2Cache:  make(map[string]L2Book),
	}
}

// AddPriceHandler registers a callback fired on every live trade price.
func (c *Client) AddPriceHandler(h PriceHandler) {
	c.mu.Lock()
	c.priceHandlers = append(c.priceHandlers, h)
	c.mu.Unlock()
}

// AddQuoteHandler registers a callback fired on every BBO update.
func (c *Client) AddQuoteHandler(h QuoteHandler) {
	c.mu.Lock()
	c.quoteHandlers = append(c.quoteHandlers, h)
	c.mu.Unlock()
}

// addRawBookHandler registers an internal callback using the package-local L2Book type.
func (c *Client) addRawBookHandler(h BookHandler) {
	c.mu.Lock()
	c.bookHandlers = append(c.bookHandlers, h)
	c.mu.Unlock()
}

// AddBookHandler implements exchange.BookStreamer.
// Converts the internal float64 L2Book to exchange.L2Snapshot on each dispatch.
func (c *Client) AddBookHandler(h func(exchange.L2Snapshot)) {
	c.addRawBookHandler(func(symbol string, book L2Book) {
		snap := exchange.L2Snapshot{Symbol: symbol, Timestamp: time.Now()}
		for i, b := range book.Bids {
			snap.Bids[i] = exchange.L2Level{
				Price: decimal.NewFromFloat(b.Price),
				Size:  decimal.NewFromFloat(b.Size),
			}
		}
		for i, a := range book.Asks {
			snap.Asks[i] = exchange.L2Level{
				Price: decimal.NewFromFloat(a.Price),
				Size:  decimal.NewFromFloat(a.Size),
			}
		}
		h(snap)
	})
}

// GetBBO returns the cached best bid/ask for a symbol.
// ok=false if no data has been received yet.
func (c *Client) GetBBO(symbol string) (bid, ask float64, ok bool) {
	c.mu.RLock()
	bbo, ok := c.bboCache[symbol]
	c.mu.RUnlock()
	return bbo.Bid, bbo.Ask, ok
}

// GetL2 returns the cached top-5 order book for a symbol.
// ok=false if no data has been received yet.
func (c *Client) GetL2(symbol string) (L2Book, bool) {
	c.mu.RLock()
	book, ok := c.l2Cache[symbol]
	c.mu.RUnlock()
	return book, ok
}

func (c *Client) dispatchPrice(symbol string, price decimal.Decimal) {
	c.mu.RLock()
	hs := c.priceHandlers
	c.mu.RUnlock()
	for _, h := range hs {
		h(symbol, price)
	}
}

func (c *Client) dispatchQuote(symbol string, bid, ask float64) {
	c.mu.Lock()
	c.bboCache[symbol] = BBO{Bid: bid, Ask: ask}
	hs := c.quoteHandlers
	c.mu.Unlock()
	for _, h := range hs {
		h(symbol, bid, ask)
	}
}

func (c *Client) dispatchBook(symbol string, book L2Book) {
	c.mu.Lock()
	c.l2Cache[symbol] = book
	hs := c.bookHandlers
	c.mu.Unlock()
	for _, h := range hs {
		h(symbol, book)
	}
}
