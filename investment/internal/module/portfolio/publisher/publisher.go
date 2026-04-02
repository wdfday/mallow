package publisher

import (
	"context"

	"mallow/investment/internal/module/portfolio/event"
)

// EventPublisher publishes committed events to NATS JetStream.
type EventPublisher interface {
	Publish(ctx context.Context, ev event.InvestmentEvent) error
}
