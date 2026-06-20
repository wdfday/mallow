package service

import (
	"context"
	"errors"

	"mallow/helm/internal/shared"
)

// DeleteAccount soft deletes an account
func (s *accountService) DeleteAccount(ctx context.Context, id, userID string) error {
	if _, err := s.repo.GetByIDAndUserID(ctx, id, userID); err != nil {
		if errors.Is(err, shared.ErrNotFound) {
			return err
		}
		return shared.ErrInternal.WithError(err)
	}

	if err := s.repo.SoftDelete(ctx, id); err != nil {
		if errors.Is(err, shared.ErrNotFound) {
			return err
		}
		return shared.ErrInternal.WithError(err)
	}

	return nil
}
