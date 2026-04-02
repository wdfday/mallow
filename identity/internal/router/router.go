package router

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	_ "mallow/identity/docs"
	"mallow/identity/internal/config"
	"mallow/identity/internal/middleware"
	authHandler "mallow/identity/internal/module/auth/handler"
	authService "mallow/identity/internal/module/auth/service"
	profileHandler "mallow/identity/internal/module/profile/handler"
	userHandler "mallow/identity/internal/module/user/handler"
	userService "mallow/identity/internal/module/user/service"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.uber.org/fx"
	pkgtelemetry "mallow/pkg/telemetry"
)

// Module provides the router to fx.
var Module = fx.Module("router",
	fx.Provide(middleware.NewEmailVerificationMiddleware),
	fx.Invoke(Register),
)

// Params groups all handler dependencies for fx injection.
type Params struct {
	fx.In

	Engine           *gin.Engine
	Config           *config.Config
	JWTService       authService.IJWTService
	UserService      userService.IUserService
	EmailVerifMiddle *middleware.EmailVerificationMiddleware

	AuthHandler    *authHandler.Handler
	UserHandler    *userHandler.Handler
	ProfileHandler *profileHandler.Handler
}

// Register wires all middleware and routes onto the engine.
func Register(p Params) {
	r := p.Engine

	// Global middleware.
	r.Use(gin.Recovery())
	r.Use(pkgtelemetry.GinMiddleware("identity"))
	r.Use(middleware.NewCORS(p.Config.CORS.AllowedOrigins))
	r.Use(middleware.GlobalRateLimiter(200, 50))

	// Build auth middleware.
	authMiddle := middleware.NewMiddleware(p.JWTService, p.UserService)

	// Health check.
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	r.GET("/api/v1/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	registerWellKnownRoutes(r, p.Config)

	// Feature routes.
	p.AuthHandler.RegisterRoutes(r, authMiddle, p.EmailVerifMiddle, p.Config.ServiceSecret)
	p.UserHandler.RegisterRoutes(r, authMiddle)
	p.ProfileHandler.RegisterRoutes(r, authMiddle)
}

func registerWellKnownRoutes(r *gin.Engine, cfg *config.Config) {
	issuer := strings.TrimSuffix(cfg.JWT.Issuer, "/")
	if issuer == "" {
		issuer = "http://localhost:" + cfg.Server.Port
	}

	r.GET("/.well-known/openid-configuration", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"issuer":   issuer,
			"jwks_uri": issuer + "/.well-known/jwks.json",
		})
	})

	r.GET("/.well-known/jwks.json", func(c *gin.Context) {
		pubKey, ok := parseEd25519PublicKey(cfg.JWT.PublicKey)
		if !ok {
			c.JSON(http.StatusOK, gin.H{"keys": []any{}})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"keys": []gin.H{
				{
					"kty": "OKP",
					"crv": "Ed25519",
					"alg": "EdDSA",
					"use": "sig",
					"kid": cfg.JWT.KeyID,
					"x":   base64.RawURLEncoding.EncodeToString(pubKey),
				},
			},
		})
	})
}

func parseEd25519PublicKey(raw string) (ed25519.PublicKey, bool) {
	raw = strings.ReplaceAll(raw, `\n`, "\n")
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, false
	}
	parsedKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, false
	}
	pubKey, ok := parsedKey.(ed25519.PublicKey)
	if !ok {
		return nil, false
	}
	return pubKey, true
}
