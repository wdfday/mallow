package act

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/adshao/go-binance/v2/futures"

	"mallow/helm/internal/infra/exchange"
)

// PlaceOrder places a USDM futures order.
func (c *Client) PlaceOrder(ctx context.Context, creds exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	side := futures.SideTypeBuy
	if req.Side == exchange.Sell {
		side = futures.SideTypeSell
	}
	orderType := futures.OrderTypeMarket
	if req.Type == exchange.Limit {
		orderType = futures.OrderTypeLimit
	}

	svc := c.newFut(creds).NewCreateOrderService().
		Symbol(req.Symbol).
		Side(side).
		Type(orderType).
		Quantity(req.Qty.String()).
		ReduceOnly(req.ReduceOnly)
	if req.ClientOrderID != "" {
		svc = svc.NewClientOrderID(req.ClientOrderID)
	}
	if orderType == futures.OrderTypeLimit {
		svc = svc.TimeInForce(binanceFuturesTIF(req.TIF)).Price(req.Price.String())
	}

	slog.Info("fbinance: placing futures order", "symbol", req.Symbol, "side", req.Side,
		"qty", req.Qty, "type", orderType, "reduce_only", req.ReduceOnly)
	t0 := time.Now()
	resp, err := svc.Do(ctx)
	if err != nil {
		slog.Error("fbinance: futures order rejected", "symbol", req.Symbol, "err", err,
			"rest_latency", time.Since(t0).Truncate(time.Millisecond))
		return nil, fmt.Errorf("fbinance place order: %w", err)
	}
	result := futuresCreateToResult(req.Side, resp)
	slog.Info("fbinance: futures order ack", "symbol", result.Symbol, "order_id", result.ID,
		"status", result.Status, "filled_qty", result.FilledQty,
		"rest_latency", time.Since(t0).Truncate(time.Millisecond))
	return result, nil
}

// GetOrder is not supported directly for futures — symbol context is required.
// Returns an error directing callers to use GetFuturesOrder.
func (c *Client) GetOrder(ctx context.Context, creds exchange.Credentials, orderID string) (*exchange.OrderResult, error) {
	return nil, fmt.Errorf("fbinance: GetOrder requires symbol; use GetFuturesOrder(symbol, orderID)")
}

// GetFuturesOrder retrieves a futures order by symbol + numeric order ID.
func (c *Client) GetFuturesOrder(ctx context.Context, creds exchange.Credentials, symbol string, orderID int64) (*exchange.OrderResult, error) {
	resp, err := c.newFut(creds).NewGetOrderService().Symbol(symbol).OrderID(orderID).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("fbinance get order: %w", err)
	}
	return &exchange.OrderResult{
		ID:            strconv.FormatInt(resp.OrderID, 10),
		ClientOrderID: resp.ClientOrderID,
		Symbol:        resp.Symbol,
		Side:          exchange.OrderSide(strings.ToLower(string(resp.Side))),
		Status:        strings.ToLower(string(resp.Status)),
		Qty:           parseDecimal(resp.OrigQuantity),
		FilledQty:     parseDecimal(resp.ExecutedQuantity),
	}, nil
}

// CancelOrder is not supported directly for futures — symbol context is required.
// Returns an error directing callers to use CancelFuturesOrder.
func (c *Client) CancelOrder(ctx context.Context, creds exchange.Credentials, orderID string) error {
	return fmt.Errorf("fbinance: CancelOrder requires symbol; use CancelFuturesOrder")
}

// CancelFuturesOrder cancels a futures order by symbol + numeric order ID.
func (c *Client) CancelFuturesOrder(ctx context.Context, creds exchange.Credentials, symbol string, orderID int64) error {
	_, err := c.newFut(creds).NewCancelOrderService().Symbol(symbol).OrderID(orderID).Do(ctx)
	if err != nil {
		return fmt.Errorf("fbinance cancel order: %w", err)
	}
	return nil
}

// GetOrderByClientOrderID looks up a futures order by clOrdId.
func (c *Client) GetOrderByClientOrderID(ctx context.Context, creds exchange.Credentials, symbol string, market exchange.MarketKind, clid string) (*exchange.OrderResult, error) {
	if c.paper {
		return nil, nil
	}
	resp, err := c.newFut(creds).NewGetOrderService().Symbol(symbol).OrigClientOrderID(clid).Do(ctx)
	if err != nil || resp == nil {
		return nil, nil
	}
	return &exchange.OrderResult{
		ID:            strconv.FormatInt(resp.OrderID, 10),
		ClientOrderID: resp.ClientOrderID,
		Symbol:        resp.Symbol,
		Side:          exchange.OrderSide(strings.ToLower(string(resp.Side))),
		Status:        strings.ToLower(string(resp.Status)),
		Qty:           parseDecimal(resp.OrigQuantity),
		FilledQty:     parseDecimal(resp.ExecutedQuantity),
	}, nil
}

