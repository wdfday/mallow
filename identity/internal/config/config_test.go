package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Config loading — all through env vars (no file)
// ---------------------------------------------------------------------------

// setEnv sets multiple env vars and returns a cleanup func.
func setEnv(t *testing.T, pairs map[string]string) {
	t.Helper()
	for k, v := range pairs {
		t.Setenv(k, v)
	}
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	// Env vars must always win over .env file values.
	t.Setenv("SERVER_PORT", "9999")
	t.Setenv("NATS_URL", "nats://override:4222")

	cfg := Load()

	assert.Equal(t, "9999", cfg.Server.Port)
	assert.Equal(t, "nats://override:4222", cfg.NATS.URL)
	// URL field should always contain a non-empty value (from .env or default)
	assert.NotEmpty(t, cfg.Database.URL)
	assert.NotEmpty(t, cfg.Redis.URL)
}

func TestLoad_CustomEnvVars(t *testing.T) {
	setEnv(t, map[string]string{
		"SERVER_PORT":     "9090",
		"GIN_MODE":        "release",
		"POSTGRES_URL":    "postgres://test:test@db:5432/test",
		"REDIS_URL":       "redis://redis-host:6379/1",
		"JWT_SECRET":      "my-production-secret",
		"JWT_ACCESS_TTL":  "1h",
		"JWT_REFRESH_TTL": "72h",
		"NATS_URL":        "nats://nats-host:4222",
		"CORS_ORIGINS":    "https://app.example.com,https://admin.example.com",
	})

	cfg := Load()

	assert.Equal(t, "9090", cfg.Server.Port)
	assert.Equal(t, "release", cfg.Server.GinMode)
	assert.Equal(t, "postgres://test:test@db:5432/test", cfg.Database.URL)
	assert.Equal(t, "redis://redis-host:6379/1", cfg.Redis.URL)
	assert.Equal(t, "my-production-secret", cfg.JWT.Secret)
	assert.Equal(t, time.Hour, cfg.JWT.AccessTTL)
	assert.Equal(t, 72*time.Hour, cfg.JWT.RefreshTTL)
	assert.Equal(t, "nats://nats-host:4222", cfg.NATS.URL)
	assert.Equal(t, []string{"https://app.example.com", "https://admin.example.com"}, cfg.CORS.AllowedOrigins)
}

func TestLoad_JWTDefaults(t *testing.T) {
	cfg := Load()

	assert.False(t, cfg.JWT.CookieSecure) // default false
	assert.True(t, cfg.JWT.CookieHTTPOnly)
	assert.Equal(t, "lax", cfg.JWT.CookieSameSite)
	assert.Equal(t, 7*24*60*60, cfg.JWT.CookieMaxAge)
}

func TestLoad_JWTCookieEnv(t *testing.T) {
	setEnv(t, map[string]string{
		"COOKIE_SECURE":    "true",
		"COOKIE_SAME_SITE": "strict",
		"COOKIE_MAX_AGE":   "3600",
	})

	cfg := Load()

	assert.True(t, cfg.JWT.CookieSecure)
	assert.Equal(t, "strict", cfg.JWT.CookieSameSite)
	assert.Equal(t, 3600, cfg.JWT.CookieMaxAge)
}

func TestLoad_BrokerSync(t *testing.T) {
	cfg := Load()
	assert.True(t, cfg.BrokerSync.Enabled)
	assert.Equal(t, 60, cfg.BrokerSync.IntervalMin)
	assert.Equal(t, 5, cfg.BrokerSync.MaxConcurrent)
	assert.Equal(t, 10, cfg.BrokerSync.TimeoutMin)
}

func TestLoad_BrokerSyncEnv(t *testing.T) {
	setEnv(t, map[string]string{
		"BROKER_SYNC_ENABLED":        "false",
		"BROKER_SYNC_INTERVAL_MIN":   "30",
		"BROKER_SYNC_MAX_CONCURRENT": "10",
		"BROKER_SYNC_TIMEOUT_MIN":    "5",
	})

	cfg := Load()
	assert.False(t, cfg.BrokerSync.Enabled)
	assert.Equal(t, 30, cfg.BrokerSync.IntervalMin)
	assert.Equal(t, 10, cfg.BrokerSync.MaxConcurrent)
	assert.Equal(t, 5, cfg.BrokerSync.TimeoutMin)
}

func TestLoad_SMTP(t *testing.T) {
	setEnv(t, map[string]string{
		"SMTP_HOST":      "smtp.mailgun.org",
		"SMTP_PORT":      "465",
		"SMTP_USERNAME":  "postmaster@example.com",
		"SMTP_PASSWORD":  "secret",
		"SMTP_FROM":      "no-reply@example.com",
		"SMTP_FROM_NAME": "My App",
	})

	cfg := Load()
	assert.Equal(t, "smtp.mailgun.org", cfg.SMTP.Host)
	assert.Equal(t, 465, cfg.SMTP.Port)
	assert.Equal(t, "postmaster@example.com", cfg.SMTP.Username)
	assert.Equal(t, "secret", cfg.SMTP.Password)
	assert.Equal(t, "no-reply@example.com", cfg.SMTP.From)
	assert.Equal(t, "My App", cfg.SMTP.FromName)
}

func TestLoad_MailWorker(t *testing.T) {
	setEnv(t, map[string]string{
		"MAIL_NATS_SUBJECT":       "email.jobs",
		"MAIL_WORKER_CONCURRENCY": "5",
		"MAIL_WORKER_BUFFER":      "200",
	})

	cfg := Load()
	assert.Equal(t, "email.jobs", cfg.Mail.Subject)
	assert.Equal(t, 5, cfg.Mail.Concurrency)
	assert.Equal(t, 200, cfg.Mail.BufferSize)
}

func TestLoad_Telegram(t *testing.T) {
	setEnv(t, map[string]string{
		"TELEGRAM_GENERAL_BOT_TOKEN": "123456:ABC-DEF",
		"TELEGRAM_ALLOWED_CHATS":     "111,222,333",
	})

	cfg := Load()
	assert.Equal(t, "123456:ABC-DEF", cfg.Telegram.Token)
	assert.Equal(t, "111,222,333", cfg.Telegram.AllowedChats)
}

func TestLoad_AdminSeed(t *testing.T) {
	setEnv(t, map[string]string{
		"ADMIN_SEED_EMAIL":    "admin@company.com",
		"ADMIN_SEED_PASSWORD": "Admin@123",
	})

	cfg := Load()
	assert.Equal(t, "admin@company.com", cfg.AdminSeed.Email)
	assert.Equal(t, "Admin@123", cfg.AdminSeed.Password)
}

func TestLoad_ServiceSecret(t *testing.T) {
	t.Setenv("SERVICE_SECRET", "internal-secret-xyz")
	cfg := Load()
	assert.Equal(t, "internal-secret-xyz", cfg.ServiceSecret)
}

func TestLoad_EncryptionKey(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "my32charexactkeyfortesting12345")
	cfg := Load()
	assert.Equal(t, "my32charexactkeyfortesting12345", cfg.Encryption.Key)
}
