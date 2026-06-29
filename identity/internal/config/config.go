package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
	"go.uber.org/fx"
)

// Module provides config to fx.
var Module = fx.Module("config", fx.Provide(Load))

// Config holds all application configuration.
type Config struct {
	Server        ServerConfig
	Database      DatabaseConfig
	Redis         RedisConfig
	JWT           JWTConfig
	Encryption    EncryptionConfig
	CORS          CORSConfig
	BrokerSync    BrokerSyncConfig
	NATS          NATSConfig
	ServiceSecret string
	SMTP          SMTPConfig
	Mail          MailWorkerConfig
	Telegram      TelegramConfig
	Google        GoogleConfig
	AdminSeed     AdminSeedConfig
}

type AdminSeedConfig struct {
	Email    string
	Password string
}

type ServerConfig struct {
	Port    string
	GinMode string // debug | release | test
}

type DatabaseConfig struct {
	URL string
}

type RedisConfig struct {
	URL string
}

type JWTConfig struct {
	Secret     string
	PrivateKey string
	PublicKey  string
	KeyID      string
	Issuer     string
	AccessTTL  time.Duration
	RefreshTTL time.Duration

	CookieDomain   string
	CookieSecure   bool
	CookieHTTPOnly bool
	CookieSameSite string
	CookieMaxAge   int
}

type EncryptionConfig struct {
	Key string
}

type CORSConfig struct {
	AllowedOrigins []string
}

type BrokerSyncConfig struct {
	Enabled       bool
	IntervalMin   int
	MaxConcurrent int
	TimeoutMin    int
}

type NATSConfig struct {
	URL string
}

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
}

type MailWorkerConfig struct {
	Subject     string
	Concurrency int
	BufferSize  int
}

type TelegramConfig struct {
	Token        string
	BotUsername  string
	AllowedChats string
}

type GoogleConfig struct {
	ClientID string
}

