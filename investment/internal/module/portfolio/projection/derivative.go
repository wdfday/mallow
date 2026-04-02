package projection

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	derivDomain "mallow/investment/internal/module/derivative/domain"
	"mallow/investment/internal/module/portfolio/event"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DerivativeProjector manages derivative_positions from derivative_open/close events.
type DerivativeProjector struct {
	db *gorm.DB
}

func NewDerivativeProjector(db *gorm.DB) Projector {
	return &DerivativeProjector{db: db}
}

func (p *DerivativeProjector) Project(ctx context.Context, ev event.InvestmentEvent) error {
	if ev.EventType != event.EventTypeTransactionRecorded {
		return nil
	}

	var tx event.TransactionRecorded
	if err := json.Unmarshal(ev.Payload, &tx); err != nil {
		return err
	}

	switch tx.Type {
	case event.TransactionTypeDerivativeOpen:
		return p.handleOpen(ctx, ev, tx)
	case event.TransactionTypeDerivativeClose:
		return p.handleClose(ctx, ev, tx)
	}
	return nil
}

func (p *DerivativeProjector) handleOpen(ctx context.Context, ev event.InvestmentEvent, tx event.TransactionRecorded) error {
	pos := &derivDomain.DerivativePosition{
		AccountID:      ev.AggregateID,
		UserID:         tx.UserID,
		Symbol:         tx.Symbol,
		Underlying:     tx.Underlying,
		InstrumentType: tx.InstrumentType,
		Side:           tx.Side,
		Currency:       tx.Currency,
		Quantity:       tx.Quantity,
		EntryPrice:     tx.PricePerUnit,
		CurrentPrice:   tx.PricePerUnit,
		Leverage:       tx.Leverage,
		ContractSize:   tx.ContractSize,
		Status:         "open",
		OpenedAt:       tx.TransactionDate,
		OpenEventID:    ev.ID,
	}
	if tx.StrikePrice > 0 {
		pos.StrikePrice = &tx.StrikePrice
	}
	if tx.OptionType != "" {
		pos.OptionType = tx.OptionType
	}
	if tx.ExpiryDate != "" {
		pos.ExpiryDate = &tx.ExpiryDate
	}
	return p.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "open_event_id"}},
			DoNothing: true,
		}).
		Create(pos).Error
}

func (p *DerivativeProjector) handleClose(ctx context.Context, ev event.InvestmentEvent, tx event.TransactionRecorded) error {
	var pos derivDomain.DerivativePosition
	err := p.db.WithContext(ctx).
		Where("account_id = ? AND symbol = ? AND side = ? AND status = 'open'", ev.AggregateID, tx.Symbol, tx.Side).
		Order("opened_at ASC").
		First(&pos).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}

	realized := (tx.PricePerUnit - pos.EntryPrice) * tx.Quantity * pos.ContractSize
	if pos.Side == "short" {
		realized = -realized
	}

	now := time.Now().UTC()
	pos.Status = "closed"
	pos.ClosedAt = &now
	pos.RealizedPnL += realized
	pos.CloseEventID = &ev.ID
	return p.db.WithContext(ctx).Save(&pos).Error
}
