use std::path::PathBuf;

use alm_core::{Bar, Timeframe};
use alm_data::{BarFeed, ParquetFeed, BarVecFeed};
use alm_engine::{HeapMtfEngine, PointerSyncMtfEngine};
use alm_strategy::{MtfEmaRsiStrategy, MtfMaCrossStrategy};
use alm_engine::risk::PercentEquity;

fn load_real_bars() -> Option<Vec<Bar>> {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("data/testdata/BTCUSDT/M1/BTCUSDT_M1_2026-01.parquet");
    if !path.exists() {
        eprintln!("[integration-parity] testdata missing at {}, skipping", path.display());
        return None;
    }
    let mut feed = ParquetFeed::load(&path, "BTCUSDT").ok()?;
    let mut bars: Vec<Bar> = std::iter::from_fn(|| feed.next()).collect();
    bars.truncate(20000);
    Some(bars)
}

fn load_real_m1_h1() -> Option<(Vec<Bar>, Vec<Bar>)> {
    let m1 = load_real_bars()?;
    let path_h1 = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("data/testdata/BTCUSDT/H1/BTCUSDT_H1_2026-01.parquet");
    if !path_h1.exists() {
        eprintln!("[integration-parity] testdata missing at {}, skipping", path_h1.display());
        return None;
    }
    let mut feed_h1 = ParquetFeed::load(&path_h1, "BTCUSDT").ok()?;
    let h1_all: Vec<Bar> = std::iter::from_fn(|| feed_h1.next()).collect();
    let t_start = m1.first().unwrap().timestamp;
    let t_end = m1.last().unwrap().timestamp;
    let h1: Vec<Bar> = h1_all
        .into_iter()
        .filter(|b| b.timestamp >= t_start && b.timestamp <= t_end)
        .collect();
    Some((m1, h1))
}

#[test]
fn test_real_data_ema_rsi_parity() {
    let Some((m1_data, h1_data)) = load_real_m1_h1() else {
        return;
    };

    // 1. Run Heap MtfEngine
    let m1_feed_heap = BarVecFeed::new(m1_data.clone(), "BTCUSDT".into());
    let h1_feed_heap = BarVecFeed::new(h1_data.clone(), "BTCUSDT".into());

    let mut engine_heap = HeapMtfEngine::sync(
        10_000.0,
        MtfEmaRsiStrategy::new(),
        PercentEquity::fractional(0.95, 1),
        0.0,
        0.0,
    )
    .with_base_tf(Timeframe::M1)
    .with_single_entry();
    engine_heap.add_feed(Timeframe::M1, m1_feed_heap);
    engine_heap.add_feed(Timeframe::H1, h1_feed_heap);
    let report_heap = engine_heap.run(0.0);

    // 2. Run Dynamic MtfEngine
    let m1_feed_dyn = BarVecFeed::new(m1_data, "BTCUSDT".into());
    let h1_feed_dyn = BarVecFeed::new(h1_data, "BTCUSDT".into());

    let mut engine_dyn = PointerSyncMtfEngine::sync(
        10_000.0,
        MtfEmaRsiStrategy::new(),
        PercentEquity::fractional(0.95, 1),
        0.0,
        0.0,
    )
    .with_base_tf(Timeframe::M1)
    .with_single_entry();
    engine_dyn.add_feed(Timeframe::M1, m1_feed_dyn);
    engine_dyn.add_feed(Timeframe::H1, h1_feed_dyn);
    let report_dyn = engine_dyn.run(0.0);

    // 3. Assert parity
    assert_eq!(report_heap.total_trades, report_dyn.total_trades, "MtfEmaRsi: Total trades mismatch");
    assert_eq!(report_heap.gross_profit_usd, report_dyn.gross_profit_usd, "MtfEmaRsi: Gross profit mismatch");
    assert_eq!(report_heap.gross_loss_usd, report_dyn.gross_loss_usd, "MtfEmaRsi: Gross loss mismatch");
    assert_eq!(report_heap.win_rate_pct, report_dyn.win_rate_pct, "MtfEmaRsi: Win rate mismatch");

    let trades_heap = &engine_heap.core.portfolio.trades;
    let trades_dyn = &engine_dyn.core.portfolio.trades;
    assert_eq!(trades_heap.len(), trades_dyn.len(), "MtfEmaRsi: Trades count mismatch");
    for (t_heap, t_dyn) in trades_heap.iter().zip(trades_dyn.iter()) {
        assert_eq!(t_heap.entry_timestamp, t_dyn.entry_timestamp);
        assert_eq!(t_heap.exit_timestamp, t_dyn.exit_timestamp);
        assert_eq!(t_heap.entry_price, t_dyn.entry_price);
        assert_eq!(t_heap.exit_price, t_dyn.exit_price);
        assert_eq!(t_heap.pnl, t_dyn.pnl);
    }

    let eq_heap = &engine_heap.core.portfolio.equity_curve;
    let eq_dyn = &engine_dyn.core.portfolio.equity_curve;
    assert_eq!(eq_heap.len(), eq_dyn.len(), "MtfEmaRsi: Equity curve length mismatch");
    for (e_heap, e_dyn) in eq_heap.iter().zip(eq_dyn.iter()) {
        assert_eq!(e_heap.timestamp, e_dyn.timestamp);
        assert_eq!(e_heap.equity, e_dyn.equity);
    }
}