// Load reads configuration using Viper.
// Priority (highest → lowest):
//  1. Actual environment variables set in the shell
//  2. Values from the nearest .env file (walks up from cwd)
//  3. Built-in defaults
func Load() *Config {
	v := viper.New()

	// ── Defaults ────────────────────────────────────────────────────────────
	v.SetDefault("SERVER_PORT", "8080")
	v.SetDefault("GIN_MODE", "debug")
	v.SetDefault("POSTGRES_URL", "postgres://mallow:mallow-dev@localhost:5432/identity?sslmode=disable")
	v.SetDefault("REDIS_URL", "redis://localhost:6379/0")
	v.SetDefault("NATS_URL", "nats://localhost:4222")
	v.SetDefault("JWT_SECRET", "change-me-in-production")
	v.SetDefault("JWT_PRIVATE_KEY", "")
	v.SetDefault("JWT_PUBLIC_KEY", "")
	v.SetDefault("JWT_KEY_ID", "identity-ed25519-k1")
	v.SetDefault("JWT_ISSUER", "http://localhost:8082")
	v.SetDefault("JWT_ACCESS_TTL", "24h")
	v.SetDefault("JWT_REFRESH_TTL", "168h")
	v.SetDefault("COOKIE_SECURE", false)
	v.SetDefault("COOKIE_SAME_SITE", "lax")
	v.SetDefault("COOKIE_MAX_AGE", 7*24*60*60)
	v.SetDefault("ENCRYPTION_KEY", "")
	v.SetDefault("CORS_ORIGINS", "http://localhost:3000")
	v.SetDefault("BROKER_SYNC_ENABLED", true)
	v.SetDefault("BROKER_SYNC_INTERVAL_MIN", 60)
	v.SetDefault("BROKER_SYNC_MAX_CONCURRENT", 5)
	v.SetDefault("BROKER_SYNC_TIMEOUT_MIN", 10)
	v.SetDefault("SERVICE_SECRET", "")
	v.SetDefault("SMTP_HOST", "smtp.gmail.com")
	v.SetDefault("SMTP_PORT", 587)
	v.SetDefault("SMTP_USERNAME", "")
	v.SetDefault("SMTP_PASSWORD", "")
	v.SetDefault("SMTP_FROM", "")
	v.SetDefault("SMTP_FROM_NAME", "Identity Service")
	v.SetDefault("MAIL_NATS_SUBJECT", "mail.send")
	v.SetDefault("MAIL_WORKER_CONCURRENCY", 3)
	v.SetDefault("MAIL_WORKER_BUFFER", 100)
	v.SetDefault("TELEGRAM_GENERAL_BOT_TOKEN", "")
	v.SetDefault("TELEGRAM_BOT_USERNAME", "")
	v.SetDefault("TELEGRAM_ALLOWED_CHATS", "")
	v.SetDefault("GOOGLE_CLIENT_ID", "")
	v.SetDefault("ADMIN_SEED_EMAIL", "")
	v.SetDefault("ADMIN_SEED_PASSWORD", "")

	// ── .env file — walk up from cwd until found (works with go.work) ────────
	if envFile := findDotEnv(); envFile != "" {
		v.SetConfigFile(envFile)
		v.SetConfigType("env") // must be set explicitly — Viper doesn't infer from .env extension
		_ = v.ReadInConfig()
	}

	// ── Environment variables override .env file ──────────────────────────
	v.AutomaticEnv()

	return &Config{
		Server: ServerConfig{
			Port:    v.GetString("SERVER_PORT"),
			GinMode: v.GetString("GIN_MODE"),
		},
		Database: DatabaseConfig{
			URL: v.GetString("POSTGRES_URL"),
		},
		Redis: RedisConfig{
			URL: v.GetString("REDIS_URL"),
		},
		JWT: JWTConfig{
			Secret:         v.GetString("JWT_SECRET"),
			PrivateKey:     v.GetString("JWT_PRIVATE_KEY"),
			PublicKey:      v.GetString("JWT_PUBLIC_KEY"),
			KeyID:          v.GetString("JWT_KEY_ID"),
			Issuer:         v.GetString("JWT_ISSUER"),
			AccessTTL:      parseDuration(v.GetString("JWT_ACCESS_TTL")),
			RefreshTTL:     parseDuration(v.GetString("JWT_REFRESH_TTL")),
			CookieDomain:   v.GetString("COOKIE_DOMAIN"),
			CookieSecure:   v.GetBool("COOKIE_SECURE"),
			CookieHTTPOnly: true,
			CookieSameSite: v.GetString("COOKIE_SAME_SITE"),
			CookieMaxAge:   v.GetInt("COOKIE_MAX_AGE"),
		},
		Encryption: EncryptionConfig{
			Key: v.GetString("ENCRYPTION_KEY"),
		},
		CORS: CORSConfig{
			AllowedOrigins: splitComma(v.GetString("CORS_ORIGINS")),
		},
		BrokerSync: BrokerSyncConfig{
			Enabled:       v.GetBool("BROKER_SYNC_ENABLED"),
			IntervalMin:   v.GetInt("BROKER_SYNC_INTERVAL_MIN"),
			MaxConcurrent: v.GetInt("BROKER_SYNC_MAX_CONCURRENT"),
			TimeoutMin:    v.GetInt("BROKER_SYNC_TIMEOUT_MIN"),
		},
		NATS: NATSConfig{
			URL: v.GetString("NATS_URL"),
		},
		ServiceSecret: v.GetString("SERVICE_SECRET"),
		SMTP: SMTPConfig{
			Host:     v.GetString("SMTP_HOST"),
			Port:     v.GetInt("SMTP_PORT"),
			Username: v.GetString("SMTP_USERNAME"),
			Password: v.GetString("SMTP_PASSWORD"),
			From:     v.GetString("SMTP_FROM"),
			FromName: v.GetString("SMTP_FROM_NAME"),
		},
		Mail: MailWorkerConfig{
			Subject:     v.GetString("MAIL_NATS_SUBJECT"),
			Concurrency: v.GetInt("MAIL_WORKER_CONCURRENCY"),
			BufferSize:  v.GetInt("MAIL_WORKER_BUFFER"),
		},
		Telegram: TelegramConfig{
			Token:        v.GetString("TELEGRAM_GENERAL_BOT_TOKEN"),
			BotUsername:  v.GetString("TELEGRAM_BOT_USERNAME"),
			AllowedChats: v.GetString("TELEGRAM_ALLOWED_CHATS"),
		},
		Google: GoogleConfig{
			ClientID: strings.TrimSpace(v.GetString("GOOGLE_CLIENT_ID")),
		},
		AdminSeed: AdminSeedConfig{
			Email:    v.GetString("ADMIN_SEED_EMAIL"),
			Password: v.GetString("ADMIN_SEED_PASSWORD"),
		},
	}
}

func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 24 * time.Hour
	}
	return d
}

func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// findDotEnv locates the service env file.
//
// Search order (stops at first match):
//  1. deployment/environments/identity.env relative to the repo root (.git dir)
//  2. identity/.env relative to the repo root (legacy fallback)
func findDotEnv() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			// Found repo root — check new canonical location first.
			if candidate := filepath.Join(dir, "deployment", "environments", "identity.env"); fileExists(candidate) {
				return candidate
			}
			// Legacy fallback: identity/.env at repo root.
			if candidate := filepath.Join(dir, "identity", ".env"); fileExists(candidate) {
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
