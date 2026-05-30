package runtime

import (
	"context"

	"github.com/nats-io/nats.go"
)

// StartFillStreaming starts account fill listeners for all runtimes whose exchange
// implements AccountStreamer. Called once from the app lifecycle after SetRuntime.
// Each HelmRuntime owns its own fill streaming goroutines — see helm_fills.go.
func (r *Registry) StartFillStreaming(ctx context.Context, _ *nats.Conn) {
	r.mu.RLock()
	rts := make([]*HelmRuntime, 0, len(r.helmRuntimes))
	for _, rt := range r.helmRuntimes {
		rts = append(rts, rt)
	}
	r.mu.RUnlock()

	for _, rt := range rts {
		rt.StartFillStreaming(ctx)
	}
}
