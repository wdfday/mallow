//! Pointer Sync Multi-Timeframe (MTF) Engine
//!
//! Driven by the base timeframe feed, synchronizing higher timeframe (HTF) feeds
//! on-the-fly using an active cursor/pointer approach instead of a BinaryHeap.

use std::collections::{HashMap, VecDeque};

use alm_core::{
    exit::IntraBarMode,
    strategy::RiskManager,
    Bar, Timeframe,
};
use alm_data::BarFeed;
use alm_report::BacktestReport;
use tracing::debug;

use crate::core::EngineCore;
use crate::engine::ReversePolicy;

pub use alm_core::{MtfSnapshot, MtfStrategy, TfBarEvent, TfView};

const DEFAULT_WINDOW_SIZE: usize = 500;

struct HtfState {
    tf: Timeframe,
    feed: Box<dyn BarFeed>,
    window: VecDeque<Bar>,
    peeked: Option<Bar>,
}

/// Pointer Sync MTF backtesting engine.
pub struct PointerSyncMtfEngine<S: MtfStrategy, R: RiskManager> {
    pub core: EngineCore<R>,
    pub strategy: S,

    base_tf: Timeframe,
    base_feed: Option<Box<dyn BarFeed>>,
    htf_feeds: HashMap<Timeframe, HtfState>,
    window_size: usize,
}

impl<S: MtfStrategy, R: RiskManager> PointerSyncMtfEngine<S, R> {
    pub fn sync(
        initial_capital: f64,
        strategy: S,
        risk: R,
        commission_pct: f64,
        slippage_pct: f64,
    ) -> Self {
        Self {
            core: EngineCore::new(initial_capital, risk, commission_pct, slippage_pct),
            strategy,
            base_tf: Timeframe::M1,
            base_feed: None,
            htf_feeds: HashMap::new(),
            window_size: DEFAULT_WINDOW_SIZE,
        }
    }

    pub fn with_base_tf(mut self, tf: Timeframe) -> Self {
        self.base_tf = tf;
        self
    }

    pub fn with_window(mut self, size: usize) -> Self {
        self.window_size = size.max(1);
        self
    }

    pub fn with_intra_bar_mode(mut self, mode: IntraBarMode) -> Self {
        self.core.intra_bar_mode = mode;
        self
    }

    pub fn with_single_entry(mut self) -> Self {
        self.core.single_entry = true;
        self
    }

    pub fn with_reverse_policy(mut self, policy: ReversePolicy) -> Self {
        self.core.reverse_policy = Some(policy);
        self
    }

    pub fn with_pyramiding(mut self, max_units: usize, max_position_pct: f64) -> Self {
        self.core.max_units = max_units.max(1);
        self.core.max_position_pct = max_position_pct.max(0.0);
        self
    }

    pub fn with_independent_legs(mut self) -> Self {
        self.core.pyramid_merge = false;
        self
    }

    pub fn with_warmup_until(mut self, until: i64) -> Self {
        self.core.warmup_until = if until > 0 { Some(until) } else { None };
        self
    }

    pub fn add_feed(&mut self, tf: Timeframe, feed: impl BarFeed + 'static) {
        if tf == self.base_tf {
            self.base_feed = Some(Box::new(feed));
        } else {
            self.htf_feeds.insert(tf, HtfState {
                tf,
                feed: Box::new(feed),
                window: VecDeque::new(),
                peeked: None,
            });
        }
    }

