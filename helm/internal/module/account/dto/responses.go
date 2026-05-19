package dto

import (
	"time"

	"mallow/helm/internal/module/account/domain"
)

// AccountResponse represents an account in API responses.
type AccountResponse struct {
	ID                 string     `json:"id"`
	UserID             string     `json:"user_id"`
	AccountName        string     `json:"account_name"`
	AccountType        string     `json:"account_type"`
	InstitutionName    *string    `json:"institution_name,omitempty"`
	Currency           string     `json:"currency"`
	IsActive           bool       `json:"is_active"`
	IncludeInNetWorth  bool       `json:"include_in_net_worth"`
	IsAutoSync         bool       `json:"is_auto_sync"`
	LastSyncedAt       *time.Time `json:"last_synced_at,omitempty"`
	SyncStatus         *string    `json:"sync_status,omitempty"`
	SyncErrorMessage   *string    `json:"sync_error_message,omitempty"`
	BrokerConnectionID *string    `json:"broker_connection_id,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// ToResponse converts a domain account to DTO response.
func ToResponse(account domain.Account) AccountResponse {
	var syncStatus *string
	if account.SyncStatus != nil {
		status := string(*account.SyncStatus)
		syncStatus = &status
	}
	var brokerConnID *string
	if account.BrokerConnectionID != nil {
		s := account.BrokerConnectionID.String()
		brokerConnID = &s
	}

	return AccountResponse{
		ID:                 account.ID.String(),
		UserID:             account.UserID.String(),
		AccountName:        account.AccountName,
		AccountType:        string(account.AccountType),
		InstitutionName:    account.InstitutionName,
		Currency:           string(account.Currency),
		IsActive:           account.IsActive,
		IncludeInNetWorth:  account.IncludeInNetWorth,
		IsAutoSync:         account.IsAutoSync,
		LastSyncedAt:       account.LastSyncedAt,
		SyncStatus:         syncStatus,
		SyncErrorMessage:   account.SyncErrorMessage,
		BrokerConnectionID: brokerConnID,
		CreatedAt:          account.CreatedAt,
		UpdatedAt:          account.UpdatedAt,
	}
}

// AccountsListResponse represents a list of accounts.
type AccountsListResponse struct {
	Items []AccountResponse `json:"items"`
	Total int64             `json:"total"`
}
