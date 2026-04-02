package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"mallow/investment/internal/module/transaction/domain"
)

type pgxRepo struct {
	pool *pgxpool.Pool
}

func NewPgx(pool *pgxpool.Pool) Repository {
	return &pgxRepo{pool: pool}
}

func (r *pgxRepo) ListByUserID(ctx context.Context, userID uuid.UUID, filter ListFilter) ([]domain.PortfolioTransaction, error) {
	q := `SELECT id, user_id, symbol, tx_type, currency,
		quantity::float8, price::float8, amount::float8, fees::float8,
		commission::float8, tax::float8, realized_pnl::float8,
		broker, external_id, source, notes, source_event_id, tx_date, created_at
	FROM portfolio_transactions WHERE user_id = $1`

	args := []any{userID}
	idx := 2

	if filter.TxType != "" {
		q += fmt.Sprintf(` AND tx_type = $%d`, idx)
		args = append(args, filter.TxType)
		idx++
	}
	if filter.Symbol != "" {
		q += fmt.Sprintf(` AND symbol = $%d`, idx)
		args = append(args, filter.Symbol)
		idx++
	}
	q += ` ORDER BY tx_date DESC`
	if filter.Limit > 0 {
		q += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, idx, idx+1)
		args = append(args, filter.Limit, filter.Offset)
	}

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var txs []domain.PortfolioTransaction
	for rows.Next() {
		var t domain.PortfolioTransaction
		err := rows.Scan(
			&t.ID, &t.UserID, &t.Symbol, &t.TxType, &t.Currency,
			&t.Quantity, &t.Price, &t.Amount, &t.Fees,
			&t.Commission, &t.Tax, &t.RealizedPnL,
			&t.Broker, &t.ExternalID, &t.Source, &t.Notes, &t.SourceEventID, &t.TxDate, &t.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		txs = append(txs, t)
	}
	return txs, rows.Err()
}
