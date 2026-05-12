package projection

import (
	"context"
	"encoding/json"

	"mallow/investment/internal/module/portfolio/event"
	txDomain "mallow/investment/internal/module/transaction/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TransactionProjector inserts a row into portfolio_transactions for every TransactionRecorded event.
type TransactionProjector struct {
	db *gorm.DB
}

func NewTransactionProjector(db *gorm.DB) Projector {
	return &TransactionProjector{db: db}
}

func (p *TransactionProjector) Project(ctx context.Context, ev event.InvestmentEvent) error {
	if ev.EventType != event.EventTypeTransactionRecorded {
		return nil
	}

	var tx event.TransactionRecorded
	if err := json.Unmarshal(ev.Payload, &tx); err != nil {
		return err
	}

	row := &txDomain.PortfolioTransaction{
		AccountID:     ev.AggregateID,
		UserID:        tx.UserID,
		Symbol:        tx.Symbol,
		TxType:        string(tx.Type),
		Currency:      tx.Currency,
		Quantity:      tx.Quantity,
		Price:         tx.PricePerUnit,
		Amount:        tx.Amount,
		Fees:          tx.Fees,
		Commission:    tx.Commission,
		Tax:           tx.Tax,
		RealizedPnL:   tx.RealizedGain,
		ExternalID:    tx.ExternalID,
		Source:        tx.Source,
		BotID:         tx.BotID,
		Notes:         tx.Notes,
		SourceEventID: ev.ID,
		TxDate:        tx.TransactionDate,
	}

	// Idempotent: ON CONFLICT source_event_id DO NOTHING prevents duplicates
	// on replay or crash-recovery.
	return p.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "source_event_id"}},
			DoNothing: true,
		}).
		Create(row).Error
}
