package dto

import (
	"mallow/helm/internal/module/broker/domain"

	"github.com/google/uuid"
)

// BaseBrokerConnection contains common fields for all broker connections.
type BaseBrokerConnection struct {
	BrokerName  string  `json:"broker_name" binding:"required"`
	AccountType string  `json:"account_type,omitempty"` // spot | futures_usdm | futures_coinm | unified
	Notes       *string `json:"notes,omitempty"`
}

func (b *BaseBrokerConnection) toServiceRequest(userID uuid.UUID, brokerType domain.BrokerType) *CreateBrokerConnectionServiceRequest {
	return &CreateBrokerConnectionServiceRequest{
		UserID:      userID,
		BrokerType:  brokerType,
		BrokerName:  b.BrokerName,
		AccountType: b.AccountType,
		Notes:       b.Notes,
	}
}

// ============================================================================
// OKX
// ============================================================================

type CreateOKXConnectionRequest struct {
	BaseBrokerConnection
	APIKey     string `json:"api_key" binding:"required"`
	APISecret  string `json:"api_secret" binding:"required"`
	Passphrase string `json:"passphrase" binding:"required"`
	IsPaper    bool   `json:"is_paper"` // simulated trading
}

func (r *CreateOKXConnectionRequest) ToServiceRequest(userID uuid.UUID) *CreateBrokerConnectionServiceRequest {
	req := r.toServiceRequest(userID, domain.BrokerTypeOKX)
	req.APIKey = r.APIKey
	req.APISecret = r.APISecret
	req.Passphrase = &r.Passphrase
	req.IsPaper = r.IsPaper
	return req
}

// ============================================================================
// Binance
// ============================================================================

type CreateBinanceConnectionRequest struct {
	BaseBrokerConnection
	APIKey    string `json:"api_key" binding:"required"`
	APISecret string `json:"api_secret" binding:"required"`
	IsPaper   bool   `json:"is_paper"` // demo trading
}

func (r *CreateBinanceConnectionRequest) ToServiceRequest(userID uuid.UUID) *CreateBrokerConnectionServiceRequest {
	req := r.toServiceRequest(userID, domain.BrokerTypeBinance)
	req.APIKey = r.APIKey
	req.APISecret = r.APISecret
	req.IsPaper = r.IsPaper
	return req
}

// ============================================================================
// Alpaca
// ============================================================================

type CreateAlpacaConnectionRequest struct {
	BaseBrokerConnection
	APIKey    string `json:"api_key" binding:"required"`
	APISecret string `json:"api_secret" binding:"required"`
	IsPaper   bool   `json:"is_paper"` // paper trading
}

func (r *CreateAlpacaConnectionRequest) ToServiceRequest(userID uuid.UUID) *CreateBrokerConnectionServiceRequest {
	req := r.toServiceRequest(userID, domain.BrokerTypeAlpaca)
	req.APIKey = r.APIKey
	req.APISecret = r.APISecret
	req.IsPaper = r.IsPaper
	return req
}

// ============================================================================
// Bybit
// ============================================================================

type CreateBybitConnectionRequest struct {
	BaseBrokerConnection
	APIKey    string `json:"api_key" binding:"required"`
	APISecret string `json:"api_secret" binding:"required"`
	IsPaper   bool   `json:"is_paper"`
}

func (r *CreateBybitConnectionRequest) ToServiceRequest(userID uuid.UUID) *CreateBrokerConnectionServiceRequest {
	req := r.toServiceRequest(userID, domain.BrokerTypeBybit)
	req.APIKey = r.APIKey
	req.APISecret = r.APISecret
	req.IsPaper = r.IsPaper
	return req
}

// ============================================================================
// Update / List
// ============================================================================

type UpdateBrokerConnectionRequest struct {
	BrokerName *string `json:"broker_name,omitempty"`
	APIKey     *string `json:"api_key,omitempty"`
	APISecret  *string `json:"api_secret,omitempty"`
	Passphrase *string `json:"passphrase,omitempty"`
	Notes      *string `json:"notes,omitempty"`
}

// ReBrokerRequest is the body for POST /broker-connections/{id}/rebroker.
type ReBrokerRequest struct {
	AccountID uuid.UUID `json:"account_id" binding:"required"`
}

type ListBrokerConnectionsQuery struct {
	BrokerType *string `form:"broker_type"`
	Status     *string `form:"status"`
	ActiveOnly bool    `form:"active_only"`
}

// ============================================================================
// Internal service-layer request
// ============================================================================

type CreateBrokerConnectionServiceRequest struct {
	UserID     uuid.UUID
	BrokerType domain.BrokerType
	BrokerName string

	APIKey      string
	APISecret   string
	IsPaper     bool
	Passphrase  *string
	AccountType string // optional override: spot | futures_usdm | futures_coinm | unified

	Notes *string
}
