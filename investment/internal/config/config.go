package config

import (
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"go.uber.org/fx"
)

var Module = fx.Module("config", fx.Provide(Load))

type Config struct {
	Server     ServerConfig
	Database   DatabaseConfig
	NATS       NATSConfig
	CORS       CORSConfig
	Encryption EncryptionConfig
	BrokerSync BrokerSyncConfig
}

type EncryptionConfig struct {
	Key string
}

type BrokerSyncConfig struct {
	Enabled       bool
	IntervalMin   int
	MaxConcurrent int
	TimeoutMin    int
}

type ServerConfig struct {
	Port    string
	GinMode string
}

type DatabaseConfig struct {
	URL string
}

type NATSConfig struct {
	URL string
}

type CORSConfig struct {
	AllowedOrigins []string
}

// Load reads configuration using Viper.
// Priority (highest → lowest):
//  1. Actual environment variables set in the shell
//  2. Values from the nearest .env file (walks up from cwd)
//  3. Built-in defaults
func Load() *Config {
	v := viper.New()

	v.SetDefault("PORT", "8083")
	v.SetDefault("GIN_MODE", "debug")
	v.SetDefault("POSTGRES_URL", "postgres://mallow:mallow-dev@localhost:5432/investment?sslmode=disable")
	v.SetDefault("NATS_URL", "nats://localhost:4222")
	v.SetDefault("CORS_ORIGINS", "http://localhost:3000")
	v.SetDefault("ENCRYPTION_KEY", "")
	v.SetDefault("BROKER_SYNC_ENABLED", false)
	v.SetDefault("BROKER_SYNC_INTERVAL_MIN", 60)
	v.SetDefault("BROKER_SYNC_MAX_CONCURRENT", 5)
	v.SetDefault("BROKER_SYNC_TIMEOUT_MIN", 10)

	if envFile := findDotEnv(); envFile != "" {
		v.SetConfigFile(envFile)
		v.SetConfigType("env")
		_ = v.ReadInConfig()
	}

	v.AutomaticEnv()

	return &Config{
		Server: ServerConfig{
			Port:    v.GetString("PORT"),
			GinMode: v.GetString("GIN_MODE"),
		},
		Database: DatabaseConfig{
			URL: v.GetString("POSTGRES_URL"),
		},
		NATS: NATSConfig{
			URL: v.GetString("NATS_URL"),
		},
		CORS: CORSConfig{
			AllowedOrigins: splitComma(v.GetString("CORS_ORIGINS")),
		},
		Encryption: EncryptionConfig{
			Key: v.GetString("ENCRYPTION_KEY"),
		},
		BrokerSync: BrokerSyncConfig{
			Enabled:       v.GetBool("BROKER_SYNC_ENABLED"),
			IntervalMin:   v.GetInt("BROKER_SYNC_INTERVAL_MIN"),
			MaxConcurrent: v.GetInt("BROKER_SYNC_MAX_CONCURRENT"),
			TimeoutMin:    v.GetInt("BROKER_SYNC_TIMEOUT_MIN"),
		},
	}
}

func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if t := trimSpace(s[start:i]); t != "" {
				out = append(out, t)
			}
			start = i + 1
		}
	}
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
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