// PlaceExitOrders places SL/TP bracket orders for a USDM futures position.
func (c *Client) PlaceExitOrders(ctx context.Context, creds exchange.Credentials, req exchange.ExitOrderRequest) (*exchange.ExitOrderResult, error) {
	side := futures.SideTypeSell
	if req.Side == exchange.Buy {
		side = futures.SideTypeBuy
	}

	var ids []string
	// Like Bybit, fbinance places SL/TP as two independent algo orders (no
	// shared group id), so a single clid can't be reused for both — suffix per
	// leg. The base clid alone is what reconcile queries both variants from.
	var slClientAlgoID, tpClientAlgoID string
	if req.ClientOrderID != "" {
		slClientAlgoID = req.ClientOrderID + "S"
		tpClientAlgoID = req.ClientOrderID + "T"
	}

	if req.StopLoss.IsPositive() {
		slog.Info("fbinance: placing futures SL exit", "symbol", req.Symbol, "side", side, "sl", req.StopLoss)
		svc := c.newFut(creds).NewCreateAlgoOrderService().
			Symbol(req.Symbol).
			Side(side).
			Type(futures.AlgoOrderTypeStopMarket).
			TriggerPrice(req.StopLoss.String()).
			Quantity(req.Qty.String()).
			ReduceOnly(true)
		if slClientAlgoID != "" {
			svc = svc.ClientAlgoId(slClientAlgoID)
		}
		resp, err := svc.Do(ctx)
		if err != nil {
			return nil, fmt.Errorf("fbinance futures SL exit: %w", err)
		}
		ids = append(ids, strconv.FormatInt(resp.AlgoId, 10))
	}

	if req.TakeProfit.IsPositive() {
		slog.Info("fbinance: placing futures TP exit", "symbol", req.Symbol, "side", side, "tp", req.TakeProfit)
		svc := c.newFut(creds).NewCreateAlgoOrderService().
			Symbol(req.Symbol).
			Side(side).
			Type(futures.AlgoOrderTypeTakeProfitMarket).
			TriggerPrice(req.TakeProfit.String()).
			Quantity(req.Qty.String()).
			ReduceOnly(true)
		if tpClientAlgoID != "" {
			svc = svc.ClientAlgoId(tpClientAlgoID)
		}
		resp, err := svc.Do(ctx)
		if err != nil {
			return nil, fmt.Errorf("fbinance futures TP exit: %w", err)
		}
		ids = append(ids, strconv.FormatInt(resp.AlgoId, 10))
	}

	return &exchange.ExitOrderResult{OrderIDs: ids, ClientOrderID: req.ClientOrderID}, nil
}

// GetExitOrderByClientOrderID implements exchange.ExitOrderQuerier — resolves an
// ambiguous PlaceExitOrders call (crash between the REST call and
// KindBracketPlaced) after a restart. Tries both suffixed client algo ids
// (clientOrderID+"S" for SL, +"T" for TP — see PlaceExitOrders) via
// GetAlgoOrderService.ClientAlgoID, which covers any algo status (not just
// open), unlike Bybit/OKX's realtime-only fallback.
func (c *Client) GetExitOrderByClientOrderID(ctx context.Context, creds exchange.Credentials, _ string, _ exchange.MarketKind, clientOrderID string) ([]exchange.OrderResult, error) {
	var results []exchange.OrderResult
	for _, id := range []string{clientOrderID + "S", clientOrderID + "T"} {
		resp, err := c.newFut(creds).NewGetAlgoOrderService().ClientAlgoID(id).Do(ctx)
		if err != nil || resp == nil || resp.AlgoId == 0 {
			continue
		}
		results = append(results, exchange.OrderResult{
			ID:        strconv.FormatInt(resp.AlgoId, 10),
			Symbol:    resp.Symbol,
			Side:      exchange.OrderSide(resp.Side),
			Status:    strings.ToLower(string(resp.AlgoStatus)),
			Qty:       parseDecimal(resp.Quantity),
			FilledAvg: parseDecimal(resp.ActualPrice),
		})
	}
	return results, nil
}
