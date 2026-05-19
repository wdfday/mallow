package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"mallow/helm/internal/module/account/domain"
	accountdto "mallow/helm/internal/module/account/dto"
	"mallow/helm/internal/shared"
)

// CreateAccount creates a new broker sub-account for a user.
func (s *accountService) CreateAccount(ctx context.Context, userID string, req accountdto.CreateAccountRequest) (*domain.Account, error) {
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, shared.ErrBadRequest.WithDetails("field", "user_id").WithDetails("reason", "invalid UUID format")
	}

	accountType, err := parseAccountType(req.AccountType)
	if err != nil {
		return nil, err
	}

	account := &domain.Account{
		UserID:            userUUID,
		AccountName:       strings.TrimSpace(req.AccountName),
		AccountType:       accountType,
		Currency:          domain.CurrencyUSD,
		IsActive:          true,
		IncludeInNetWorth: true,
	}

	if req.InstitutionName != nil {
		account.InstitutionName = normalizeString(*req.InstitutionName)
	}
	if req.Currency != nil {
		account.Currency = domain.Currency(strings.ToUpper(strings.TrimSpace(*req.Currency)))
	}
	if req.IsActive != nil {
		account.IsActive = *req.IsActive
	}
	if req.IncludeInNetWorth != nil {
		account.IncludeInNetWorth = *req.IncludeInNetWorth
	}

	account.CreatedAt = time.Now().UTC()
	account.UpdatedAt = account.CreatedAt

	if err := s.repo.Create(ctx, account); err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	return s.GetByID(ctx, account.ID.String(), userID)
}
