package dto

import (
	"mallow/helm/internal/module/broker/domain"
	"time"

	"github.com/google/uuid"
)

// BrokerConnectionResponse represents a broker connection in API responses.
type BrokerConnectionResponse struct {
	ID         uuid.UUID `json:"id"`
	UserID     uuid.UUID `json:"user_id"`
	BrokerType string    `json:"broker_type"`
	BrokerName string    `json:"broker_name"`
	Status     string    `json:"status"` // active, disconnected, error, pending
	IsPaper    bool      `json:"is_paper"`

	// External account info
	ExternalAccountID     *string `json:"external_account_id,omitempty"`
	ExternalAccountNumber *string `json:"external_account_number,omitempty"`
	ExternalAccountName   *string `json:"external_account_name,omitempty"`

	// Metadata
	Notes     *string   `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// BrokerConnectionListResponse represents a list of broker connections.
type BrokerConnectionListResponse struct {
	Connections []*BrokerConnectionResponse `json:"connections"`
	Total       int                         `json:"total"`
}

// BrokerProviderInfo represents information about a broker provider.
type BrokerProviderInfo struct {
	BrokerType        string   `json:"broker_type"`
	DisplayName       string   `json:"display_name"`
	Description       string   `json:"description"`
	RequiredFields    []string `json:"required_fields"`
	SupportedFeatures []string `json:"supported_features"`
}

// ListBrokerProvidersResponse represents available broker providers.
type ListBrokerProvidersResponse struct {
	Providers []BrokerProviderInfo `json:"providers"`
}

// ============================================================================
// Conversion Functions (Domain → DTO only)
// ============================================================================

// ToBrokerConnectionResponse converts domain model to API response.
func ToBrokerConnectionResponse(conn *domain.BrokerConnection) *BrokerConnectionResponse {
	return &BrokerConnectionResponse{
		ID:                    conn.ID,
		UserID:                conn.UserID,
		BrokerType:            string(conn.BrokerType),
		BrokerName:            conn.BrokerName,
		Status:                string(conn.Status),
		IsPaper:               conn.IsPaper,
		ExternalAccountID:     conn.ExternalAccountID,
		ExternalAccountNumber: conn.ExternalAccountNumber,
		ExternalAccountName:   conn.ExternalAccountName,
		Notes:                 conn.Notes,
		CreatedAt:             conn.CreatedAt,
		UpdatedAt:             conn.UpdatedAt,
	}
}

// ToBrokerConnectionListResponse converts a list of domain models to API response.
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

// GetBrokerProviders returns information about available broker providers.
func GetBrokerProviders() *ListBrokerProvidersResponse {
	return &ListBrokerProvidersResponse{
		Providers: []BrokerProviderInfo{
			{
				BrokerType:        string(domain.BrokerTypeOKX),
				DisplayName:       "OKX Exchange",
				Description:       "Cryptocurrency trading platform",
				RequiredFields:    []string{"api_key", "api_secret", "passphrase"},
				SupportedFeatures: []string{"portfolio", "positions"},
			},
			{
				BrokerType:        string(domain.BrokerTypeBinance),
				DisplayName:       "Binance",
				Description:       "World's largest cryptocurrency exchange",
				RequiredFields:    []string{"api_key", "api_secret"},
				SupportedFeatures: []string{"portfolio", "positions"},
			},
			{
				BrokerType:        string(domain.BrokerTypeAlpaca),
				DisplayName:       "Alpaca",
				Description:       "US equities and crypto trading platform",
				RequiredFields:    []string{"api_key", "api_secret"},
				SupportedFeatures: []string{"portfolio", "positions"},
			},
			{
				BrokerType:        string(domain.BrokerTypeBybit),
				DisplayName:       "Bybit",
				Description:       "Crypto derivatives and spot trading platform",
				RequiredFields:    []string{"api_key", "api_secret"},
				SupportedFeatures: []string{"portfolio", "positions"},
			},
		},
	}
}