    pub fn run(&mut self, risk_free_annual: f64) -> BacktestReport {
        let mut base_feed = self.base_feed.take().expect("base feed must be registered");
        let symbol = base_feed.symbol().to_string();
        let strategy_name = self.strategy.name().to_string();
        self.core.metrics_strategy = strategy_name.clone();

        let mut base_window: VecDeque<Bar> = VecDeque::new();
        let mut bar_count: usize = 0;

        while let Some(base_bar) = base_feed.next() {
            bar_count += 1;
            let base_close_ts = base_bar.timestamp.saturating_add(self.base_tf.duration_ms());

            // 1. Advance each HTF feed to sync with the current base_close_ts
            let mut htf_just_closed: HashMap<Timeframe, bool> = HashMap::new();
            for htf in self.htf_feeds.values_mut() {
                let htf_duration = htf.tf.duration_ms();
                let mut closed = false;

                loop {
                    if htf.peeked.is_none() {
                        htf.peeked = htf.feed.next();
                    }

                    if let Some(peeked_bar) = &htf.peeked {
                        let htf_close_ts = peeked_bar.timestamp.saturating_add(htf_duration);
                        if htf_close_ts <= base_close_ts {
                            let bar = htf.peeked.take().unwrap();
                            push_bounded(&mut htf.window, bar, self.window_size);
                            closed = true;
                        } else {
                            break;
                        }
                    } else {
                        break;
                    }
                }
                htf_just_closed.insert(htf.tf, closed);
            }

            // Push base bar to base window
            push_bounded(&mut base_window, base_bar.clone(), self.window_size);

            // 2. Build events list
            let mut events: Vec<TfBarEvent<'_>> = Vec::new();
            // Base TF closes at this tick by definition
            events.push(TfBarEvent { tf: self.base_tf, bar: &base_bar });

            for htf in self.htf_feeds.values() {
                if *htf_just_closed.get(&htf.tf).unwrap_or(&false) {
                    if let Some(bar) = htf.window.back() {
                        events.push(TfBarEvent { tf: htf.tf, bar });
                    }
                }
            }

            // Sort events by TF size (largest first)
            events.sort_by_key(|e| std::cmp::Reverse(e.tf.duration_ms()));

            // 3. Build views mapping
            let mut views: HashMap<Timeframe, TfView<'_>> = HashMap::new();
            views.insert(self.base_tf, TfView { tf: self.base_tf, confirmed: &base_window });
            for htf in self.htf_feeds.values() {
                views.insert(htf.tf, TfView { tf: htf.tf, confirmed: &htf.window });
            }

            let snap = MtfSnapshot {
                base_tf: self.base_tf,
                close_ts: base_close_ts,
                events: &events,
                views: &views,
            };

            // 4. Determine if this tick is in trading territory
            let is_trading = match self.core.warmup_until {
                Some(until) => base_bar.timestamp >= until,
                None => true,
            };

            // 5. Per-base-bar housekeeping
            if is_trading {
                self.core.run_base_tick(&base_bar);
            } else {
                self.core.risk.on_bar(&base_bar);
            }

            // 6. Update regime from strategy
            let regime = self.strategy.current_regime().cloned();
            self.core.update_regime(base_bar.timestamp, regime);

            // 7. Call strategy on bars
            let signals = self.strategy.on_bars(snap);

            // 8. Dispatch signals
            if is_trading {
                self.core.dispatch_signals(signals);
            }
        }

        // Force-close at last base-bar close
        if let Some(last_bar) = base_window.back().cloned() {
            self.core.force_close_all(&last_bar);
        }

        let report = self.core.build_report(&strategy_name, &symbol, risk_free_annual);
        debug!(
            symbol = %symbol,
            strategy = %strategy_name,
            bars = bar_count,
            tfs = self.htf_feeds.len() + 1,
            trades = report.total_trades,
            "pointer sync mtf backtest complete",
        );
        report
    }
}

