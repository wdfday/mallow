package app

import (
	"fmt"
	"mallow/helm/internal/infra/exchange"
	alpacaact "mallow/helm/internal/infra/exchange/alpaca/act"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	bybitact "mallow/helm/internal/infra/exchange/bybit/act"
	fbinanceact "mallow/helm/internal/infra/exchange/fbinance/act"
	okxact "mallow/helm/internal/infra/exchange/okx/act"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"sync"
)

// exchangeFactory implements runtime.ExchangeFactory.
// Returns a stateless shared REST client per (broker, env) — credentials are
// injected at request time via HelmRuntime.Creds, not stored in the client.
// Public market data streaming is handled separately by buildMarketDataListener
// in lifecycle.go (hardcoded per MARKET_DATA_SOURCE env).
type exchangeFactory struct {
	mu      sync.Mutex
	clients map[string]exchange.Exchange // cache key: "brokerType|baseURL|paper"
}

func newExchangeFactory() *exchangeFactory {
	return &exchangeFactory{clients: make(map[string]exchange.Exchange)}
}

func (f *exchangeFactory) New(cfg helmdomain.ExchangeConfig) (exchange.Exchange, error) {
	key := fmt.Sprintf("%s|%s|paper=%v", cfg.BrokerType, cfg.BaseURL, cfg.Paper)
	f.mu.Lock()
	defer f.mu.Unlock()
	if cl, ok := f.clients[key]; ok {
		return cl, nil
	}
	raw, err := f.create(cfg)
	if err != nil {
		return nil, err
	}
	// Wrap with MeteredExchange so PlaceOrder/GetOrder/CancelOrder latency
	// and error rates are tracked and exposed via /metrics.
	cl := exchange.NewMeteredExchange(raw)
	f.clients[key] = cl
	return cl, nil
}

func (f *exchangeFactory) create(cfg helmdomain.ExchangeConfig) (exchange.Exchange, error) {
	switch cfg.BrokerType {
	case "okx":
		return okxact.New(okxact.Config{
			BaseURL: cfg.BaseURL,
			Paper:   cfg.Paper,
		}), nil
	case "binance":
		return binanceact.New(cfg.Paper), nil
	case "fbinance":
		return fbinanceact.New(cfg.Paper), nil
	case "bybit":
		return bybitact.New(bybitact.Config{
			BaseURL: cfg.BaseURL,
			Paper:   cfg.Paper,
		}), nil
	case "alpaca", "":
		baseURL := cfg.BaseURL
		if baseURL == "" && cfg.Paper {
			baseURL = "https://paper-api.alpaca.markets"
		}
		return alpacaact.New(alpacaact.Config{
			BaseURL: baseURL,
		}), nil
	default:
		return nil, fmt.Errorf("unknown exchange: %q (supported: alpaca, binance, fbinance, okx, bybit)", cfg.BrokerType)
	}
}
