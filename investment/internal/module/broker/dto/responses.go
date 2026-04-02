package dto

import (
	"mallow/investment/internal/module/broker/domain"
	"time"

	"github.com/google/uuid"
)

// BrokerConnectionResponse represents a broker connection in API responses
type BrokerConnectionResponse struct {
	ID         uuid.UUID `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	UserID     uuid.UUID `json:"user_id" example:"550e8400-e29b-41d4-a716-446655440001"`
	BrokerType string    `json:"broker_type" example:"ssi"` // ssi, okx, sepay
	BrokerName string    `json:"broker_name" example:"SSI Securities"`
	Status     string    `json:"status" example:"active"` // active, disconnected, error, pending

	// Token info (NOT sensitive values, just metadata)
	TokenExpiresAt  *time.Time `json:"token_expires_at,omitempty"`
	LastRefreshedAt *time.Time `json:"last_refreshed_at,omitempty"`
	IsTokenValid    bool       `json:"is_token_valid"`

	// Sync settings
	AutoSync         bool `json:"auto_sync" example:"true"`
	SyncFrequency    int  `json:"sync_frequency" example:"60"` // minutes
	SyncAssets       bool `json:"sync_assets" example:"true"`
	SyncTransactions bool `json:"sync_transactions" example:"true"`
	SyncPrices       bool `json:"sync_prices" example:"true"`
	SyncBalance      bool `json:"sync_balance" example:"true"`

	// Sync status
	LastSyncAt     *time.Time `json:"last_sync_at,omitempty"`
	LastSyncStatus *string    `json:"last_sync_status,omitempty"` // success, failed
	LastSyncError  *string    `json:"last_sync_error,omitempty"`

	// Sync statistics
	TotalSyncs      int `json:"total_syncs" example:"10"`
	SuccessfulSyncs int `json:"successful_syncs" example:"9"`
	FailedSyncs     int `json:"failed_syncs" example:"1"`

	// External account info
	ExternalAccountID     *string `json:"external_account_id,omitempty"`
	ExternalAccountNumber *string `json:"external_account_number,omitempty"`
	ExternalAccountName   *string `json:"external_account_name,omitempty"`

	// Metadata
	Notes     *string   `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// BrokerConnectionListResponse represents a list of broker connections
type BrokerConnectionListResponse struct {
	Connections []*BrokerConnectionResponse `json:"connections"`
	Total       int                         `json:"total"`
}

// SyncResultResponse represents the result of a sync operation
type SyncResultResponse struct {
	Success            bool           `json:"success"`
	SyncedAt           time.Time      `json:"synced_at"`
	AssetsCount        int            `json:"assets_count"`
	TransactionsCount  int            `json:"transactions_count"`
	UpdatedPricesCount int            `json:"updated_prices_count"`
	BalanceUpdated     bool           `json:"balance_updated"`
	Error              *string        `json:"error,omitempty"`
	Details            map[string]any `json:"details,omitempty"`
}

// BrokerProviderInfo represents information about a broker provider
type BrokerProviderInfo struct {
	BrokerType        string   `json:"broker_type" example:"ssi"`
	DisplayName       string   `json:"display_name" example:"SSI Securities"`
	Description       string   `json:"description" example:"Vietnam stock trading platform"`
	RequiredFields    []string `json:"required_fields" example:"consumer_id,consumer_secret,otp_code"`
	SupportedFeatures []string `json:"supported_features" example:"portfolio,transactions,market_prices"`
	Logo              string   `json:"logo,omitempty"`
}

// ListBrokerProvidersResponse represents available broker providers
type ListBrokerProvidersResponse struct {
	Providers []BrokerProviderInfo `json:"providers"`
}

// ============================================================================
// Conversion Functions (Domain → DTO only, no service types)
// ============================================================================

// ToBrokerConnectionResponse converts domain model to API response
func ToBrokerConnectionResponse(conn *domain.BrokerConnection) *BrokerConnectionResponse {
	return &BrokerConnectionResponse{
		ID:                    conn.ID,
		UserID:                conn.UserID,
		BrokerType:            string(conn.BrokerType),
		BrokerName:            conn.BrokerName,
		Status:                string(conn.Status),
		TokenExpiresAt:        conn.TokenExpiresAt,
		LastRefreshedAt:       conn.LastRefreshedAt,
		IsTokenValid:          conn.IsTokenValid(),
		AutoSync:              conn.AutoSync,
		SyncFrequency:         conn.SyncFrequency,
		SyncAssets:            conn.SyncAssets,
		SyncTransactions:      conn.SyncTransactions,
		SyncPrices:            conn.SyncPrices,
		SyncBalance:           conn.SyncBalance,
		LastSyncAt:            conn.LastSyncAt,
		LastSyncStatus:        conn.LastSyncStatus,
		LastSyncError:         conn.LastSyncError,
		TotalSyncs:            conn.TotalSyncs,
		SuccessfulSyncs:       conn.SuccessfulSyncs,
		FailedSyncs:           conn.FailedSyncs,
		ExternalAccountID:     conn.ExternalAccountID,
		ExternalAccountNumber: conn.ExternalAccountNumber,
		ExternalAccountName:   conn.ExternalAccountName,
		Notes:                 conn.Notes,
		CreatedAt:             conn.CreatedAt,
		UpdatedAt:             conn.UpdatedAt,
	}
}

// ToBrokerConnectionListResponse converts a list of domain models to API response
func ToBrokerConnectionListResponse(connections []*domain.BrokerConnection) *BrokerConnectionListResponse {
	responses := make([]*BrokerConnectionResponse, len(connections))
	for i, conn := range connections {
		responses[i] = ToBrokerConnectionResponse(conn)
	}

	return &BrokerConnectionListResponse{
		Connections: responses,
		Total:       len(connections),
	}
}

// GetBrokerProviders returns information about available broker providers
func GetBrokerProviders() *ListBrokerProvidersResponse {
	return &ListBrokerProvidersResponse{
		Providers: []BrokerProviderInfo{
			{
				BrokerType:  string(domain.BrokerTypeOKX),
				DisplayName: "OKX Exchange",
				Description: "Cryptocurrency trading platform",
				RequiredFields: []string{
					"api_key",
					"api_secret",
					"passphrase",
				},
				SupportedFeatures: []string{
					"portfolio",
					"positions",
					"transactions",
					"market_prices",
				},
			},
			{
				BrokerType:  string(domain.BrokerTypeBinance),
				DisplayName: "Binance",
				Description: "World's largest cryptocurrency exchange",
				RequiredFields: []string{
					"api_key",
					"api_secret",
				},
				SupportedFeatures: []string{
					"portfolio",
					"positions",
					"market_prices",
				},
			},
			{
				BrokerType:  string(domain.BrokerTypeAlpaca),
				DisplayName: "Alpaca",
				Description: "US equities and crypto trading platform",
				RequiredFields: []string{
					"api_key",
					"api_secret",
				},
				SupportedFeatures: []string{
					"portfolio",
					"positions",
					"transactions",
					"market_prices",
				},
			},
			{
				BrokerType:  string(domain.BrokerTypeBybit),
				DisplayName: "Bybit",
				Description: "Crypto derivatives and spot trading platform",
				RequiredFields: []string{
					"api_key",
					"api_secret",
				},
				SupportedFeatures: []string{
					"portfolio",
					"positions",
					"transactions",
					"market_prices",
				},
			},
		},
	}
}
