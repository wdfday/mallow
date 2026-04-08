package dto

import (
	"time"

	"github.com/shopspring/decimal"
	"mallow/investment/internal/module/account/domain"
)

// AccountResponse represents an account in API responses.
type AccountResponse struct {
	ID                  string     `json:"id"`
	UserID              string     `json:"userId"`
	AccountName         string     `json:"accountName"`
	AccountType         string     `json:"accountType"`
	InstitutionName     *string    `json:"institutionName,omitempty"`
	CurrentBalance      decimal.Decimal  `json:"currentBalance"`
	AvailableBalance    *decimal.Decimal `json:"availableBalance,omitempty"`
	Currency            string     `json:"currency"`
	AccountNumberMasked *string    `json:"accountNumberMasked,omitempty"`
	IsActive            bool       `json:"isActive"`
	IsPrimary           bool       `json:"isPrimary"`
	IncludeInNetWorth   bool       `json:"includeInNetWorth"`
	LastSyncedAt        *time.Time `json:"lastSyncedAt,omitempty"`
	SyncStatus          *string    `json:"syncStatus,omitempty"`
	SyncErrorMessage    *string    `json:"syncErrorMessage,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

// ToResponse converts a domain account to DTO response.
func ToResponse(account domain.Account) AccountResponse {
	var syncStatus *string
	if account.SyncStatus != nil {
		status := string(*account.SyncStatus)
		syncStatus = &status
	}

	return AccountResponse{
		ID:                  account.ID.String(),
		UserID:              account.UserID.String(),
		AccountName:         account.AccountName,
		AccountType:         string(account.AccountType),
		InstitutionName:     account.InstitutionName,
		CurrentBalance:      account.CurrentBalance,
		AvailableBalance:    account.AvailableBalance,
		Currency:            string(account.Currency),
		AccountNumberMasked: account.AccountNumberMasked,
		IsActive:            account.IsActive,
		IsPrimary:           account.IsPrimary,
		IncludeInNetWorth:   account.IncludeInNetWorth,
		LastSyncedAt:        account.LastSyncedAt,
		SyncStatus:          syncStatus,
		SyncErrorMessage:    account.SyncErrorMessage,
		CreatedAt:           account.CreatedAt,
		UpdatedAt:           account.UpdatedAt,
	}
}

// AccountsListResponse represents a list of accounts.
type AccountsListResponse struct {
	Items []AccountResponse `json:"items"`
	Total int64             `json:"total"`
}
