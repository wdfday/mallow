package dto

// CreateAccountRequest represents data for creating a new broker account.
type CreateAccountRequest struct {
	AccountName       string  `json:"accountName" binding:"required,min=1,max=255"`
	AccountType       string  `json:"accountType" binding:"required,oneof=spot futures_usdm futures_coinm unified options"`
	InstitutionName   *string `json:"institutionName,omitempty" binding:"omitempty,max=255"`
	Currency          *string `json:"currency,omitempty" binding:"omitempty,len=3"`
	IsActive          *bool   `json:"isActive,omitempty"`
	IncludeInNetWorth *bool   `json:"includeInNetWorth,omitempty"`
}

// UpdateAccountRequest represents data for updating an account.
type UpdateAccountRequest struct {
	AccountName       *string `json:"accountName,omitempty" binding:"omitempty,min=1,max=255"`
	AccountType       *string `json:"accountType,omitempty" binding:"omitempty,oneof=spot futures_usdm futures_coinm unified options"`
	InstitutionName   *string `json:"institutionName,omitempty" binding:"omitempty,max=255"`
	Currency          *string `json:"currency,omitempty" binding:"omitempty,len=3"`
	IsActive          *bool   `json:"isActive,omitempty"`
	IncludeInNetWorth *bool   `json:"includeInNetWorth,omitempty"`
}

// ListAccountsRequest represents query parameters for listing accounts.
type ListAccountsRequest struct {
	AccountType    *string `form:"account_type" binding:"omitempty,oneof=spot futures_usdm futures_coinm unified options"`
	IsActive       *bool   `form:"is_active"`
	IncludeDeleted *bool   `form:"include_deleted"`
}
