//! Heap-based Multi-Timeframe (MTF) Engine
//!
//! Single symbol, multiple independent feeds — one per timeframe. No
//! resampling: each TF stream is the ground truth for that TF.
//!
//! Time-merges all TF feeds using a min-heap so that when a base bar
//! fires, the strategy already sees the latest confirmed HTF bars in its
//! [`MtfSnapshot`]. All per-bar housekeeping (fills, exit rules, equity,
//! sizing, pyramiding, regime) is identical and handled by the embedded
//! [`EngineCore`].

use std::cmp::Ordering;
use std::collections::{BinaryHeap, HashMap, VecDeque};

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

// Re-export the public interface types from alm-core so existing import paths keep working.
pub use alm_core::{MtfSnapshot, MtfStrategy, TfBarEvent, TfView};

const DEFAULT_WINDOW_SIZE: usize = 500;

// ── Heap entry ───────────────────────────────────────────────────────────────

/// Time-merge key for the BinaryHeap.
///
/// Sorts primary on bar close-time (smallest first), secondary on TF size
/// (LARGEST first) so that a just-closed HTF bar is pushed into its window
/// before the simultaneously-closed base-TF bar fires the strategy.
#[derive(Debug)]
struct HeapEntry {
    close_ts: i64,
    tf_ms: i64,
    tf: Timeframe,
    bar: Bar,
}

impl PartialEq for HeapEntry {
    fn eq(&self, other: &Self) -> bool {
        self.close_ts == other.close_ts && self.tf_ms == other.tf_ms
    }
}
impl Eq for HeapEntry {}

impl Ord for HeapEntry {
    fn cmp(&self, other: &Self) -> Ordering {
        // BinaryHeap pops the GREATEST element. We want:
        //   1. smallest close_ts first  → invert
        //   2. on tie, LARGEST tf_ms first → natural order
        other
            .close_ts
            .cmp(&self.close_ts)
            .then_with(|| self.tf_ms.cmp(&other.tf_ms))
    }
}
impl PartialOrd for HeapEntry {
    fn partial_cmp(&self, other: &Self) -> Option<Ordering> {
        Some(self.cmp(other))
    }
}

// ── HeapMtfEngine ────────────────────────────────────────────────────────────

/// Heap-based multi-timeframe backtesting engine. Single symbol; one base TF + N HTFs.
///
/// Embeds [`EngineCore`] for all per-bar housekeeping. The only MTF-specific
/// logic is the heap-based feed merging and building an [`MtfSnapshot`] for
/// `strategy.on_bars`.
pub struct HeapMtfEngine<S: MtfStrategy, R: RiskManager> {
    pub core: EngineCore<R>,
    pub strategy: S,

    base_tf: Timeframe,
    feeds: HashMap<Timeframe, Box<dyn BarFeed>>,
    heap: BinaryHeap<HeapEntry>,
    windows: HashMap<Timeframe, VecDeque<Bar>>,
    window_size: usize,
}

// ── Constructors / builder ───────────────────────────────────────────────────

impl<S: MtfStrategy, R: RiskManager> HeapMtfEngine<S, R> {
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
            feeds: HashMap::new(),
            heap: BinaryHeap::new(),
            windows: HashMap::new(),
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

    // ── Shared builder methods (delegate to core) ─────────────────────────

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

    /// Register one feed per timeframe. Calling twice for the same `tf`
    /// replaces the previous feed (and clears its window).
    pub fn add_feed(&mut self, tf: Timeframe, feed: impl BarFeed + 'static) {
        self.feeds.insert(tf, Box::new(feed));
        self.windows.entry(tf).or_insert_with(VecDeque::new).clear();
    }
}

// ── Run loop ─────────────────────────────────────────────────────────────────

