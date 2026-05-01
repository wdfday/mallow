package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	authDomain "mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/module/auth/repository"
	"mallow/identity/internal/shared"
)

const sessionLifetime = 7 * 24 * time.Hour

// ISessionService manages user sessions linked to token pairs by SID.
type ISessionService interface {
	CreateSession(ctx context.Context, sid, userID, ip, userAgent string) error
	ListSessions(ctx context.Context, userID string) ([]authDomain.Session, error)
	RevokeSession(ctx context.Context, sid, userID string) error
	RevokeAllSessions(ctx context.Context, userID string) error
}

type sessionService struct {
	repo      repository.ISessionRepository
	blacklist repository.ITokenBlacklistRepository
}

// NewSessionService creates a session service backed by a session repository and blacklist.
func NewSessionService(repo repository.ISessionRepository, blacklist repository.ITokenBlacklistRepository) ISessionService {
	return &sessionService{repo: repo, blacklist: blacklist}
}

func (s *sessionService) CreateSession(ctx context.Context, sid, userIDStr, ip, userAgent string) error {
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return shared.ErrBadRequest.WithDetails("message", "invalid user ID")
	}
	return s.repo.Create(ctx, &authDomain.Session{
		SID:       sid,
		UserID:    userID,
		IP:        ip,
		UserAgent: userAgent,
		ExpiresAt: time.Now().Add(sessionLifetime),
	})
}

func (s *sessionService) ListSessions(ctx context.Context, userIDStr string) ([]authDomain.Session, error) {
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return nil, shared.ErrBadRequest.WithDetails("message", "invalid user ID")
	}
	return s.repo.ListByUser(ctx, userID)
}

func (s *sessionService) RevokeSession(ctx context.Context, sid, userIDStr string) error {
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return shared.ErrBadRequest.WithDetails("message", "invalid user ID")
	}
	session, err := s.repo.GetBySID(ctx, sid, userID)
	if err != nil {
		return shared.ErrInternal.WithError(err)
	}
	if session == nil {
		return shared.ErrNotFound.WithDetails("message", "session not found")
	}
	if session.RevokedAt != nil {
		return shared.ErrBadRequest.WithDetails("message", "session already revoked")
	}
	if err := s.repo.MarkRevoked(ctx, sid, userID); err != nil {
		return shared.ErrInternal.WithError(err)
	}
	return s.blacklist.RevokeSession(ctx, sid, session.ExpiresAt)
}

func (s *sessionService) RevokeAllSessions(ctx context.Context, userIDStr string) error {
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return shared.ErrBadRequest.WithDetails("message", "invalid user ID")
	}
	if err := s.repo.RevokeAllByUser(ctx, userID); err != nil {
		return shared.ErrInternal.WithError(err)
	}
	return s.blacklist.BlacklistAllUserTokens(ctx, userID, "revoke_all_sessions")
}
