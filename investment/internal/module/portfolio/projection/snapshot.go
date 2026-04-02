package projection

import (
	"context"
	"encoding/json"

	"mallow/investment/internal/module/portfolio/event"
	snapshotDomain "mallow/investment/internal/module/snapshot/domain"

	"gorm.io/gorm"
)

// SnapshotProjector inserts rows into portfolio_snapshots on PortfolioSnapshotTaken.
type SnapshotProjector struct {
	db *gorm.DB
}

func NewSnapshotProjector(db *gorm.DB) Projector {
	return &SnapshotProjector{db: db}
}

func (p *SnapshotProjector) Project(ctx context.Context, ev event.InvestmentEvent) error {
	if ev.EventType != event.EventTypePortfolioSnapshotTaken {
		return nil
	}

	var snap event.PortfolioSnapshotTaken
	if err := json.Unmarshal(ev.Payload, &snap); err != nil {
		return err
	}

	row := &snapshotDomain.PortfolioSnapshot{
		AccountID:        ev.AggregateID,
		UserID:           snap.UserID,
		SnapshotDate:     snap.SnapshotDate,
		SnapshotType:     string(snap.SnapshotType),
		TotalValue:       snap.TotalValue,
		CashBalance:      snap.CashBalance,
		SpotValue:        snap.SpotValue,
		DerivativeValue:  snap.DerivativeValue,
		TotalCost:        snap.TotalCost,
		UnrealizedPnL:    snap.UnrealizedPnL,
		RealizedPnL:      snap.RealizedPnL,
		TotalDividends:   snap.TotalDividends,
		TotalFees:        snap.TotalFees,
		TotalReturn:      snap.TotalReturn,
		TotalReturnPct:   snap.TotalReturnPct,
		DayChange:        snap.DayChange,
		DayChangePct:     snap.DayChangePct,
		CashInflow:       snap.CashInflow,
		CashOutflow:      snap.CashOutflow,
		SpotAllocation:   snap.SpotAllocation,
		DerivativeAlloc:  snap.DerivativeAlloc,
		SectorAllocation: snap.SectorAllocation,
		Metrics:          snap.Metrics,
		SourceEventID:    ev.ID,
	}

	// Upsert: if a snapshot for (account_id, snapshot_date, snapshot_type) exists, update it
	result := p.db.WithContext(ctx).
		Where("account_id = ? AND snapshot_date = ? AND snapshot_type = ?",
			ev.AggregateID, snap.SnapshotDate, snap.SnapshotType).
		First(&snapshotDomain.PortfolioSnapshot{})

	if result.Error != nil {
		return p.db.WithContext(ctx).Create(row).Error
	}
	return p.db.WithContext(ctx).
		Where("account_id = ? AND snapshot_date = ? AND snapshot_type = ?",
			ev.AggregateID, snap.SnapshotDate, snap.SnapshotType).
		Updates(row).Error
}
