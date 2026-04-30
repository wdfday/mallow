use crate::{
    exit::ExitReason,
    order::{Fill, Side},
    trade::Trade,
};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

/// Open position for one symbol.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Position {
    pub symbol: String,
    /// Positive = long, negative = short
    pub qty: f64,
    pub avg_price: f64,
    pub entry_timestamp: i64,
    /// Entry-side commissions currently attached to the open quantity.
    pub entry_commission: f64,
    pub realized_pnl: f64,
}

impl Position {
    pub fn unrealized_pnl(&self, current_price: f64) -> f64 {
        (current_price - self.avg_price) * self.qty
    }

    pub fn market_value(&self, current_price: f64) -> f64 {
        self.qty * current_price
    }

    pub fn is_long(&self) -> bool {
        self.qty > 0.0
    }

    pub fn is_short(&self) -> bool {
        self.qty < 0.0
    }
}

/// Lightweight read-only snapshot of portfolio state passed to strategies each bar.
///
/// Strategies that need to know current position / equity can implement
/// `Strategy::set_portfolio_snapshot()`. The default is a no-op, so existing
/// strategies do not need to change.
#[derive(Debug, Clone, PartialEq)]
pub struct PortfolioSnapshot {
    pub cash: f64,
    pub equity: f64,
    /// Signed quantity per symbol (positive = long, negative = short, absent = flat).
    pub positions: std::collections::HashMap<String, PositionSummary>,
}

/// Compact position info for a single symbol.
#[derive(Debug, Clone, PartialEq)]
pub struct PositionSummary {
    /// Signed quantity: positive = long, negative = short.
    pub qty: f64,
    pub avg_price: f64,
    pub unrealized_pnl: f64,
}

impl PortfolioSnapshot {
    /// True when no position is open for `symbol`.
    pub fn is_flat(&self, symbol: &str) -> bool {
        self.positions.get(symbol).map(|p| p.qty.abs() < f64::EPSILON).unwrap_or(true)
    }
    /// True when holding a long position in `symbol`.
    pub fn is_long(&self, symbol: &str) -> bool {
        self.positions.get(symbol).map(|p| p.qty > f64::EPSILON).unwrap_or(false)
    }
    /// True when holding a short position in `symbol`.
    pub fn is_short(&self, symbol: &str) -> bool {
        self.positions.get(symbol).map(|p| p.qty < -f64::EPSILON).unwrap_or(false)
    }
    /// Signed quantity (0.0 if flat).
    pub fn qty(&self, symbol: &str) -> f64 {
        self.positions.get(symbol).map(|p| p.qty).unwrap_or(0.0)
    }
}

/// A point on the equity curve.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct EquityPoint {
    pub timestamp: i64,
    pub equity: f64,
}

/// Portfolio state — tracks cash, positions, equity curve, and completed trades.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Portfolio {
    pub initial_capital: f64,
    pub cash: f64,
    pub positions: HashMap<String, Position>,
    pub equity_curve: Vec<EquityPoint>,
    pub trades: Vec<Trade>,
}

impl Portfolio {
    pub fn new(initial_capital: f64) -> Self {
        Self {
            initial_capital,
            cash: initial_capital,
            positions: HashMap::new(),
            equity_curve: Vec::new(),
            trades: Vec::new(),
        }
    }

    /// Total equity = cash + sum of market values at current prices.
    pub fn equity(&self, prices: &HashMap<String, f64>) -> f64 {
        let market_value: f64 = self.positions.values().map(|p| {
            prices.get(&p.symbol).map(|&px| p.market_value(px)).unwrap_or(0.0)
        }).sum();
        self.cash + market_value
    }

    /// Snapshot equity curve at this bar's close.
    pub fn record_equity(&mut self, timestamp: i64, prices: &HashMap<String, f64>) {
        let eq = self.equity(prices);
        self.equity_curve.push(EquityPoint { timestamp, equity: eq });
    }

