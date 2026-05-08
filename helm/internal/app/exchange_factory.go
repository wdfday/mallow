package app

import (
	"fmt"
	"sync"

	"mallow/helm/internal/config"
	"mallow/helm/internal/infra/exchange"
	alpacaaction "mallow/helm/internal/infra/exchange/alpaca/action"
	alpacaex "mallow/helm/internal/infra/exchange/alpaca/ex"
	binanceaction "mallow/helm/internal/infra/exchange/binance/action"
	binanceex "mallow/helm/internal/infra/exchange/binance/ex"
	bybitaction "mallow/helm/internal/infra/exchange/bybit/action"
	bybitex "mallow/helm/internal/infra/exchange/bybit/ex"
	"mallow/helm/internal/infra/exchange/oanda"
	okxaction "mallow/helm/internal/infra/exchange/okx/action"
	okxex "mallow/helm/internal/infra/exchange/okx/ex"
	orchdomain "mallow/helm/internal/module/helm/domain"
)

// exchangeFactory implements runtime.ExchangeFactory.
// Returns a stateless shared client per (broker, env) — no credentials stored.
type exchangeFactory struct {
	mu      sync.Mutex
	clients map[string]exchange.Exchange // cache key: "brokerType|baseURL|testnet|demo"
}

func newExchangeFactory() *exchangeFactory {
	return &exchangeFactory{clients: make(map[string]exchange.Exchange)}
}

func (f *exchangeFactory) New(cfg orchdomain.ExchangeConfig) (exchange.Exchange, error) {
	key := fmt.Sprintf("%s|%s|testnet=%v|demo=%v", cfg.BrokerType, cfg.BaseURL, cfg.Testnet, cfg.Demo)
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
		return okxaction.New(okxaction.Config{
			BaseURL: cfg.BaseURL,
			Demo:    cfg.Demo,
		}), nil
	case "binance":
		return binanceaction.New(cfg.Testnet), nil
	case "bybit":
		return bybitaction.New(bybitaction.Config{
			BaseURL: cfg.BaseURL,
			Testnet: cfg.Testnet,
		}), nil
	case "oanda":
		return oanda.New(oanda.Config{
			BaseURL: cfg.BaseURL,
		}), nil
	case "alpaca", "":
		baseURL := cfg.BaseURL
		if baseURL == "" && cfg.Demo {
			baseURL = "https://paper-api.alpaca.markets"
		}
		return alpacaaction.New(alpacaaction.Config{
			BaseURL: baseURL,
		}), nil
	default:
		return nil, fmt.Errorf("unknown exchange: %q (supported: alpaca, binance, okx, bybit, oanda)", cfg.BrokerType)
	}
}

// marketStreamerFactory implements runtime.MarketStreamerFactory.
// Returns a shared market data streaming client per broker type.
type marketStreamerFactory struct {
	cfg config.MarketDataConfig
}

func newMarketStreamerFactory(cfg config.Config) *marketStreamerFactory {
	return &marketStreamerFactory{cfg: cfg.MarketData}
}

func (f *marketStreamerFactory) New(cfg orchdomain.ExchangeConfig) exchange.MarketStreamer {
	switch cfg.BrokerType {
	case "binance":
		return binanceex.New(cfg.Testnet)
	case "bybit":
		return bybitex.New(cfg.Testnet)
	case "okx":
		return okxex.New(cfg.Demo)
	case "alpaca", "":
		// Use shared admin-level key from service config (not per-account).
		return alpacaex.New(f.cfg.AlpacaAPIKey, f.cfg.AlpacaAPISecret)
	default:
		return nil
	}
}
