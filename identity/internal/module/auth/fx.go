package auth

import (
	"log/slog"

	natsgo "github.com/nats-io/nats.go"
	redisgo "github.com/redis/go-redis/v9"
	"go.uber.org/fx"

	"mallow/identity/internal/config"
	"mallow/identity/internal/module/auth/handler"
	"mallow/identity/internal/module/auth/repository"
	"mallow/identity/internal/module/auth/service"
	notificationservice "mallow/identity/internal/module/notification/service"
	userservice "mallow/identity/internal/module/user/service"
)

// ProvideTelegramHandler creates a TelegramHandler, injecting botUsername from config.
func ProvideTelegramHandler(
	cfg *config.Config,
	userSvc userservice.IUserService,
	rdb *redisgo.Client,
	js natsgo.JetStreamContext,
) *handler.TelegramHandler {
	return handler.NewTelegramHandler(userSvc, rdb, js, cfg.Telegram.BotUsername)
}

// ProvideJWTService creates a JWT service with configuration
func ProvideJWTService(cfg *config.Config) (service.IJWTService, error) {
	return service.NewJWTService(
		cfg.JWT.Secret,
		cfg.JWT.PrivateKey,
		cfg.JWT.PublicKey,
		cfg.JWT.KeyID,
		cfg.JWT.Issuer,
		cfg.JWT.AccessTTL,
		cfg.JWT.RefreshTTL,
	)
}

// ProvidePasswordService creates a password service
func ProvidePasswordService(
	userService userservice.IUserService,
	tokenRepo repository.TokenRepository,
	tokenService service.ITokenService,
	emailService notificationservice.EmailService,
	logger *slog.Logger,
) service.IPasswordService {
	return service.NewPasswordService(userService, tokenRepo, tokenService, emailService, logger)
}

// ProvideTokenService creates a token service
func ProvideTokenService() service.ITokenService {
	return service.NewTokenService()
}

// Module provides auth module dependencies
var Module = fx.Module("auth",
	fx.Provide(
		// Core services
		ProvideJWTService,
		ProvidePasswordService,
		ProvideTokenService,
		service.NewGoogleOAuthService,

		// Repositories
		repository.NewTokenRepository,
		repository.NewTokenBlacklistRepository,
		repository.NewSessionRepository,

		// Verification Service - provide as interface
		fx.Annotate(
			service.NewVerificationService,
			fx.As(new(service.IVerificationService)),
		),

		// Auth Service - provide as interface
		fx.Annotate(
			service.NewService,
			fx.As(new(service.IAuthService)),
		),

		// Session Service - provide as interface
		fx.Annotate(
			service.NewSessionService,
			fx.As(new(service.ISessionService)),
		),

		// Handlers
		handler.NewHandler,
		ProvideTelegramHandler,
		handler.NewInternalHandler,
	),
)
