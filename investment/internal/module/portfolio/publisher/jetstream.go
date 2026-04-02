package publisher

import (
	"context"
	"encoding/json"
	"fmt"

	"mallow/investment/internal/module/portfolio/event"

	"github.com/nats-io/nats.go"
)

type jetstreamPublisher struct {
	js nats.JetStreamContext
}

// NewJetStreamPublisher creates a publisher that writes to the INVESTMENT_EVENTS stream.
func NewJetStreamPublisher(js nats.JetStreamContext) EventPublisher {
	return &jetstreamPublisher{js: js}
}

func (p *jetstreamPublisher) Publish(ctx context.Context, ev event.InvestmentEvent) error {
	subject := fmt.Sprintf("investment.events.%s", ev.AggregateID.String())

	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	_, err = p.js.Publish(subject, payload, nats.MsgId(ev.ID.String()))
	return err
}
