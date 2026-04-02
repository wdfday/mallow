package dto

// CreateAccountRequest represents data for creating a new account.
type CreateAccountRequest struct {
	AccountName         string   `json:"accountName" binding:"required,min=1,max=255"`
	AccountType         string   `json:"accountType" binding:"required,oneof=cash bank savings credit_card investment crypto_wallet"`
	InstitutionName     *string  `json:"institutionName,omitempty" binding:"omitempty,max=255"`
	CurrentBalance      *float64 `json:"currentBalance,omitempty"`
	AvailableBalance    *float64 `json:"availableBalance,omitempty"`
	Currency            *string  `json:"currency,omitempty" binding:"omitempty,len=3"`
	AccountNumber       *string  `json:"accountNumber,omitempty" binding:"omitempty,max=50"`
	AccountNumberMasked *string  `json:"accountNumberMasked,omitempty" binding:"omitempty,max=50"`
	IsActive            *bool    `json:"isActive,omitempty"`
	IsPrimary           *bool    `json:"isPrimary,omitempty"`
	IncludeInNetWorth   *bool    `json:"includeInNetWorth,omitempty"`
	CreditLimit         *float64 `json:"creditLimit,omitempty"`
}

// UpdateAccountRequest represents data for updating an account.
type UpdateAccountRequest struct {
	AccountName         *string  `json:"accountName,omitempty" binding:"omitempty,min=1,max=255"`
	AccountType         *string  `json:"accountType,omitempty" binding:"omitempty,oneof=cash bank savings credit_card investment crypto_wallet"`
	InstitutionName     *string  `json:"institutionName,omitempty" binding:"omitempty,max=255"`
	CurrentBalance      *float64 `json:"currentBalance,omitempty"`
	AvailableBalance    *float64 `json:"availableBalance,omitempty"`
	Currency            *string  `json:"currency,omitempty" binding:"omitempty,len=3"`
	AccountNumber       *string  `json:"accountNumber,omitempty" binding:"omitempty,max=50"`
	AccountNumberMasked *string  `json:"accountNumberMasked,omitempty" binding:"omitempty,max=50"`
	IsActive            *bool    `json:"isActive,omitempty"`
	IsPrimary           *bool    `json:"isPrimary,omitempty"`
	IncludeInNetWorth   *bool    `json:"includeInNetWorth,omitempty"`
	SyncStatus          *string  `json:"syncStatus,omitempty" binding:"omitempty,oneof=active error disconnected"`
	SyncErrorMessage    *string  `json:"syncErrorMessage,omitempty"`
}

// ListAccountsRequest represents query parameters for listing accounts.
type ListAccountsRequest struct {
	AccountType    *string `form:"account_type" binding:"omitempty,oneof=cash bank savings credit_card investment crypto_wallet"`
	IsActive       *bool   `form:"is_active"`
	IsPrimary      *bool   `form:"is_primary"`
	IncludeDeleted *bool   `form:"include_deleted"`
}
