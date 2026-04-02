package projection

import (
	"context"
	"encoding/json"

	cashDomain "mallow/investment/internal/module/cash_flow/domain"
	"mallow/investment/internal/module/portfolio/event"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CashFlowProjector inserts rows into portfolio_cash_flows for dividend/deposit/withdrawal/fee events.
type CashFlowProjector struct {
	db *gorm.DB
}

func NewCashFlowProjector(db *gorm.DB) Projector {
	return &CashFlowProjector{db: db}
}

func (p *CashFlowProjector) Project(ctx context.Context, ev event.InvestmentEvent) error {
	if ev.EventType != event.EventTypeTransactionRecorded {
		return nil
	}

	var tx event.TransactionRecorded
	if err := json.Unmarshal(ev.Payload, &tx); err != nil {
		return err
	}

	var flowType string
	switch tx.Type {
	case event.TransactionTypeDividend:
		flowType = "dividend"
	case event.TransactionTypeDeposit:
		flowType = "deposit"
	case event.TransactionTypeWithdrawal:
		flowType = "withdrawal"
	case event.TransactionTypeFee:
		flowType = "fee"
	default:
		return nil // buy/sell/derivative don't create cash flow rows
	}

	row := &cashDomain.PortfolioCashFlow{
		AccountID:     ev.AggregateID,
		UserID:        tx.UserID,
		FlowType:      flowType,
		Amount:        tx.Amount,
		Currency:      tx.Currency,
		Symbol:        tx.Symbol,
		Description:   tx.Notes,
		SourceEventID: ev.ID,
		OccurredAt:    tx.TransactionDate,
	}

	return p.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "source_event_id"}},
			DoNothing: true,
		}).
		Create(row).Error
}
