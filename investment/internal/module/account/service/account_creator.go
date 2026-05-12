package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"mallow/investment/internal/module/account/domain"
	accountdto "mallow/investment/internal/module/account/dto"
	"mallow/investment/internal/shared"
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
		CurrentBalance:    decimal.Zero,
		Currency:          domain.CurrencyUSD,
		IsActive:          true,
		IncludeInNetWorth: true,
	}

	if req.InstitutionName != nil {
		account.InstitutionName = normalizeString(*req.InstitutionName)
	}
	if req.CurrentBalance != nil {
		account.CurrentBalance = decimal.NewFromFloat(*req.CurrentBalance)
	}
	if req.AvailableBalance != nil {
		d := decimal.NewFromFloat(*req.AvailableBalance)
		account.AvailableBalance = &d
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
