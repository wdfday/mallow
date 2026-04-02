package projection

import (
	"context"

	"mallow/investment/internal/module/portfolio/event"
)

// Projector handles a specific event type and updates a read model.
type Projector interface {
	Project(ctx context.Context, ev event.InvestmentEvent) error
}

// Runner dispatches events to all registered projectors.
type Runner struct {
	projectors []Projector
}

func NewRunner(projectors ...Projector) *Runner {
	return &Runner{projectors: projectors}
}

func (r *Runner) Project(ctx context.Context, ev event.InvestmentEvent) error {
	for _, p := range r.projectors {
		if err := p.Project(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}
