//! Locks the pnl_pct/mae_pct unit contract: Trade._pct fields are FRACTIONS,
//! report aggregate *_pct fields are PERCENTAGE POINTS.
use alm_core::order::{Fill, Side};
use alm_core::portfolio::Portfolio;
use alm_report::BacktestReport;

#[test]
fn trade_pct_is_fraction_aggregate_is_percent() {
    let mut p = Portfolio::new(10_000.0);
    let day = 86_400_000i64;
    let mut prices = std::collections::HashMap::new();
    p.record_equity(0, &prices);
    p.apply_fill(&Fill { timestamp: 1, symbol: "X".into(), side: Side::Buy,  qty: 2.0, price: 100.0, commission: 0.0 });
    prices.insert("X".to_string(), 110.0);
    p.apply_fill(&Fill { timestamp: day, symbol: "X".into(), side: Side::Sell, qty: 2.0, price: 110.0, commission: 0.0 });
    p.record_equity(day, &prices);

    // Per-trade: FRACTION (0.10 for +10%).
    let t = &p.trades[0];
    assert!((t.pnl_pct - 0.10).abs() < 1e-9, "trade.pnl_pct should be fraction 0.10, got {}", t.pnl_pct);

    // Aggregate: PERCENTAGE POINTS (0.2 for +0.2% of account).
    let r = BacktestReport::generate("audit", "X", &p, 0.0);
    assert!((r.avg_win_pct      - 0.2).abs() < 1e-6, "avg_win_pct={}", r.avg_win_pct);
    assert!((r.largest_win_pct  - 0.2).abs() < 1e-6, "largest_win_pct={}", r.largest_win_pct);
    assert!((r.expectancy       - 20.0).abs() < 1e-6, "expectancy={}", r.expectancy);
    assert!((r.win_rate_pct     - 100.0).abs() < 1e-6, "win_rate_pct={}", r.win_rate_pct);
}
