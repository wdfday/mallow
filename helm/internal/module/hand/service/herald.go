package service

import (
	"context"
	"time"

	"mallow/helm/internal/fleet/actor"
	"mallow/helm/internal/module/hand/domain"
)

// heraldValidate calls engine.validate for all symbols via the runtime's Herald.
// Uses the same HeraldRegistrar path as register/deregister — no separate herald interface.
func (s *Service) heraldValidate(cfg domain.HandConfig, rt *actor.HelmRuntime) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return rt.ValidateHandAll(ctx, cfg.HelmID.String(), cfg.Symbols, cfg.Strategy.Script, cfg.Strategy.Timeframe, cfg.Market == domain.MarketTypeFutures)
}