#[inline]
fn push_bounded<T>(buf: &mut VecDeque<T>, v: T, cap: usize) {
    buf.push_back(v);
    while buf.len() > cap {
        buf.pop_front();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_data::BarVecFeed;
    use crate::risk::FixedFractional;
    use crate::heap_mtf_engine::HeapMtfEngine;
    use alm_core::signal::Signal;

    fn mk_bar(ts: i64, c: f64, sym: &str) -> Bar {
        Bar::new(ts, sym, c, c + 0.05, c - 0.05, c, 10.0)
    }

    struct MtfTrend {
        symbol: String,
    }

    impl MtfStrategy for MtfTrend {
        fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
            if !snap.just_closed(Timeframe::H1) {
                return vec![];
            }
            let h1 = snap.get(Timeframe::H1).unwrap();
            if h1.len() < 2 {
                return vec![];
            }
            let last = h1.back().unwrap();
            let prev = h1.iter().rev().nth(1).unwrap();
            let ts = snap.base_bar().map(|b| b.timestamp).unwrap_or(snap.close_ts);
            if last.close > prev.close {
                vec![Signal::long(ts, &self.symbol, 1.0)]
            } else {
                vec![Signal::exit(ts, &self.symbol)]
            }
        }
        fn name(&self) -> &str {
            "mtf_trend"
        }
        fn reset(&mut self) {}
    }

    fn m1_uptrend(n: usize) -> Vec<Bar> {
        (0..n as i64)
            .map(|i| mk_bar(i * 60_000, 100.0 + i as f64 * 0.1, "TEST"))
            .collect()
    }

    fn h1_uptrend(n: usize) -> Vec<Bar> {
        (0..n as i64)
            .map(|i| mk_bar(i * 3_600_000, 100.0 + i as f64 * 6.0, "TEST"))
            .collect()
    }

    #[test]
    fn test_engines_parity() {
        let m1_data_1 = m1_uptrend(4 * 60);
        let m1_data_2 = m1_data_1.clone();

        let h1_data_1 = h1_uptrend(4);
        let h1_data_2 = h1_data_1.clone();

        // 1. Run original HeapMtfEngine
        let m1_feed_heap = BarVecFeed::new(m1_data_1, "TEST".into());
        let h1_feed_heap = BarVecFeed::new(h1_data_1, "TEST".into());

        let mut engine_heap = HeapMtfEngine::sync(
            10_000.0,
            MtfTrend { symbol: "TEST".into() },
            FixedFractional::fractional(0.95, 1),
            0.0,
            0.0,
        )
        .with_base_tf(Timeframe::M1)
        .with_single_entry();
        engine_heap.add_feed(Timeframe::M1, m1_feed_heap);
        engine_heap.add_feed(Timeframe::H1, h1_feed_heap);
        let report_heap = engine_heap.run(0.0);

        // 2. Run new PointerSyncMtfEngine
        let m1_feed_dyn = BarVecFeed::new(m1_data_2, "TEST".into());
        let h1_feed_dyn = BarVecFeed::new(h1_data_2, "TEST".into());

        let mut engine_dyn = PointerSyncMtfEngine::sync(
            10_000.0,
            MtfTrend { symbol: "TEST".into() },
            FixedFractional::fractional(0.95, 1),
            0.0,
            0.0,
        )
        .with_base_tf(Timeframe::M1)
        .with_single_entry();
        engine_dyn.add_feed(Timeframe::M1, m1_feed_dyn);
        engine_dyn.add_feed(Timeframe::H1, h1_feed_dyn);
        let report_dyn = engine_dyn.run(0.0);

        // 3. Assert Parity
        assert_eq!(report_heap.total_trades, report_dyn.total_trades, "Total trades parity mismatch");
        assert_eq!(report_heap.gross_profit_usd, report_dyn.gross_profit_usd, "Gross profit parity mismatch");
        assert_eq!(report_heap.win_rate_pct, report_dyn.win_rate_pct, "Win rate parity mismatch");

        // Compare trades
        let trades_heap = &engine_heap.core.portfolio.trades;
        let trades_dyn = &engine_dyn.core.portfolio.trades;
        assert_eq!(trades_heap.len(), trades_dyn.len(), "Trades count mismatch");
        for (t_heap, t_dyn) in trades_heap.iter().zip(trades_dyn.iter()) {
            assert_eq!(t_heap.entry_timestamp, t_dyn.entry_timestamp, "Trade entry timestamp mismatch");
            assert_eq!(t_heap.exit_timestamp, t_dyn.exit_timestamp, "Trade exit timestamp mismatch");
            assert_eq!(t_heap.entry_price, t_dyn.entry_price, "Trade entry price mismatch");
            assert_eq!(t_heap.exit_price, t_dyn.exit_price, "Trade exit price mismatch");
            assert_eq!(t_heap.pnl, t_dyn.pnl, "Trade PnL mismatch");
        }

        // Compare equity curve
        let eq_heap = &engine_heap.core.portfolio.equity_curve;
        let eq_dyn = &engine_dyn.core.portfolio.equity_curve;
        assert_eq!(eq_heap.len(), eq_dyn.len(), "Equity curve length mismatch");
        for (e_heap, e_dyn) in eq_heap.iter().zip(eq_dyn.iter()) {
            assert_eq!(e_heap.timestamp, e_dyn.timestamp, "Equity timestamp mismatch");
            assert_eq!(e_heap.equity, e_dyn.equity, "Equity value mismatch");
        }
    }
}
