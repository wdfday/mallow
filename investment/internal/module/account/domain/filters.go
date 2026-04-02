package domain

// ListAccountsFilter represents filters for listing accounts.
type ListAccountsFilter struct {
	AccountType    *AccountType
	IsActive       *bool
	IsPrimary      *bool
	IncludeDeleted bool
}
