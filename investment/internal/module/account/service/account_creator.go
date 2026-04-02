package service

import (
	"context"
	"strings"
	"time"

	"mallow/investment/internal/module/account/domain"
	accountdto "mallow/investment/internal/module/account/dto"
	"mallow/investment/internal/shared"

	"github.com/google/uuid"
)

// CreateAccount creates a new account for a user
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
		CurrentBalance:    0,
		Currency:          domain.CurrencyVND,
		IsActive:          true,
		IsPrimary:         false,
		IncludeInNetWorth: true,
	}

	// Apply optional fields
	if req.InstitutionName != nil {
		account.InstitutionName = normalizeString(*req.InstitutionName)
	}
	if req.CurrentBalance != nil {
		account.CurrentBalance = *req.CurrentBalance
	}
	if req.AvailableBalance != nil {
		account.AvailableBalance = req.AvailableBalance
	}
	if req.Currency != nil {
		account.Currency = domain.Currency(strings.ToUpper(strings.TrimSpace(*req.Currency)))
	}
	if req.AccountNumber != nil && strings.TrimSpace(*req.AccountNumber) != "" {
		enc, err := s.encrypt.Encrypt(strings.TrimSpace(*req.AccountNumber))
		if err != nil {
			return nil, shared.ErrInternal.WithError(err)
		}
		account.AccountNumberEncrypted = &enc
		// Auto-mask unless caller provided an explicit mask
		if req.AccountNumberMasked == nil {
			masked := maskAccountNumber(*req.AccountNumber)
			account.AccountNumberMasked = &masked
		}
	}
	if req.AccountNumberMasked != nil {
		account.AccountNumberMasked = normalizeString(*req.AccountNumberMasked)
	}
	if req.IsActive != nil {
		account.IsActive = *req.IsActive
	}
	if req.IsPrimary != nil && *req.IsPrimary {
		if err := s.unsetPrimaryAccount(ctx, userID); err != nil {
			return nil, err
		}
		account.IsPrimary = true
	}
	if req.IncludeInNetWorth != nil {
		account.IncludeInNetWorth = *req.IncludeInNetWorth
	}
	if req.CreditLimit != nil {
		account.CreditLimit = req.CreditLimit
	}

	account.CreatedAt = time.Now().UTC()
	account.UpdatedAt = account.CreatedAt

	if err := s.repo.Create(ctx, account); err != nil {
		return nil, shared.ErrInternal.WithError(err)
	}

	return s.GetByID(ctx, account.ID.String(), userID)
}

func (s *accountService) CreateDefaultCashAccount(ctx context.Context, userID string) error {
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return shared.ErrBadRequest.WithDetails("field", "user_id").WithDetails("reason", "invalid UUID format")
	}

	account := &domain.Account{
		UserID:            userUUID,
		AccountName:       "Cash",
		AccountType:       domain.AccountTypeCash,
		CurrentBalance:    0,
		Currency:          domain.CurrencyVND,
		IsActive:          true,
		IsPrimary:         true,
		IncludeInNetWorth: true,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}

	if err := s.repo.Create(ctx, account); err != nil {
		return shared.ErrInternal.WithError(err)
	}
	return nil
}
