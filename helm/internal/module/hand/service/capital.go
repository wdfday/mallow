package service

import (
	"fmt"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/module/hand/dto"
)

// CheckCapitalAllocation validates that the requested allocation fits inside the
// helm's unallocated capital budget.
//
// Parameters:
//   - helmCash: Portfolio.Summary().Cash (liquid broker cash — not total equity)
//   - existing: current hand allocations on the helm
//   - allocatedCapital: the AllocatedCapital being requested
//   - excludeHandID: current hand's ID when updating (avoids double-counting); "" when creating
//
// Returns (*dto.CapitalOverflow, nil) on insufficient budget; (nil, nil) when valid.
// Skipped when allocatedCapital is zero (shared-pool hand — no per-hand budget).
func CheckCapitalAllocation(
	helmCash float64,
	existing []domain.HandSummary,
	allocatedCapital decimal.Decimal,
	excludeHandID string,
) (*dto.CapitalOverflow, error) {
	if !allocatedCapital.IsPositive() {
		return nil, nil
	}
	var used float64
	for _, b := range existing {
		if b.ID.String() == excludeHandID || b.Status.IsTerminal() {
			continue
		}
		if alloc := b.AllocatedCapital.InexactFloat64(); alloc > 0 {
			used += alloc
		}
	}
	available := helmCash - used
	if available < 0 {
		// Pre-existing skew: total alloc already exceeds equity. Clamp reported
		// "available" to 0 so the error message is intelligible.
		available = 0
	}
	requesting := allocatedCapital.InexactFloat64()
	if requesting <= available {
		return nil, nil
	}
	shortage := requesting - available
	return buildOverflow(
		fmt.Sprintf("insufficient unallocated capital: requesting %.2f but only %.2f available "+
			"(helm cash %.2f, already allocated %.2f)", requesting, available, helmCash, used),
		helmCash, used, requesting, available, shortage, existing, excludeHandID,
	), nil
}

// CheckSymbolConflict blocks a hand from claiming a symbol already traded by another
// non-terminal hand on the same helm. The exchange holds ONE net position per symbol per
// account, so two hands on the same symbol cannot keep independent positions.
func CheckSymbolConflict(existing []domain.HandSummary, symbols []string, excludeHandID string) error {
	claimed := make(map[string]string, len(existing))
	for i := range existing {
		b := existing[i]
		if b.ID.String() == excludeHandID || b.Status.IsTerminal() {
			continue
		}
		for _, s := range b.Symbols {
			claimed[s] = b.Name
		}
	}
	for _, s := range symbols {
		if owner, ok := claimed[s]; ok {
			return fmt.Errorf("symbol %s is already traded by hand %q on this helm — one symbol can have at most one hand per helm", s, owner)
		}
	}
	return nil
}

// buildOverflow assembles a CapitalOverflow with sorted suggestions.
// Hands with the most reducible free capital are listed first.
func buildOverflow(
	msg string,
	helmEquity, usedAbs, requesting, available, shortage float64,
	existing []domain.HandSummary,
	excludeHandID string,
) *dto.CapitalOverflow {
	var suggestions []dto.CapitalSuggestion
	remaining := shortage
	for _, b := range existing {
		if b.ID.String() == excludeHandID || b.Status.IsTerminal() {
			continue
		}
		alloc := b.AllocatedCapital
		if !alloc.IsPositive() {
			continue
		}
		deployed := b.DeployedCapital
		reducible := alloc.Sub(deployed)
		if !reducible.IsPositive() {
			continue
		}
		reduceBy := reducible
		if remaining > 0 {
			cap := decimal.NewFromFloat(remaining)
			if reducible.GreaterThan(cap) {
				reduceBy = cap
			}
			remaining -= reduceBy.InexactFloat64()
		}
		suggestions = append(suggestions, dto.CapitalSuggestion{
			HandID:          b.ID.String(),
			Name:            b.Name,
			Allocated:       alloc,
			Deployed:        deployed,
			ReducibleBy:     reducible,
			SuggestedTarget: alloc.Sub(reduceBy),
		})
	}
	return &dto.CapitalOverflow{
		Error:       msg,
		HelmEquity:  helmEquity,
		TotalAlloc:  usedAbs,
		Requested:   requesting,
		Available:   available,
		Suggestions: suggestions,
	}
}