    /// Apply a fill, updating cash and position. Records a completed Trade on exit.
    ///
    /// Supports both long and short positions:
    /// - `Side::Buy` on flat/long → opens or adds to long
    /// - `Side::Buy` on short     → covers (closes) short, records short Trade
    /// - `Side::Sell` on flat/short → opens or adds to short
    /// - `Side::Sell` on long      → closes long, records long Trade
    pub fn apply_fill(&mut self, fill: &Fill) {
        self.cash += fill.cash_impact();

        self.positions.entry(fill.symbol.clone()).or_insert(Position {
            symbol: fill.symbol.clone(),
            qty: 0.0,
            avg_price: 0.0,
            entry_timestamp: fill.timestamp,
            entry_commission: 0.0,
            realized_pnl: 0.0,
        });

        let mut trade: Option<Trade> = None;
        let mut remove = false;

        let pos = self.positions.get_mut(&fill.symbol).unwrap();

        match fill.side {
            Side::Buy => {
                if pos.qty < -f64::EPSILON {
                    // Covering a short position
                    let existing_qty = pos.qty.abs();
                    let covered_qty = fill.qty.min(existing_qty);
                    let entry_commission = if existing_qty > f64::EPSILON {
                        pos.entry_commission * (covered_qty / existing_qty)
                    } else {
                        0.0
                    };
                    let exit_commission = if fill.qty > f64::EPSILON {
                        fill.commission * (covered_qty / fill.qty)
                    } else {
                        0.0
                    };
                    let pnl =
                        (pos.avg_price - fill.price) * covered_qty - entry_commission - exit_commission;
                    pos.realized_pnl += pnl;

                    if covered_qty > 0.0 {
                        trade = Some(Trade {
                            symbol: fill.symbol.clone(),
                            side: Side::Sell,
                            qty: covered_qty,
                            entry_price: pos.avg_price,
                            exit_price: fill.price,
                            entry_timestamp: pos.entry_timestamp,
                            exit_timestamp: fill.timestamp,
                            pnl,
                            pnl_pct: if pos.avg_price.abs() > f64::EPSILON {
                                pnl / (pos.avg_price * covered_qty)
                            } else {
                                0.0
                            },
                            commission: entry_commission + exit_commission,
                            mae_pct: 0.0,
                            mfe_pct: 0.0,
                            bars_held: 0,
                            exit_reason: ExitReason::Signal,
                        });
                    }

                    pos.entry_commission -= entry_commission;
                    let new_qty = pos.qty + covered_qty;
                    if new_qty.abs() < f64::EPSILON {
                        remove = true;
                    } else {
                        pos.qty = new_qty;
                    }
                } else {
                    // Opening or adding to long position
                    let new_qty = pos.qty + fill.qty;
                    if new_qty.abs() > f64::EPSILON {
                        pos.avg_price =
                            (pos.avg_price * pos.qty + fill.price * fill.qty) / new_qty;
                    }
                    if pos.qty <= 0.0 {
                        pos.entry_timestamp = fill.timestamp;
                    }
                    pos.qty = new_qty;
                    pos.entry_commission += fill.commission;
                }
            }
            Side::Sell => {
                if pos.qty > f64::EPSILON {
                    // Closing a long position
                    let existing_qty = pos.qty;
                    let closed_qty = fill.qty.min(existing_qty);
                    let entry_commission = if existing_qty > f64::EPSILON {
                        pos.entry_commission * (closed_qty / existing_qty)
                    } else {
                        0.0
                    };
                    let exit_commission = if fill.qty > f64::EPSILON {
                        fill.commission * (closed_qty / fill.qty)
                    } else {
                        0.0
                    };
                    let pnl =
                        (fill.price - pos.avg_price) * closed_qty - entry_commission - exit_commission;
                    pos.realized_pnl += pnl;

                    if closed_qty > 0.0 {
                        trade = Some(Trade {
                            symbol: fill.symbol.clone(),
                            side: Side::Buy,
                            qty: closed_qty,
                            entry_price: pos.avg_price,
                            exit_price: fill.price,
                            entry_timestamp: pos.entry_timestamp,
                            exit_timestamp: fill.timestamp,
                            pnl,
                            pnl_pct: pnl / (pos.avg_price * closed_qty),
                            commission: entry_commission + exit_commission,
                            mae_pct: 0.0,
                            mfe_pct: 0.0,
                            bars_held: 0,
                            exit_reason: ExitReason::Signal,
                        });
                    }

                    pos.entry_commission -= entry_commission;
                    pos.qty -= closed_qty;
                    if pos.qty.abs() < f64::EPSILON {
                        remove = true;
                    }
                } else {
                    // Opening or adding to short position (qty goes negative)
                    let new_qty = pos.qty - fill.qty;
                    if pos.qty.abs() < f64::EPSILON {
                        // New short from flat — record entry price
                        pos.avg_price = fill.price;
                        pos.entry_timestamp = fill.timestamp;
                    } else {
                        // Adding to existing short — VWAP avg entry
                        let abs_existing = pos.qty.abs();
                        let abs_new = new_qty.abs();
                        if abs_new > f64::EPSILON {
                            pos.avg_price =
                                (pos.avg_price * abs_existing + fill.price * fill.qty) / abs_new;
                        }
                    }
                    pos.qty = new_qty;
                    pos.entry_commission += fill.commission;
                }
            }
        }

        if remove {
            self.positions.remove(&fill.symbol);
        }
        if let Some(t) = trade {
            self.trades.push(t);
        }
    }

