package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"mallow/investment/internal/module/watchlist/domain"
)

type pgxRepo struct {
	pool *pgxpool.Pool
}

func NewPgx(pool *pgxpool.Pool) Repository {
	return &pgxRepo{pool: pool}
}

func (r *pgxRepo) ListByUserID(ctx context.Context, userID uuid.UUID) ([]domain.WatchlistItem, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, user_id, symbol, name, asset_type, target_price::float8, notes, added_at, updated_at
		FROM investment_watchlist WHERE user_id = $1 ORDER BY added_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []domain.WatchlistItem
	for rows.Next() {
		var item domain.WatchlistItem
		var name, assetType, notes *string
		err := rows.Scan(
			&item.ID, &item.UserID, &item.Symbol,
			&name, &assetType, &item.TargetPrice, &notes,
			&item.AddedAt, &item.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		if name != nil {
			item.Name = *name
		}
		if assetType != nil {
			item.AssetType = *assetType
		}
		if notes != nil {
			item.Notes = *notes
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *pgxRepo) Add(ctx context.Context, item *domain.WatchlistItem) error {
	var targetPrice *float64
	if item.TargetPrice != nil {
		targetPrice = item.TargetPrice
	}

	return r.pool.QueryRow(ctx,
		`INSERT INTO investment_watchlist (id, user_id, symbol, name, asset_type, target_price, notes, added_at, updated_at)
		VALUES (uuidv7(), $1, $2, $3, $4, $5, $6, now(), now())
		RETURNING id, added_at, updated_at`,
		item.UserID, item.Symbol, item.Name, item.AssetType, targetPrice, item.Notes,
	).Scan(&item.ID, &item.AddedAt, &item.UpdatedAt)
}

func (r *pgxRepo) Delete(ctx context.Context, userID uuid.UUID, symbol string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM investment_watchlist WHERE user_id = $1 AND symbol = $2`,
		userID, symbol,
	)
	return err
}
