package service

import (
	"context"

	"mallow/helm/internal/module/account/domain"
	accountdto "mallow/helm/internal/module/account/dto"
	"mallow/helm/internal/shared"
)

// GetByID retrieves a single account by ID for a specific user
func (s *accountService) GetByID(ctx context.Context, id, userID string) (*domain.Account, error) {
	account, err := s.repo.GetByIDAndUserID(ctx, id, userID)
	if err != nil {
		if err == shared.ErrNotFound {
			return nil, err
		}
		return nil, shared.ErrInternal.WithError(err)
	}
	return account, nil
}

// GetByUserID retrieves all accounts for a user with optional filters
func (s *accountService) GetByUserID(ctx context.Context, userID string, req accountdto.ListAccountsRequest) ([]domain.Account, int64, error) {
	filters := domain.ListAccountsFilter{
		IncludeDeleted: req.IncludeDeleted != nil && *req.IncludeDeleted,
	}

	if req.AccountType != nil {
		accountType, err := parseAccountType(*req.AccountType)
		if err != nil {
			return nil, 0, err
		}
		filters.AccountType = &accountType
	}
	if req.IsActive != nil {
		filters.IsActive = req.IsActive
	}

	accounts, err := s.repo.ListByUserID(ctx, userID, filters)
	if err != nil {
		return nil, 0, shared.ErrInternal.WithError(err)
	}

	total, err := s.repo.CountByUserID(ctx, userID, filters)
	if err != nil {
		return nil, 0, shared.ErrInternal.WithError(err)
	}

	return accounts, total, nil
}
