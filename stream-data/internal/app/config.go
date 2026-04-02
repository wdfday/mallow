package app

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

var logLevel = new(slog.LevelVar)

type Config struct {
	Data struct {
		Dir           string        `mapstructure:"dir"`
		Format        string        `mapstructure:"format"`
		FlushInterval time.Duration `mapstructure:"flushInterval"`
		MaxBufferSize int           `mapstructure:"maxBufferSize"` // 0 = time-only flush
	} `mapstructure:"data"`

	Log struct {
		Level  string `mapstructure:"level"`
		Format string `mapstructure:"format"`
		File   string `mapstructure:"file"`
	} `mapstructure:"log"`

	Sources struct {
		Binance struct {
			Enabled bool     `mapstructure:"enabled"`
			Symbols []string `mapstructure:"symbols"`
		} `mapstructure:"binance"`

		Alpaca struct {
			Enabled   bool     `mapstructure:"enabled"`
			APIKey    string   `mapstructure:"apiKey"`
			APISecret string   `mapstructure:"apiSecret"`
			Symbols   []string `mapstructure:"symbols"`
		} `mapstructure:"alpaca"`

		TwelveData struct {
			Enabled bool     `mapstructure:"enabled"`
			APIKey  string   `mapstructure:"apiKey"`
			Symbols []string `mapstructure:"symbols"`
		} `mapstructure:"twelvedata"`

		OKX struct {
			Enabled bool     `mapstructure:"enabled"`
			Symbols []string `mapstructure:"symbols"`
		} `mapstructure:"okx"`
	} `mapstructure:"sources"`

	NATS struct {
		Enabled bool   `mapstructure:"enabled"`
		URL     string `mapstructure:"url"`
	} `mapstructure:"nats"`

	Bars struct {
		Interval time.Duration `mapstructure:"interval"`
	} `mapstructure:"bars"`
}

func LoadConfig() (*Config, error) {
	v := viper.New()

	cfgFile := os.Getenv("CONFIG_FILE")
	if cfgFile == "" {
		cfgFile = "config.yaml"
	}
	v.SetConfigFile(cfgFile)

	v.SetDefault("data.dir", "data/stream")
	v.SetDefault("data.format", "parquet")
	v.SetDefault("data.flushInterval", "1m")
	v.SetDefault("data.maxBufferSize", 50000)
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")
	v.SetDefault("bars.interval", "1m")
	v.SetDefault("nats.url", "nats://localhost:4222")
	_ = v.BindEnv("nats.url", "NATS_URL")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config %q: %w", cfgFile, err)
	}
	slog.Info("loaded config", "file", v.ConfigFileUsed())

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// Env overrides for secrets
	if k := os.Getenv("ALPACA_API_KEY"); k != "" {
		cfg.Sources.Alpaca.APIKey = k
	}
	if s := os.Getenv("ALPACA_API_SECRET"); s != "" {
		cfg.Sources.Alpaca.APISecret = s
	}
	if k := os.Getenv("TWELVEDATA_API_KEY"); k != "" {
		cfg.Sources.TwelveData.APIKey = k
	}

	return &cfg, nil
}

func InitLogger() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))
}

func (c *Config) ApplyLogger() (cleanup func()) {
	logLevel.Set(parseLevel(c.Log.Level))
	cleanup = func() {}

	w := io.Writer(os.Stderr)
	if path := strings.TrimSpace(c.Log.File); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			slog.Warn("log file dir create failed", "path", path, "err", err)
		} else if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err != nil {
			slog.Warn("log file open failed", "path", path, "err", err)
		} else {
			w = io.MultiWriter(os.Stderr, f)
			cleanup = func() { _ = f.Close() }
		}
	}

	opts := &slog.HandlerOptions{Level: logLevel}
	var h slog.Handler
	if strings.ToLower(strings.TrimSpace(c.Log.Format)) == "json" {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	slog.SetDefault(slog.New(h))
	return cleanup
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
