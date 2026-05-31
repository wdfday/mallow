use alm_core::{
    order::{Fill, OrderKind, OrderRequest, Side},
    Bar,
};

/// Simulated broker for backtesting.
///
/// Fill rule: market orders fill at the **next bar's open** price + slippage.
/// This avoids look-ahead bias (we never fill at the same bar that generates the signal).
pub struct SimBroker {
    /// Commission as a fraction of trade value, e.g. 0.001 = 0.1%
    pub commission_pct: f64,
    /// Slippage as a fraction of price, e.g. 0.0005 = 0.05%
    pub slippage_pct: f64,
    pending: Vec<OrderRequest>,
}

impl SimBroker {
    pub fn new(commission_pct: f64, slippage_pct: f64) -> Self {
        Self { commission_pct, slippage_pct, pending: Vec::new() }
    }

    /// Zero-cost broker (for strategy-only evaluation without slippage)
    pub fn zero_cost() -> Self {
        Self::new(0.0, 0.0)
    }

    pub fn submit(&mut self, order: OrderRequest) {
        self.pending.push(order);
    }

    /// Cancel all pending orders for the given symbol.
    /// Called when exit rules force-close a position so the pending close order
    /// (from a script `exit = true` signal on the previous bar) does not execute
    /// a second time and accidentally open an unintended short position.
    pub fn cancel_for_symbol(&mut self, symbol: &str) {
        self.pending.retain(|o| o.symbol != symbol);
    }

    /// Process pending orders using `bar` prices. Called at the START of each bar.
    /// Returns fills that were executed.
    pub fn process_pending(&mut self, bar: &Bar) -> Vec<Fill> {
        let mut fills = Vec::new();

        self.pending.retain(|order| {
            // Independent pyramiding legs are keyed `SYM#n`; match on the base
            // symbol so they fill against the underlying symbol's bar.
            if crate::engine::base_symbol(&order.symbol) != bar.symbol {
                return true; // keep — different symbol, process later
            }

            let fill_price = match &order.kind {
                OrderKind::Market => {
                    let slip = match order.side {
                        Side::Buy => 1.0 + self.slippage_pct,
                        Side::Sell => 1.0 - self.slippage_pct,
                    };
                    bar.open * slip
                }
                OrderKind::Limit { price } => {
                    // Fill if price is reachable within the bar.
                    // If bar gaps past the limit (e.g., open is already better),
                    // fill at the better of limit price vs. bar open (no look-ahead).
                    match order.side {
                        Side::Buy => {
                            if bar.low <= *price {
                                bar.open.min(*price) // gap down → fill at open (better price)
                            } else {
                                return true; // keep pending
                            }
                        }
                        Side::Sell => {
                            if bar.high >= *price {
                                bar.open.max(*price) // gap up → fill at open (better price)
                            } else {
                                return true; // keep pending
                            }
                        }
                    }
                }
            };

            let commission = fill_price * order.qty * self.commission_pct;
            fills.push(Fill {
                timestamp: bar.timestamp,
                symbol: order.symbol.clone(),
                side: order.side,
                qty: order.qty,
                price: fill_price,
                commission,
            });

            false // remove from pending
        });

        fills
    }

    /// Force-close a position at the last bar's close. Called at end of backtest.
    /// `side` should be `Sell` for long positions, `Buy` for short positions.
    pub fn force_close(
        &mut self,
        symbol: &str,
        qty: f64,
        side: Side,
        timestamp: i64,
        price: f64,
    ) -> Fill {
        let slip = match side {
            Side::Buy => 1.0 + self.slippage_pct,
            Side::Sell => 1.0 - self.slippage_pct,
        };
        let fill_price = price * slip;
        let commission = fill_price * qty * self.commission_pct;
        Fill { timestamp, symbol: symbol.to_string(), side, qty, price: fill_price, commission }
    }

    pub fn has_pending(&self) -> bool {
        !self.pending.is_empty()
    }
}
