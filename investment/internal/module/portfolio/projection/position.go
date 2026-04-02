package projection

import (
	"context"
	"encoding/json"
	"errors"

	"mallow/investment/internal/module/portfolio/event"
	positionDomain "mallow/investment/internal/module/position/domain"

	"gorm.io/gorm"
)

// PositionProjector updates portfolio_positions on TransactionRecorded and AssetPriceUpdated.
type PositionProjector struct {
	db *gorm.DB
}

func NewPositionProjector(db *gorm.DB) *PositionProjector {
	return &PositionProjector{db: db}
}

func (p *PositionProjector) Project(ctx context.Context, ev event.InvestmentEvent) error {
	switch ev.EventType {
	case event.EventTypeTransactionRecorded:
		return p.onTransaction(ctx, ev)
	}
	return nil
}

func (p *PositionProjector) onTransaction(ctx context.Context, ev event.InvestmentEvent) error {
	var tx event.TransactionRecorded
	if err := json.Unmarshal(ev.Payload, &tx); err != nil {
		return err
	}

	// Only spot transaction types affect positions
	switch tx.Type {
	case event.TransactionTypeBuy:
		return p.handleBuy(ctx, ev, tx)
	case event.TransactionTypeSell:
		return p.handleSell(ctx, ev, tx)
	case event.TransactionTypeDividend:
		return p.handleDividend(ctx, ev, tx)
	}
	return nil
}

func (p *PositionProjector) handleBuy(ctx context.Context, ev event.InvestmentEvent, tx event.TransactionRecorded) error {
	pos := &positionDomain.PortfolioPosition{}
	err := p.db.WithContext(ctx).
		Where("account_id = ? AND symbol = ?", ev.AggregateID, tx.Symbol).
		First(pos).Error

	if err != nil {
		// New position
		pos = &positionDomain.PortfolioPosition{
			AccountID: ev.AggregateID,
			UserID:    tx.UserID,
			Symbol:    tx.Symbol,
			Name:      tx.Name,
			AssetType: tx.AssetType,
			Exchange:  tx.Exchange,
			Currency:  tx.Currency,
			Status:    "active",
			OpenedAt:  tx.TransactionDate,
			LastSeq:   ev.Sequence,
		}
		pos.AddQuantity(tx.Quantity, tx.PricePerUnit)
		return p.db.WithContext(ctx).Create(pos).Error
	}

	if ev.Sequence <= pos.LastSeq {
		return nil // idempotency
	}
	pos.AddQuantity(tx.Quantity, tx.PricePerUnit)
	pos.LastSeq = ev.Sequence
	pos.Status = "active"
	return p.db.WithContext(ctx).Save(pos).Error
}

func (p *PositionProjector) handleSell(ctx context.Context, ev event.InvestmentEvent, tx event.TransactionRecorded) error {
	var pos positionDomain.PortfolioPosition
	err := p.db.WithContext(ctx).
		Where("account_id = ? AND symbol = ?", ev.AggregateID, tx.Symbol).
		First(&pos).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // no position to sell from
		}
		return err
	}
	if ev.Sequence <= pos.LastSeq {
		return nil
	}
	pos.RemoveQuantity(tx.Quantity, tx.PricePerUnit)
	pos.LastSeq = ev.Sequence
	return p.db.WithContext(ctx).Save(&pos).Error
}

func (p *PositionProjector) handleDividend(ctx context.Context, ev event.InvestmentEvent, tx event.TransactionRecorded) error {
	if tx.Symbol == "" {
		return nil
	}
	return p.db.WithContext(ctx).
		Model(&positionDomain.PortfolioPosition{}).
		Where("account_id = ? AND symbol = ?", ev.AggregateID, tx.Symbol).
		UpdateColumn("total_dividends", gorm.Expr("total_dividends + ?", tx.Amount)).Error
}
