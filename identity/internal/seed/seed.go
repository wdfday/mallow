package seed

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/fx"

	"mallow/identity/internal/config"
	"mallow/identity/internal/module/auth/service"
	userDomain "mallow/identity/internal/module/user/domain"
	userService "mallow/identity/internal/module/user/service"
)

// Params groups dependencies for admin seed (used by fx.Invoke).
type Params struct {
	fx.In
	Config          *config.Config
	UserService     userService.IUserService
	PasswordService service.IPasswordService
	Logger          *slog.Logger
}

// SeedAdmin creates the initial admin user when config.AdminSeed has email+password
// and no admin user exists yet. Safe to run on every startup (idempotent).
func SeedAdmin(p Params) {
	cfg := p.Config
	if cfg.AdminSeed.Email == "" || cfg.AdminSeed.Password == "" {
		return
	}

	ctx := context.Background()
	email := strings.ToLower(strings.TrimSpace(cfg.AdminSeed.Email))

	exists, err := p.UserService.ExistsByEmail(ctx, email)
	if err != nil {
		p.Logger.Error("admin seed: check existing admin failed", "err", err)
		return
	}
	if exists {
		p.Logger.Debug("admin seed: admin already exists", "email", email)
		return
	}

	// Optional: check that no admin exists at all (e.g. first deploy). Here we only key by seed email.
	hashed, err := p.PasswordService.HashPassword(cfg.AdminSeed.Password)
	if err != nil {
		p.Logger.Error("admin seed: hash password failed", "err", err)
		return
	}

	now := time.Now()
	user := &userDomain.User{
		ID:                uuid.New(),
		Email:             email,
		Password:          hashed,
		FullName:          "Admin",
		Role:              userDomain.UserRoleAdmin,
		Status:            userDomain.UserStatusActive,
		EmailVerified:     true,
		EmailVerifiedAt:   &now,
		TermsAccepted:     true,
		TermsAcceptedAt:   &now,
		PasswordChangedAt: now,
	}

	if _, err := p.UserService.Create(ctx, user); err != nil {
		p.Logger.Error("admin seed: create admin failed", "email", email, "err", err)
		return
	}

	p.Logger.Info("admin seed: initial admin user created", "email", email, "user_id", user.ID.String())
}

// Module runs admin seed at startup when ADMIN_SEED_EMAIL and ADMIN_SEED_PASSWORD are set.
var Module = fx.Module("seed", fx.Invoke(SeedAdmin))
