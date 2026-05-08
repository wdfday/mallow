package oanda

import (
	"context"

	"mallow/helm/internal/infra/exchange"
)

func (c *Client) ListOpenOrders(ctx context.Context, creds exchange.Credentials, symbol string) ([]exchange.OrderResult, error) {
	panic("not implemented")
}

func (c *Client) ListPositions(ctx context.Context, creds exchange.Credentials) ([]exchange.PositionResult, error) {
	panic("not implemented")
}

func (c *Client) SubscribeFills(ctx context.Context, creds exchange.Credentials) (<-chan exchange.FillEvent, error) {
	panic("not implemented")
}