impl<S: MtfStrategy, R: RiskManager> HeapMtfEngine<S, R> {
    pub fn run(&mut self, risk_free_annual: f64) -> BacktestReport {
        let symbol = self
            .feeds
            .get(&self.base_tf)
            .map(|f| f.symbol().to_string())
            .unwrap_or_default();
        let strategy_name = self.strategy.name().to_string();

        if !self.feeds.contains_key(&self.base_tf) {
            tracing::warn!(
                base_tf = ?self.base_tf,
                "mtf: no feed registered for base TF — report will be empty",
            );
        }

        self.core.metrics_strategy = strategy_name.clone();

        // Prime heap with the first bar from each registered feed.
        let tfs: Vec<Timeframe> = self.feeds.keys().copied().collect();
        for tf in tfs {
            self.refill(tf);
        }

        let mut bar_count: usize = 0;
        while let Some(top_close_ts) = self.heap.peek().map(|e| e.close_ts) {
            // 1. Collect all bars sharing this close_ts (usually 1, occasionally
            //    2-3 at HTF boundaries). Heap is sorted largest-TF-first on ties,
            //    so HTF pops before base.
            let mut batch: Vec<HeapEntry> = Vec::new();
            while let Some(top) = self.heap.peek() {
                if top.close_ts == top_close_ts {
                    batch.push(self.heap.pop().expect("just peeked"));
                } else {
                    break;
                }
            }
            bar_count += batch.len();

            // 2. Refill each TF that just popped.
            for entry in &batch {
                self.refill(entry.tf);
            }

            // 3. Push bars into windows (HTF first → strategy sees them immediately).
            for entry in &batch {
                let win = self.windows.entry(entry.tf).or_insert_with(VecDeque::new);
                push_bounded(win, entry.bar.clone(), self.window_size);
            }

            // 4. Determine if this tick is in trading territory.
            let base_bar_idx = batch.iter().position(|e| e.tf == self.base_tf);
            let is_trading = match (self.core.warmup_until, base_bar_idx) {
                (Some(until), Some(idx)) => batch[idx].bar.timestamp >= until,
                (Some(_), None) => false, // HTF-only tick → never trade
                (None, _) => true,
            };

            // 5. Per-base-bar housekeeping (fills, exits, equity).
            if is_trading {
                if let Some(idx) = base_bar_idx {
                    let base_bar = batch[idx].bar.clone();
                    self.core.run_base_tick(&base_bar);
                }
            } else if let Some(idx) = base_bar_idx {
                // Warm-up: advance risk indicator only.
                self.core.risk.on_bar(&batch[idx].bar);
            }

            // 6. Build snapshot and call strategy.on_bars.
            let signals = {
                let mut views: HashMap<Timeframe, TfView<'_>> = HashMap::new();
                for (&tf, window) in &self.windows {
                    views.insert(tf, TfView { tf, confirmed: window });
                }
                let events: Vec<TfBarEvent<'_>> = batch
                    .iter()
                    .map(|e| TfBarEvent { tf: e.tf, bar: &e.bar })
                    .collect();
                let snap = MtfSnapshot {
                    base_tf: self.base_tf,
                    close_ts: top_close_ts,
                    events: &events,
                    views: &views,
                };
                // Regime update from strategy.
                let regime = self.strategy.current_regime().cloned();
                if let Some(idx) = base_bar_idx {
                    self.core.update_regime(batch[idx].bar.timestamp, regime);
                }
                self.strategy.on_bars(snap)
            };

            // 7. Dispatch signals (suppressed during warm-up).
            if is_trading {
                self.core.dispatch_signals(signals);
            }
        }

        // End-of-data: force-close at last base-bar close.
        let last_base_bar = self
            .windows
            .get(&self.base_tf)
            .and_then(|w| w.back().cloned());
        if let Some(last_bar) = last_base_bar {
            self.core.force_close_all(&last_bar);
        }

        let report = self.core.build_report(&strategy_name, &symbol, risk_free_annual);
        debug!(
            symbol = %symbol,
            strategy = %strategy_name,
            bars = bar_count,
            tfs = self.feeds.len(),
            trades = report.total_trades,
            "heap mtf backtest complete",
        );
        report
    }

