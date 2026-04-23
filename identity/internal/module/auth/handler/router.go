package handler

import (
	"mallow/identity/internal/config"
	"mallow/identity/internal/middleware"
	"mallow/identity/internal/module/auth/service"

	"github.com/gin-gonic/gin"
)

// Handler is the facade that wires individual auth sub-handlers into the router.
type Handler struct {
	auth     *AuthHandler
	password *PasswordHandler
	verify   *VerifyHandler
	telegram *TelegramHandler
	session  *SessionHandler
}

// NewHandler builds the facade handler with its specialized sub-handlers.
func NewHandler(
	authService service.IAuthService,
	sessionService service.ISessionService,
	passwordService service.IPasswordService,
	verificationService service.IVerificationService,
	telegramHandler *TelegramHandler,
	cfg *config.Config,
) *Handler {
	return &Handler{
		auth:     NewAuthHandler(authService, sessionService, cfg),
		password: NewPasswordHandler(passwordService),
		verify:   NewVerifyHandler(verificationService),
		telegram: telegramHandler,
		session:  NewSessionHandler(sessionService),
	}
}

// RegisterRoutes exposes every auth-related route group.
func (h *Handler) RegisterRoutes(
	r *gin.Engine,
	authMiddleware *middleware.Middleware,
	emailVerificationMiddleware *middleware.EmailVerificationMiddleware,
	serviceSecret string,
) {
	h.auth.RegisterRoutes(r, authMiddleware)
	h.password.RegisterRoutes(r, authMiddleware, emailVerificationMiddleware)
	h.verify.RegisterRoutes(r, authMiddleware)
	h.telegram.RegisterRoutes(r, authMiddleware, serviceSecret)
	h.session.RegisterRoutes(r, authMiddleware)
}
