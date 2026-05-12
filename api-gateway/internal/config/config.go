package config

import (
	"os"
	"strings"
)

type Config struct {
	Port          string
	NatsURL       string
	JWTSecret     string
	JWTPublicKey  string
	JWTJWKSURL    string
	JWTIssuer     string
	JWKSCacheTTL  string
	StrategistURL string
	IdentityURL   string
	InvestmentURL string
	HelmURL       string
	HeraldURL     string
	RedisURL      string
	ServiceSecret string
	CORSOrigins   []string
}

func Load() Config {
	return Config{
		Port:          envOr("PORT", "8080"),
		NatsURL:       envOr("NATS_URL", "nats://localhost:4222"),
		JWTSecret:     envOr("JWT_SECRET", "mallow-dev-secret-change-in-prod"),
		JWTPublicKey:  envOr("JWT_PUBLIC_KEY", ""),
		JWTJWKSURL:    envOr("JWT_JWKS_URL", ""),
		JWTIssuer:     envOr("JWT_ISSUER", ""),
		JWKSCacheTTL:  envOr("JWT_JWKS_CACHE_TTL", "5m"),
		StrategistURL: envOr("STRATEGIST_URL", "http://127.0.0.1:8081"),
		IdentityURL:   envOr("IDENTITY_URL", "http://127.0.0.1:8082"),
		InvestmentURL: envOr("INVESTMENT_URL", "http://127.0.0.1:8083"),
		HelmURL:       envOr("HELM_URL", "http://127.0.0.1:8084"),
		HeraldURL:     envOr("HERALD_URL", "http://127.0.0.1:8090"),
		RedisURL:      envOr("REDIS_URL", "redis://127.0.0.1:6379"),
		ServiceSecret: envOr("SERVICE_SECRET", ""),
		CORSOrigins:   splitCSV(envOr("CORS_ORIGINS", "https://forge.m4llow.com,http://localhost:5173,http://localhost:8080")),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
