package app

import (
	"fmt"
	"sync"

	"mallow/helm/internal/config"
	"mallow/helm/internal/infra/exchange"
	alpacaact "mallow/helm/internal/infra/exchange/alpaca/act"
	alpacaex "mallow/helm/internal/infra/exchange/alpaca/ex"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	binanceex "mallow/helm/internal/infra/exchange/binance/ex"
	bybitact "mallow/helm/internal/infra/exchange/bybit/act"
	bybitex "mallow/helm/internal/infra/exchange/bybit/ex"
	okxact "mallow/helm/internal/infra/exchange/okx/act"
	okxex "mallow/helm/internal/infra/exchange/okx/ex"
	orchdomain "mallow/helm/internal/module/helm/domain"
)

// exchangeFactory implements runtime.ExchangeFactory.
// Returns a stateless shared client per (broker, env) — no credentials stored.
type exchangeFactory struct {
	mu      sync.Mutex
	clients map[string]exchange.Exchange // cache key: "brokerType|baseURL|paper"
}

func newExchangeFactory() *exchangeFactory {
	return &exchangeFactory{clients: make(map[string]exchange.Exchange)}
}

func (f *exchangeFactory) New(cfg orchdomain.ExchangeConfig) (exchange.Exchange, error) {
	key := fmt.Sprintf("%s|%s|paper=%v", cfg.BrokerType, cfg.BaseURL, cfg.Paper)
	f.mu.Lock()
	defer f.mu.Unlock()
	if cl, ok := f.clients[key]; ok {
		return cl, nil
	}
	cl, err := f.create(cfg)
	if err != nil {
		return nil, err
	}
	f.clients[key] = cl
	return cl, nil
}

func (f *exchangeFactory) create(cfg orchdomain.ExchangeConfig) (exchange.Exchange, error) {
	switch cfg.BrokerType {
	case "okx":
		return okxact.New(okxact.Config{
			BaseURL: cfg.BaseURL,
			Paper:   cfg.Paper,
		}), nil
	case "binance":
		return binanceact.New(cfg.Paper), nil
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
		return nil, fmt.Errorf("unknown exchange: %q (supported: alpaca, binance, okx, bybit)", cfg.BrokerType)
	}
}

// marketStreamerFactory implements runtime.MarketStreamerFactory.
// Returns a shared market data streaming client per broker type.
type marketStreamerFactory struct {
	cfg config.MarketDataConfig
}

func newMarketStreamerFactory(cfg *config.Config) *marketStreamerFactory {
	return &marketStreamerFactory{cfg: cfg.MarketData}
}

func (f *marketStreamerFactory) New(cfg orchdomain.ExchangeConfig) exchange.MarketStreamer {
	switch cfg.BrokerType {
	case "binance":
		return binanceex.New(cfg.Paper)
	case "bybit":
		return bybitex.New(cfg.Paper)
	case "okx":
		return okxex.New(cfg.Paper)
	case "alpaca", "":
		// Use shared admin-level key from service config (not per-account).
		return alpacaex.New(f.cfg.AlpacaAPIKey, f.cfg.AlpacaAPISecret)
	default:
		return nil
	}
}