    /// Pull the next bar from `tf`'s feed and push it onto the heap.
    fn refill(&mut self, tf: Timeframe) {
        let next = self.feeds.get_mut(&tf).and_then(|f| f.next());
        if let Some(bar) = next {
            let tf_ms = tf.duration_ms();
            let close_ts = bar.timestamp.saturating_add(tf_ms);
            self.heap.push(HeapEntry { close_ts, tf_ms, tf, bar });
        }
    }
}

// ── Helpers ──────────────────────────────────────────────────────────────────

#[inline]
fn push_bounded<T>(buf: &mut VecDeque<T>, v: T, cap: usize) {
    buf.push_back(v);
    while buf.len() > cap {
        buf.pop_front();
    }
}

// ── Tests ────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_data::BarVecFeed;
    use crate::risk::FixedFractional;
    use alm_core::signal::Signal;

    fn mk_bar(ts: i64, c: f64, sym: &str) -> Bar {
        Bar::new(ts, sym, c, c + 0.05, c - 0.05, c, 10.0)
    }

    #[test]
    fn heap_pops_earliest_close_first_then_larger_tf_first_on_tie() {
        let mut h: BinaryHeap<HeapEntry> = BinaryHeap::new();
        h.push(HeapEntry {
            close_ts: 12 * 60 * 60_000,
            tf_ms: 60_000,
            tf: Timeframe::M1,
            bar: mk_bar(11 * 60 * 60_000 + 59 * 60_000, 1.0, "X"),
        });
        h.push(HeapEntry {
            close_ts: 12 * 60 * 60_000,
            tf_ms: 3_600_000,
            tf: Timeframe::H1,
            bar: mk_bar(11 * 60 * 60_000, 2.0, "X"),
        });
        h.push(HeapEntry {
            close_ts: 10 * 60 * 60_000 + 31 * 60_000,
            tf_ms: 60_000,
            tf: Timeframe::M1,
            bar: mk_bar(10 * 60 * 60_000 + 30 * 60_000, 3.0, "X"),
        });

        let first = h.pop().unwrap();
        assert_eq!(first.tf, Timeframe::M1);
        assert_eq!(first.bar.close, 3.0, "earliest close_ts wins");

        let second = h.pop().unwrap();
        assert_eq!(second.tf, Timeframe::H1, "larger TF pops first on tied close_ts");
        let third = h.pop().unwrap();
        assert_eq!(third.tf, Timeframe::M1);
    }

    struct MtfTrend { symbol: String }

    impl MtfStrategy for MtfTrend {
        fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
            if !snap.just_closed(Timeframe::H1) { return vec![]; }
            let h1 = snap.get(Timeframe::H1).unwrap();
            if h1.len() < 2 { return vec![]; }
            let last = h1.back().unwrap();
            let prev = h1.iter().rev().nth(1).unwrap();
            let ts = snap.base_bar().map(|b| b.timestamp).unwrap_or(snap.close_ts);
            if last.close > prev.close {
                vec![Signal::long(ts, &self.symbol, 1.0)]
            } else {
                vec![Signal::exit(ts, &self.symbol)]
            }
        }
        fn name(&self) -> &str { "mtf_trend" }
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
    fn run_with_two_independent_feeds() {
        let m1 = BarVecFeed::new(m1_uptrend(4 * 60), "TEST".into());
        let h1 = BarVecFeed::new(h1_uptrend(4), "TEST".into());

        let mut engine = HeapMtfEngine::sync(
            10_000.0,
            MtfTrend { symbol: "TEST".into() },
            FixedFractional::fractional(0.95, 1),
            0.0,
            0.0,
        )
        .with_base_tf(Timeframe::M1)
        .with_single_entry();
        engine.add_feed(Timeframe::M1, m1);
        engine.add_feed(Timeframe::H1, h1);

        let report = engine.run(0.0);
        assert_eq!(engine.core.portfolio.equity_curve.len(), 240);
        assert!(report.total_trades >= 1, "expected ≥1 trade in uptrend");
    }
}
