package repository

import (
	"context"

	"github.com/google/uuid"
	"mallow/investment/internal/module/cash_flow/domain"
)

type Repository interface {
	ListByUserID(ctx context.Context, userID uuid.UUID, filter ListFilter) ([]domain.PortfolioCashFlow, error)
}

type ListFilter struct {
	FlowType string // "dividend" | "deposit" | "withdrawal" | "fee" | "" (all)
	Limit    int
	Offset   int
}
