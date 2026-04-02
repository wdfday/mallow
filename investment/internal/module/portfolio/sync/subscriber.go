// Package sync subscribes to portfolio.synced.> events published by the
// orchestrator after each REST sync and updates the investment account's
// available_balance with the latest cash value.
package sync

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/fx"

	"mallow/investment/internal/module/account/service"
)

// portfolioSyncedPayload is the minimal subset of natsapi.PortfolioSyncEvent
// that this subscriber needs. Using a local type avoids importing the
// orchestrator module from within the investment service.
type portfolioSyncedPayload struct {
	AccountID string  `json:"account_id"`
	Cash      float64 `json:"cash"`
}

// Subscriber watches portfolio.synced.> and updates available_balance on the
// matching investment account whenever the orchestrator reports a new cash value.
type Subscriber struct {
	accountSvc service.Service
	nc         *nats.Conn
	logger     *slog.Logger
}

// New creates the subscriber and registers fx lifecycle hooks.
func New(
	lc fx.Lifecycle,
	nc *nats.Conn,
	accountSvc service.Service,
	logger *slog.Logger,
) *Subscriber {
	s := &Subscriber{
		accountSvc: accountSvc,
		nc:         nc,
		logger:     logger.With("component", "portfolio.sync"),
	}

	var sub *nats.Subscription
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			var err error
			sub, err = nc.Subscribe("portfolio.synced.>", s.onSynced)
			return err
		},
		OnStop: func(_ context.Context) error {
			if sub != nil {
				return sub.Unsubscribe()
			}
			return nil
		},
	})

	return s
}

func (s *Subscriber) onSynced(msg *nats.Msg) {
	var ev portfolioSyncedPayload
	if err := json.Unmarshal(msg.Data, &ev); err != nil {
		s.logger.Warn("portfolio.synced: unmarshal failed", "err", err)
		return
	}

	accountID, err := uuid.Parse(ev.AccountID)
	if err != nil {
		s.logger.Warn("portfolio.synced: invalid account_id", "raw", ev.AccountID, "err", err)
		return
	}

	if ev.Cash < 0 {
		s.logger.Warn("portfolio.synced: negative cash, skipping", "account_id", ev.AccountID, "cash", ev.Cash)
		return
	}

	ctx := context.Background()
	if err := s.accountSvc.UpdateAvailableBalance(ctx, accountID, ev.Cash); err != nil {
		s.logger.Warn("portfolio.synced: UpdateAvailableBalance failed",
			"account_id", ev.AccountID,
			"cash", ev.Cash,
			"err", err,
		)
		return
	}

	s.logger.Info("portfolio.synced: balance updated",
		"account_id", ev.AccountID,
		"cash", ev.Cash,
	)
}
