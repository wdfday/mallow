package app

import (
	"stream-data/internal/provider/alpaca"
	"stream-data/internal/provider/binance"
	"stream-data/internal/provider/okx"
	"stream-data/internal/provider/twelvedata"
	"stream-data/internal/stream"
)

// BuildBarProviders returns providers that emit completed 1m bars directly.
func BuildBarProviders(cfg *Config) []stream.BarRegistration {
	var regs []stream.BarRegistration

	if cfg.Sources.Binance.Enabled {
		regs = append(regs, stream.BarRegistration{
			Provider: binance.New(),
			Symbols:  cfg.Sources.Binance.Symbols,
		})
	}

	if cfg.Sources.OKX.Enabled {
		regs = append(regs, stream.BarRegistration{
			Provider: okx.New(),
			Symbols:  cfg.Sources.OKX.Symbols,
		})
	}

	if cfg.Sources.Alpaca.Enabled {
		regs = append(regs, stream.BarRegistration{
			Provider: alpaca.New(cfg.Sources.Alpaca.APIKey, cfg.Sources.Alpaca.APISecret),
			Symbols:  cfg.Sources.Alpaca.Symbols,
		})
	}

	return regs
}

// BuildTickProviders returns providers that emit raw ticks (e.g. TwelveData forex).
func BuildTickProviders(cfg *Config) []stream.Registration {
	var regs []stream.Registration

	if cfg.Sources.TwelveData.Enabled {
		regs = append(regs, stream.Registration{
			Provider: twelvedata.New(cfg.Sources.TwelveData.APIKey),
			Symbols:  cfg.Sources.TwelveData.Symbols,
		})
	}

	return regs
}
