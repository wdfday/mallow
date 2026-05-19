package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config holds process-level helm configuration.
//
// Important boundary:
//   - Per-account execution/broker settings belong to persisted helm configs in the domain layer.
//   - Env config here is only for the service process itself: infra, HTTP/NATS, scheduling, and optional
//     market-data adapters used by the process.
type Config struct {
	Infra      InfraConfig
	Server     ServerConfig
	Runtime    RuntimeConfig
	MarketData MarketDataConfig
}

type InfraConfig struct {
	NATSURL       string
	PostgresURL   string
	EncryptionKey string
}

type ServerConfig struct {
	APIAddr      string
	PyroscopeURL string // PYROSCOPE_URL — empty disables profiling
}

type RuntimeConfig struct {
	SyncInterval time.Duration
}

type MarketDataConfig struct {
	Source          string
	Symbols         []string
	IsCrypto        bool
	AlpacaAPIKey    string // shared key for the Alpaca market data WebSocket streamer
	AlpacaAPISecret string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() Config {
	v := viper.New()
	v.SetDefault("NATS_URL", "nats://localhost:4222")
	v.SetDefault("POSTGRES_URL", "")
	v.SetDefault("ENCRYPTION_KEY", "")
	v.SetDefault("API_ADDR", "localhost:8084")
	v.SetDefault("PYROSCOPE_URL", "")
	v.SetDefault("SYNC_INTERVAL", "5m")
	v.SetDefault("MARKET_DATA_SOURCE", "none")
	v.SetDefault("MARKET_DATA_SYMBOLS", "")
	v.SetDefault("MARKET_DATA_CRYPTO", false)
	v.SetDefault("ALPACA_MD_API_KEY", "")
	v.SetDefault("ALPACA_MD_API_SECRET", "")

	if envFile := findDotEnv(); envFile != "" {
		v.SetConfigFile(envFile)
		v.SetConfigType("env")
		_ = v.ReadInConfig()
	}
	v.AutomaticEnv()

	cfg := Config{
		Infra: InfraConfig{
			NATSURL:       v.GetString("NATS_URL"),
			PostgresURL:   v.GetString("POSTGRES_URL"),
			EncryptionKey: v.GetString("ENCRYPTION_KEY"),
		},
		Server: ServerConfig{
			APIAddr:      v.GetString("API_ADDR"),
			PyroscopeURL: v.GetString("PYROSCOPE_URL"),
		},
		Runtime: RuntimeConfig{
			SyncInterval: getDuration(v, "SYNC_INTERVAL", 5*time.Minute),
		},
		MarketData: MarketDataConfig{
			Source:          v.GetString("MARKET_DATA_SOURCE"),
			Symbols:         getStringSlice(v.GetString("MARKET_DATA_SYMBOLS")),
			IsCrypto:        v.GetBool("MARKET_DATA_CRYPTO"),
			AlpacaAPIKey:    v.GetString("ALPACA_MD_API_KEY"),
			AlpacaAPISecret: v.GetString("ALPACA_MD_API_SECRET"),
		},
	}

	slog.Info("loaded config",
		"nats_url", cfg.Infra.NATSURL,
		"postgres_enabled", cfg.Infra.PostgresURL != "",
		"sync_interval", cfg.Runtime.SyncInterval,
		"market_data_source", cfg.MarketData.Source,
		"market_data_symbols", cfg.MarketData.Symbols,
		"api_addr", cfg.Server.APIAddr,
	)

	return cfg
}

func getDuration(v *viper.Viper, key string, def time.Duration) time.Duration {
	if raw := v.GetString(key); raw != "" {
		d, err := time.ParseDuration(raw)
		if err == nil {
			return d
		}
		slog.Warn("invalid duration env var", "key", key, "value", raw, "err", err)
	}
	return def
}

func getStringSlice(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// findDotEnv locates the .env file by walking up to the module root (go.mod).
func findDotEnv() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			candidate := filepath.Join(dir, ".env")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
