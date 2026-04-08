package app

import (
	"fmt"

	"orchestrator/internal/infra/exchange"
	alpacaaction "orchestrator/internal/infra/exchange/alpaca/action"
	alpacaex "orchestrator/internal/infra/exchange/alpaca/ex"
	binanceaction "orchestrator/internal/infra/exchange/binance/action"
	binanceex "orchestrator/internal/infra/exchange/binance/ex"
	bybitaction "orchestrator/internal/infra/exchange/bybit/action"
	bybitex "orchestrator/internal/infra/exchange/bybit/ex"
	"orchestrator/internal/infra/exchange/oanda"
	okxaction "orchestrator/internal/infra/exchange/okx/action"
	okxex "orchestrator/internal/infra/exchange/okx/ex"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
)

// exchangeFactory implements runtime.ExchangeFactory.
type exchangeFactory struct{}

func newExchangeFactory() *exchangeFactory { return &exchangeFactory{} }

func (f *exchangeFactory) New(cfg orchdomain.ExchangeConfig) (exchange.Exchange, error) {
	switch cfg.BrokerType {
	case "okx":
		return okxaction.New(okxaction.Config{
			APIKey:     cfg.APIKey,
			APISecret:  cfg.APISecret,
			Passphrase: cfg.Passphrase,
			BaseURL:    cfg.BaseURL,
			Demo:       cfg.Demo,
		}), nil
	case "binance":
		return binanceaction.New(binanceaction.Config{
			APIKey:    cfg.APIKey,
			APISecret: cfg.APISecret,
			BaseURL:   cfg.BaseURL,
			Testnet:   cfg.Testnet,
		}), nil
	case "bybit":
		return bybitaction.New(bybitaction.Config{
			APIKey:    cfg.APIKey,
			APISecret: cfg.APISecret,
			BaseURL:   cfg.BaseURL,
			Testnet:   cfg.Testnet,
		}), nil
	case "oanda":
		return oanda.New(oanda.Config{
			Token:     cfg.APIKey,
			AccountID: cfg.AccountID,
			BaseURL:   cfg.BaseURL,
		}), nil
	case "alpaca", "":
		baseURL := cfg.BaseURL
		if baseURL == "" && cfg.Demo {
			baseURL = "https://paper-api.alpaca.markets"
		}
		return alpacaaction.New(alpacaaction.Config{
			APIKey:    cfg.APIKey,
			APISecret: cfg.APISecret,
			BaseURL:   baseURL,
		}), nil
	default:
		return nil, fmt.Errorf("unknown exchange: %q (supported: alpaca, binance, okx, bybit, oanda)", cfg.BrokerType)
	}
}

// marketStreamerFactory implements runtime.MarketStreamerFactory.
type marketStreamerFactory struct{}

func newMarketStreamerFactory() *marketStreamerFactory { return &marketStreamerFactory{} }

func (f *marketStreamerFactory) New(cfg orchdomain.ExchangeConfig) exchange.MarketStreamer {
	switch cfg.BrokerType {
	case "binance":
		return binanceex.New(cfg.Testnet)
	case "bybit":
		return bybitex.New(cfg.Testnet)
	case "okx":
		return okxex.New(cfg.Demo)
	case "alpaca", "":
		return alpacaex.New(cfg.APIKey, cfg.APISecret)
	default:
		return nil
	}
}