    /// Build a lightweight snapshot for passing to strategies.
    pub fn snapshot(&self, prices: &std::collections::HashMap<String, f64>) -> PortfolioSnapshot {
        let equity = self.equity(prices);
        let positions = self
            .positions
            .iter()
            .map(|(sym, pos)| {
                let last = prices.get(sym).copied().unwrap_or(pos.avg_price);
                (
                    sym.clone(),
                    PositionSummary {
                        qty: pos.qty,
                        avg_price: pos.avg_price,
                        unrealized_pnl: pos.unrealized_pnl(last),
                    },
                )
            })
            .collect();
        PortfolioSnapshot { cash: self.cash, equity, positions }
    }

    pub fn total_return(&self) -> f64 {
        let last_equity = self.equity_curve.last().map(|p| p.equity).unwrap_or(self.cash);
        (last_equity - self.initial_capital) / self.initial_capital
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn long_trade_pnl_includes_entry_and_exit_commission() {
        let mut portfolio = Portfolio::new(1_000.0);
        portfolio.apply_fill(&Fill {
            timestamp: 1,
            symbol: "BNBUSDT".into(),
            side: Side::Buy,
            qty: 2.0,
            price: 100.0,
            commission: 1.0,
        });
        portfolio.apply_fill(&Fill {
            timestamp: 2,
            symbol: "BNBUSDT".into(),
            side: Side::Sell,
            qty: 2.0,
            price: 110.0,
            commission: 1.5,
        });

        assert_eq!(portfolio.trades.len(), 1);
        let trade = &portfolio.trades[0];
        assert!((trade.pnl - 17.5).abs() < 1e-9);
        assert!((portfolio.cash - 1_017.5).abs() < 1e-9);
    }

    #[test]
    fn partial_close_allocates_entry_commission_proportionally() {
        let mut portfolio = Portfolio::new(1_000.0);
        portfolio.apply_fill(&Fill {
            timestamp: 1,
            symbol: "BTCUSDT".into(),
            side: Side::Buy,
            qty: 4.0,
            price: 100.0,
            commission: 4.0,
        });
        portfolio.apply_fill(&Fill {
            timestamp: 2,
            symbol: "BTCUSDT".into(),
            side: Side::Sell,
            qty: 1.0,
            price: 110.0,
            commission: 1.0,
        });

        assert_eq!(portfolio.trades.len(), 1);
        let trade = &portfolio.trades[0];
        assert!((trade.pnl - 8.0).abs() < 1e-9);
        let position = portfolio.positions.get("BTCUSDT").expect("remaining long");
        assert!((position.qty - 3.0).abs() < 1e-9);
        assert!((position.entry_commission - 3.0).abs() < 1e-9);
    }

    #[test]
    fn short_trade_pnl_includes_entry_and_exit_commission() {
        let mut portfolio = Portfolio::new(1_000.0);
        portfolio.apply_fill(&Fill {
            timestamp: 1,
            symbol: "BNBUSDT".into(),
            side: Side::Sell,
            qty: 2.0,
            price: 100.0,
            commission: 1.0,
        });
        portfolio.apply_fill(&Fill {
            timestamp: 2,
            symbol: "BNBUSDT".into(),
            side: Side::Buy,
            qty: 2.0,
            price: 90.0,
            commission: 1.5,
        });

        assert_eq!(portfolio.trades.len(), 1);
        let trade = &portfolio.trades[0];
        assert!((trade.pnl - 17.5).abs() < 1e-9);
        assert!((portfolio.cash - 1_017.5).abs() < 1e-9);
    }
}
