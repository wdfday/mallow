package service

import (
	"context"
	"strings"

	"mallow/helm/internal/module/account/domain"
	accountdto "mallow/helm/internal/module/account/dto"
	"mallow/helm/internal/shared"
)

// UpdateAccount updates an existing account.
func (s *accountService) UpdateAccount(ctx context.Context, id, userID string, req accountdto.UpdateAccountRequest) (*domain.Account, error) {
	existing, err := s.repo.GetByIDAndUserID(ctx, id, userID)
	if err != nil {
		if err == shared.ErrNotFound {
			return nil, err
		}
		return nil, shared.ErrInternal.WithError(err)
	}

	updates := make(map[string]any)

	if req.AccountName != nil {
		updates["account_name"] = strings.TrimSpace(*req.AccountName)
	}
	if req.AccountType != nil {
		accountType, err := parseAccountType(*req.AccountType)
		if err != nil {
			return nil, err
		}
		updates["account_type"] = string(accountType)
	}
	if req.InstitutionName != nil {
		updates["institution_name"] = normalizeNullableString(req.InstitutionName)
	}
	if req.Currency != nil {
		updates["currency"] = strings.ToUpper(strings.TrimSpace(*req.Currency))
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}
	if req.IncludeInNetWorth != nil {
		updates["include_in_net_worth"] = *req.IncludeInNetWorth
	}

	if len(updates) == 0 {
		return existing, nil
	}

	if err := s.repo.UpdateColumns(ctx, id, updates); err != nil {
		if err == shared.ErrNotFound {
			return nil, err
		}
		return nil, shared.ErrInternal.WithError(err)
	}

	return s.GetByID(ctx, id, userID)
}
