package service

import (
	"context"

	"github.com/google/uuid"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	brokerservice "mallow/helm/internal/module/broker/service"
	"mallow/helm/internal/module/helm/domain"
	helmDto "mallow/helm/internal/module/helm/dto"
)

// BrokerAdapter adapts *Service to brokerservice.HelmCreator so the broker module
// can trigger helm lifecycle events (create/delete/pause/resume/error) without
// importing the helm service package directly.
type BrokerAdapter struct {
	Svc *Service
}

var _ brokerservice.HelmCreator = (*BrokerAdapter)(nil)

func (a *BrokerAdapter) AutoCreateForAccount(ctx context.Context, req brokerservice.HelmAutoCreateReq) error {
	_, err := a.Svc.CreateForAccount(helmDto.CreateForAccountReq{
		UserID:    req.UserID,
		AccountID: req.AccountID,
		Name:      req.AccountName,
		Exchange: domain.ExchangeConfig{
			BrokerType:  req.BrokerType,
			AccountType: domain.AccountType(req.AccountType),
			Paper:       req.Paper,
		},
		Creds: natsapi.CredentialsFetchResp{
			APIKey:      req.APIKey,
			APISecret:   req.APISecret,
			Passphrase:  req.Passphrase,
			BrokerType:  req.BrokerType,
			AccountType: req.AccountType,
			Paper:       req.Paper,
		},
	})
	return err
}

func (a *BrokerAdapter) AutoDeleteForAccount(_ context.Context, accountID uuid.UUID) error {
	return a.Svc.DeleteForAccount(accountID)
}

func (a *BrokerAdapter) RotateCredsForAccount(_ context.Context, accountID uuid.UUID, req brokerservice.HelmAutoCreateReq) error {
	return a.Svc.RotateCredsForAccount(accountID, exchange.Credentials{
		APIKey:      req.APIKey,
		APISecret:   req.APISecret,
		Passphrase:  req.Passphrase,
		AccountType: exchange.AccountType(req.AccountType),
	})
}

func (a *BrokerAdapter) PauseForAccount(_ context.Context, accountID uuid.UUID) error {
	h, err := a.Svc.GetByAccount(accountID)
	if err != nil {
		return err
	}
	return a.Svc.Pause(h.ID)
}

func (a *BrokerAdapter) MarkErrorForAccount(_ context.Context, accountID uuid.UUID) error {
	h, err := a.Svc.GetByAccount(accountID)
	if err != nil {
		return err
	}
	return a.Svc.MarkError(h.ID)
}

func (a *BrokerAdapter) ResumeErrorForAccount(_ context.Context, accountID uuid.UUID) error {
	h, err := a.Svc.GetByAccount(accountID)
	if err != nil {
		return err
	}
	if h.Status != domain.HelmStatusError {
		return nil
	}
	return a.Svc.Resume(h.ID)
}
