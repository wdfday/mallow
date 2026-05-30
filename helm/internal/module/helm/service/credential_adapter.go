package service

import (
	"context"

	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/module/helm/domain"
)

// brokerCredentialSource is the subset of broker/service.BrokerConnectionService
// that the helm service needs to spawn runtimes on Enable.
type brokerCredentialSource interface {
	GetCredentialsByAccountID(ctx context.Context, accountID string) (natsapi.CredentialsFetchResp, error)
}

// BrokerCredentialAdapter wraps a BrokerConnectionService and satisfies the
// CredentialFetcher port used by Enable to re-spawn torn-down runtimes.
type BrokerCredentialAdapter struct {
	BrokerSvc brokerCredentialSource
}

// GetCredentialsByAccountID fetches and decrypts credentials for the given account
// and converts them to a domain.ExchangeConfig ready for Registry.Spawn.
func (a BrokerCredentialAdapter) GetCredentialsByAccountID(ctx context.Context, accountID string) (domain.ExchangeConfig, error) {
	resp, err := a.BrokerSvc.GetCredentialsByAccountID(ctx, accountID)
	if err != nil {
		return domain.ExchangeConfig{}, err
	}
	return domain.ExchangeConfig{
		BrokerType:  resp.BrokerType,
		AccountType: domain.AccountType(resp.AccountType),
		AccountID:   resp.AccountRef,
		BaseURL:     resp.BaseURL,
		APIKey:      resp.APIKey,
		APISecret:   resp.APISecret,
		Passphrase:  resp.Passphrase,
		Paper:       resp.Paper,
	}, nil
}
