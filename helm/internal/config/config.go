package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/viper"
)

// Config holds process-level helm configuration.
//
// Important boundary:
//   - Per-account execution/broker settings belong to persisted helm configs in the domain layer.
//   - Env config here is only for the service process itself: infra, HTTP/NATS, scheduling.
//
// Public market-data streaming (price, filters, L2 book) is not configured here —
// runtime/market self-connects to whichever exchanges have live hands, derived
// from handservice.SymbolsByExchange() at startup. See runtime/market/stream.go.
type Config struct {
	Infra   InfraConfig
	Server  ServerConfig
	Runtime RuntimeConfig
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

// Load reads configuration from environment variables with sensible defaults.
func Load() Config {
	v := viper.New()
	v.SetDefault("NATS_URL", "nats://localhost:4222")
	v.SetDefault("POSTGRES_URL", "")
	v.SetDefault("ENCRYPTION_KEY", "")
	v.SetDefault("API_ADDR", "localhost:8084")
	v.SetDefault("PYROSCOPE_URL", "")
	v.SetDefault("SYNC_INTERVAL", "5m")

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
	}

	slog.Info("loaded config",
		"nats_url", cfg.Infra.NATSURL,
		"postgres_enabled", cfg.Infra.PostgresURL != "",
		"sync_interval", cfg.Runtime.SyncInterval,
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

// findDotEnv locates the service env file.
//
// Search order (stops at first match):
//  1. deployment/environments/helm.env relative to the repo root (.git dir)
//  2. helm/.env relative to the repo root (legacy fallback)
func findDotEnv() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			// Found repo root — check new canonical location first.
			if candidate := filepath.Join(dir, "deployment", "environments", "helm.env"); fileExists(candidate) {
				return candidate
			}
			// Legacy fallback: helm/.env at repo root.
			if candidate := filepath.Join(dir, "helm", ".env"); fileExists(candidate) {
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
