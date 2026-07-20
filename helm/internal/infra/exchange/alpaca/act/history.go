package act

import (
	"context"
	"fmt"
	"time"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"

	"mallow/helm/internal/infra/exchange"
)

// FilledOrders implements exchange.HistoryFetcher.
// Queries closed orders in [from, to) and returns those with filled status.
func (c *Client) FilledOrders(ctx context.Context, creds exchange.Credentials, _ []string, from, to time.Time) ([]exchange.AccountTransaction, error) {
	sdk := c.newSDK(creds)
	orders, err := sdk.GetOrders(alpacasdk.GetOrdersRequest{
		Status:    "closed",
		After:     from,
		Until:     to,
		Direction: "asc",
		Limit:     500,
	})
	if err != nil {
		return nil, fmt.Errorf("alpaca history orders: %w", err)
	}

	all := make([]exchange.AccountTransaction, 0, len(orders))
	for _, o := range orders {
		if o.Status != "filled" && o.Status != "partially_filled" {
			continue
		}
		if !o.FilledQty.IsPositive() || o.FilledAvgPrice == nil {
			continue
		}
		side := "buy"
		if o.Side == alpacasdk.Sell {
			side = "sell"
		}
		filledAt := time.Now().UTC()
		if o.FilledAt != nil {
			filledAt = o.FilledAt.UTC()
		}
		all = append(all, exchange.AccountTransaction{
			// FillID left empty: the Orders API has no per-execution fill ID (only
			// GetAccountActivities does, via ActivityType "FILL" — a separate endpoint
			// this adapter doesn't call). o.ID is the ORDER id, not a fill id; using it
			// here would collide with a real per-execution FillID the WS path may have
			// already published for the same fill under a different key. Leaving it empty
			// routes through PublishTradeFill's helmID+orderID fallback, consistent with
			// every other no-fill-id path (poll/kill/limit-timeout) in the system.
			OrderID:       o.ID,
			ClientOrderID: o.ClientOrderID,
			Symbol:        o.Symbol,
			Side:          side,
			Qty:           o.FilledQty,
			AvgPrice:      *o.FilledAvgPrice,
			FilledAt:      filledAt,
		})
	}
	return all, nil
}

// ── KNOWN GAP: cross-path FillID mismatch can still let a duplicate trade.filled
// through for Alpaca specifically (2026-07-10, found during pre-defense review) ──
//
// FilledOrders (this function) always leaves FillID empty because the Orders API
// has no per-execution fill ID — PublishTradeFill's fallback then keys the NATS
// Nats-Msg-Id on helmID+orderID (see PublishTradeFill in natsapi/protocol.go).
//
// The WS path (streaming.go) is different: it uses tu.ExecutionID when Alpaca
// provides one, falling back to orderID+"_fill" only when it doesn't (see
// runFillProcessor's onFill closure). So for the SAME fill, the WS path and this
// Sync()-driven gap-recovery path can compute two DIFFERENT dedup keys
// (real ExecutionID vs. helmID+orderID) — JetStream's Nats-Msg-Id dedup only
// catches exact key matches, so it does not catch this cross-path case.
//
// Exposure window: this only matters when BOTH paths fire for the same fill,
// which needs a restart between them (the in-memory hasOrderFillPublished/orderID
// guard — not FillID-keyed — is what normally prevents the second publish; it is
// wiped on restart, same failure mode as the fillDedup gaps already documented
// elsewhere in this session).
//
// Real fix: switch FilledOrders to alpacasdk.GetAccountActivities(ActivityTypes:
// ["FILL"]) — AccountActivity.ID is a genuine per-execution ID distinct from
// OrderID, matching the pattern Binance/OKX/Bybit/fbinance already use. Not done
// tonight: different endpoint, different response shape, AccountActivity has no
// ClientOrderID field (this adapter currently uses o.ClientOrderID for hand
// routing — would need a fallback or an extra lookup), and untestable here
// (sandbox has no network egress to Alpaca). Deferred, not implemented.
