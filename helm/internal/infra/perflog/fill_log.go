package perflog

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/runtime/perf"
)

const fillStream = "TRADE_FILLS"

// FillPage is one cursor page of fill results.
type FillPage struct {
	Fills   []natsapi.TransactionMsg `json:"fills"`
	Next    time.Time                `json:"next"`
	HasMore bool                     `json:"has_more"`
}

// FillLog queries the PostgreSQL `fills` table (populated by FillPersister).
type FillLog struct {
	db *sql.DB
}

func NewFillLog(db *sql.DB) *FillLog {
	return &FillLog{db: db}
}

// Query returns fills for accountID ordered newest-first.
func (l *FillLog) Query(ctx context.Context, accountID string, page perf.Page) (FillPage, error) {
	limit := page.Limit
	if limit <= 0 {
		limit = 200
	}

	args := []any{accountID, limit + 1}
	where := "account_id = $1"
	if !page.After.IsZero() {
		where += " AND filled_at < $3"
		args = append(args, page.After)
	}

	rows, err := l.db.QueryContext(ctx,
		`SELECT trade_id, order_id, helm_id, account_id, user_id, hand_id,
			symbol, side, kind, qty, avg_price, fee, filled_at
		 FROM fills
		 WHERE `+where+`
		 ORDER BY filled_at DESC
		 LIMIT $2`,
		args...,
	)
	if err != nil {
		return FillPage{}, fmt.Errorf("fills query: %w", err)
	}
	defer rows.Close()

	var fills []natsapi.TransactionMsg
	for rows.Next() {
		var (
			f               natsapi.TransactionMsg
			orderID         sql.NullString
			handID          sql.NullString
			qty, price, fee sql.NullString
		)
		if err := rows.Scan(
			&f.TradeID, &orderID, &f.HelmID, &f.AccountID, &f.UserID, &handID,
			&f.Symbol, &f.Side, &f.Kind, &qty, &price, &fee, &f.FilledAt,
		); err != nil {
			return FillPage{}, fmt.Errorf("fills scan: %w", err)
		}
		f.OrderID = orderID.String
		f.HandID = handID.String
		f.Qty = decimalOrZeroStr(qty.String)
		f.AvgPrice = decimalOrZeroStr(price.String)
		f.Fee = decimalOrZeroStr(fee.String)
		fills = append(fills, f)
	}
	if err := rows.Err(); err != nil {
		return FillPage{}, fmt.Errorf("fills rows: %w", err)
	}

	hasMore := len(fills) > limit
	if hasMore {
		fills = fills[:limit]
	}
	var next time.Time
	if hasMore && len(fills) > 0 {
		next = fills[len(fills)-1].FilledAt
	}
	return FillPage{Fills: fills, Next: next, HasMore: hasMore}, nil
}

func decimalOrZeroStr(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	d, _ := decimal.NewFromString(s)
	return d
}