#[test]
fn test_real_data_ma_cross_parity() {
    let Some((m1_data, h1_data)) = load_real_m1_h1() else {
        return;
    };

    // 1. Run Heap MtfEngine
    let m1_feed_heap = BarVecFeed::new(m1_data.clone(), "BTCUSDT".into());
    let h1_feed_heap = BarVecFeed::new(h1_data.clone(), "BTCUSDT".into());

    let mut engine_heap = HeapMtfEngine::sync(
        10_000.0,
        MtfMaCrossStrategy::new(),
        PercentEquity::fractional(0.95, 1),
        0.0,
        0.0,
    )
    .with_base_tf(Timeframe::M1)
    .with_single_entry();
    engine_heap.add_feed(Timeframe::M1, m1_feed_heap);
    engine_heap.add_feed(Timeframe::H1, h1_feed_heap);
    let report_heap = engine_heap.run(0.0);

    // 2. Run Dynamic MtfEngine
    let m1_feed_dyn = BarVecFeed::new(m1_data, "BTCUSDT".into());
    let h1_feed_dyn = BarVecFeed::new(h1_data, "BTCUSDT".into());

    let mut engine_dyn = PointerSyncMtfEngine::sync(
        10_000.0,
        MtfMaCrossStrategy::new(),
        PercentEquity::fractional(0.95, 1),
        0.0,
        0.0,
    )
    .with_base_tf(Timeframe::M1)
    .with_single_entry();
    engine_dyn.add_feed(Timeframe::M1, m1_feed_dyn);
    engine_dyn.add_feed(Timeframe::H1, h1_feed_dyn);
    let report_dyn = engine_dyn.run(0.0);

    // 3. Assert parity
    assert_eq!(report_heap.total_trades, report_dyn.total_trades, "MtfMaCross: Total trades mismatch");
    assert_eq!(report_heap.gross_profit_usd, report_dyn.gross_profit_usd, "MtfMaCross: Gross profit mismatch");
    assert_eq!(report_heap.gross_loss_usd, report_dyn.gross_loss_usd, "MtfMaCross: Gross loss mismatch");
    assert_eq!(report_heap.win_rate_pct, report_dyn.win_rate_pct, "MtfMaCross: Win rate mismatch");

    let trades_heap = &engine_heap.core.portfolio.trades;
    let trades_dyn = &engine_dyn.core.portfolio.trades;
    assert_eq!(trades_heap.len(), trades_dyn.len(), "MtfMaCross: Trades count mismatch");
    for (t_heap, t_dyn) in trades_heap.iter().zip(trades_dyn.iter()) {
        assert_eq!(t_heap.entry_timestamp, t_dyn.entry_timestamp);
        assert_eq!(t_heap.exit_timestamp, t_dyn.exit_timestamp);
        assert_eq!(t_heap.entry_price, t_dyn.entry_price);
        assert_eq!(t_heap.exit_price, t_dyn.exit_price);
        assert_eq!(t_heap.pnl, t_dyn.pnl);
    }

    let eq_heap = &engine_heap.core.portfolio.equity_curve;
    let eq_dyn = &engine_dyn.core.portfolio.equity_curve;
    assert_eq!(eq_heap.len(), eq_dyn.len(), "MtfMaCross: Equity curve length mismatch");
    for (e_heap, e_dyn) in eq_heap.iter().zip(eq_dyn.iter()) {
        assert_eq!(e_heap.timestamp, e_dyn.timestamp);
        assert_eq!(e_heap.equity, e_dyn.equity);
    }
}
