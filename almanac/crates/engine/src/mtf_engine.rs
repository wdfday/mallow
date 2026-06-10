//! Multi-Timeframe (MTF) Engine — DRAFT
//!
//! Status: experimental. Coexists with [`Engine`] and [`MultiEngine`]; neither
//! is touched. Single symbol, **multiple independent feeds — one per
//! timeframe**. No resampling: each TF stream is the ground truth for that
//! TF, read straight from its own bus (typically its own parquet file).
//!
//! # Why "no resampling"
//!
//! The previous resampler-based design rebuilt HTF bars on the fly from base
//! TF bars. That has three problems:
//!
//! 1. **Disagreement with the source of truth.** Exchanges publish their own
//!    H1 / H4 candles and traders write strategies against those exact OHLCV
//!    values. Aggregating M1 → H1 in-engine can drift (missing bars, dedup,
//!    out-of-order ticks) and produce different highs/lows than the upstream.
//! 2. **Indicator state cannot warm up off pre-rolled HTF history.** Without
//!    a real HTF feed, the only HTF history you have is whatever the
//!    resampler produced after engine start — useless for long-period
//!    indicators (200-period D1 EMA on a 2-month M1 backtest is empty).
//! 3. **Forces M1 granularity onto every backtest.** Pure HTF strategies
//!    (e.g., D1 trend-follow) shouldn't have to load 100k M1 bars to feed a
//!    resampler when one D1 file would do.
//!
//! `MtfEngine` flips the model: every TF you care about is a first-class
//! [`BarFeed`]. The engine time-merges them through a min-heap keyed on
//! **bar close time** so a strategy on bar `base_t` always sees confirmed
//! HTF history up to and including the bar that *also* just closed at
//! `base_t`.
//!
//! # Bar timestamp convention
//!
//! A bar's `timestamp` is its bucket-open. Its close time is
//! `bar.timestamp + tf.duration_ms()`. The heap key is the **close time** so
//! that:
//!
//! - M1@11:59 (close = 12:00:00) and H1@11:00 (close = 12:00:00) tie.
//! - On a tie, the **larger TF pops first** — pushing the just-closed H1 into
//!   its window so the base-TF strategy callback at M1@11:59 already sees
//!   the new H1.
//! - That is not look-ahead: both bars are fully formed at the same physical
//!   moment, and orders submitted by the strategy fill on the NEXT base bar's
//!   open. The information set at decision time is honest.
//!
//! # Event flow per base-TF pop
//!
//! ```text
//! base_bar arrives at t (close_ts = t + base_tf_ms)
//!   ├── broker.process_pending(bar)  → Fill events    (pending from prev bar)
//!   ├── drain Fill events            → apply_fill
//!   ├── push to windows[base_tf]
//!   ├── signal-level exit check on base bar OHLC → force_close
//!   ├── record_equity (AFTER exits)  → EquityEvent
//!   ├── strategy.on_bar(MtfSnapshot)  → SignalEvent
//!   └── signal → risk.size → OrderEvent → broker.submit  (fills next bar)
//! ```
//!
//! For HTF pops, only `windows[tf]` is updated; no strategy callback fires.
//! The strategy reads HTF history when it next runs (on a base bar).
//!
//! # Components reused
//!
//! [`Portfolio`], [`SimBroker`], [`PositionTracker`],
//! [`RiskManager`], [`BacktestReport`].

use std::cmp::Ordering;
use std::collections::{BinaryHeap, HashMap, VecDeque};

use alm_core::{
    bus::EventBus,
    event::{EquityEvent, Event, FillEvent},
    exit::{ExitReason, IntraBarMode, PositionTracker},
    order::{OrderRequest, Side},
    portfolio::Portfolio,
    signal::{Direction, Signal},
    strategy::RiskManager,
    Bar, Timeframe,
};
use alm_data::BarFeed;
use alm_report::BacktestReport;
use tracing::debug;

use crate::broker::SimBroker;
use crate::bus_sync::SyncBus;

// Re-export the public interface types that live in alm-core so existing
// import paths (`alm_engine::mtf_engine::{MtfStrategy, …}`) keep working.
pub use alm_core::{MtfSnapshot, MtfStrategy, TfBarEvent, TfView};

const DEFAULT_WINDOW_SIZE: usize = 500;

// ── Heap entry ───────────────────────────────────────────────────────────────

/// Time-merge key.
///
/// Sorts primary on bar close-time (smaller first), secondary on TF size
/// (LARGER first) so that a just-closed HTF bar is pushed into its window
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
        // BinaryHeap pops the GREATEST element. We want pop order:
        //   1. smallest close_ts first
        //   2. on tie, LARGEST tf_ms first
        // So "greatest" = "smallest close_ts" (invert) then "largest tf_ms".
        other.close_ts.cmp(&self.close_ts)
            .then_with(|| self.tf_ms.cmp(&other.tf_ms))
    }
}
impl PartialOrd for HeapEntry {
    fn partial_cmp(&self, other: &Self) -> Option<Ordering> {
        Some(self.cmp(other))
    }
}

// ── MtfEngine ────────────────────────────────────────────────────────────────

/// Multi-timeframe backtesting engine. Single symbol; one base TF + N HTFs.
/// Each TF is fed from its own [`BarFeed`].
pub struct MtfEngine<S: MtfStrategy, R: RiskManager> {
    pub portfolio: Portfolio,
    pub broker: SimBroker,
    pub strategy: S,
    pub risk: R,
    bus: SyncBus,

    base_tf: Timeframe,

    feeds: HashMap<Timeframe, Box<dyn BarFeed>>,
    heap: BinaryHeap<HeapEntry>,

    windows: HashMap<Timeframe, VecDeque<Bar>>,
    window_size: usize,

    last_price: f64,
    intra_bar_mode: IntraBarMode,
    position_trackers: HashMap<String, PositionTracker>,
    pending_signal_levels: HashMap<String, (Option<f64>, Option<f64>, Option<f64>, Option<usize>, bool)>,
    single_entry: bool,
    /// Warm-up boundary (Unix ms, base-bar open). Base bars before this only warm
    /// indicators (via `on_bars`); no trading / equity. `None` = trade from the start.
    warmup_until: Option<i64>,
}

// ── Constructors / builder ───────────────────────────────────────────────────

impl<S: MtfStrategy, R: RiskManager> MtfEngine<S, R> {
    pub fn sync(
        initial_capital: f64,
        strategy: S,
        risk: R,
        commission_pct: f64,
        slippage_pct: f64,
    ) -> Self {
        Self {
            portfolio: Portfolio::new(initial_capital),
            broker: SimBroker::new(commission_pct, slippage_pct),
            strategy,
            risk,
            bus: SyncBus::new(),
            base_tf: Timeframe::M1,
            feeds: HashMap::new(),
            heap: BinaryHeap::new(),
            windows: HashMap::new(),
            window_size: DEFAULT_WINDOW_SIZE,
            last_price: 0.0,
            intra_bar_mode: IntraBarMode::default(),
            position_trackers: HashMap::new(),
            pending_signal_levels: HashMap::new(),
            single_entry: false,
            warmup_until: None,
        }
    }

    /// Warm indicators on base bars before `until` (Unix ms, bar OPEN) without trading.
    /// Critical for HTF: an HTF indicator needs many base bars of history to converge,
    /// so the report would otherwise start with cold HTF state. `on_bars` still runs
    /// during warm-up (advancing every TF's indicator state); only trading/equity is gated.
    pub fn with_warmup_until(mut self, until: i64) -> Self {
        self.warmup_until = if until > 0 { Some(until) } else { None };
        self
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
        self.intra_bar_mode = mode;
        self
    }

    pub fn with_single_entry(mut self) -> Self {
        self.single_entry = true;
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

impl<S: MtfStrategy, R: RiskManager> MtfEngine<S, R> {
    pub fn run(&mut self, risk_free_annual: f64) -> BacktestReport {
        // Ensure base TF has a feed; otherwise we have no driving series.
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

        // Prime heap with the first bar from each registered feed.
        let tfs: Vec<Timeframe> = self.feeds.keys().copied().collect();
        for tf in tfs {
            self.refill(tf);
        }

        let mut bar_count: usize = 0;
        while let Some(top_close_ts) = self.heap.peek().map(|e| e.close_ts) {
            // 1. Gom batch: tất cả bars cùng close_ts (đa số chỉ 1, đôi khi 2-3
            //    tại HTF boundaries). Heap đã sort largest-tf-first trên tie nên
            //    pop tuần tự sẽ ra largest TF trước.
            let mut batch: Vec<HeapEntry> = Vec::new();
            while let Some(top) = self.heap.peek() {
                if top.close_ts == top_close_ts {
                    batch.push(self.heap.pop().expect("just peeked"));
                } else {
                    break;
                }
            }
            bar_count += batch.len();

            // 2. Refill các TF vừa pop để heap chứa "what comes next".
            for entry in &batch {
                self.refill(entry.tf);
            }

            // 3. Push từng bar vào window của TF tương ứng (largest first
            //    nên HTF vào trước base — strategy sẽ thấy HTF mới trong views).
            for entry in &batch {
                let win = self.windows.entry(entry.tf).or_insert_with(VecDeque::new);
                push_bounded(win, entry.bar.clone(), self.window_size);
            }

            // 4. Nếu base TF có trong batch: dispatch full event chain.
            //    Nếu không (HTF-only tick — hiếm khi feeds aligned, chỉ xảy ra
            //    khi base feed có gap): bỏ qua broker/exit/equity, chỉ gọi strategy.
            // Warm-up: base bars whose OPEN < warmup_until only advance indicator
            // state (step 5 `on_bars` below) — no fills/exits/equity, no trading.
            // Essential for HTF, whose indicators need a long base history to converge.
            let base_bar_idx = batch.iter().position(|e| e.tf == self.base_tf);
            let is_trading = match (self.warmup_until, base_bar_idx) {
                (Some(until), Some(idx)) => batch[idx].bar.timestamp >= until,
                (Some(_), None)          => false, // HTF-only tick never trades
                (None, _)                => true,
            };
            if is_trading {
                if let Some(idx) = base_bar_idx {
                    let base_bar = batch[idx].bar.clone();
                    self.run_base_tick(&base_bar);
                }
            }

            // 5. Build snapshot và gọi strategy.on_bars (sau khi state đã cập nhật).
            let mut views: HashMap<Timeframe, TfView<'_>> = HashMap::new();
            for (&tf, window) in &self.windows {
                views.insert(tf, TfView { tf, confirmed: window });
            }
            let events: Vec<TfBarEvent<'_>> = batch
                .iter()
                .map(|e| TfBarEvent { tf: e.tf, bar: &e.bar })
                .collect();
            let signals = {
                let snap = MtfSnapshot {
                    base_tf: self.base_tf,
                    close_ts: top_close_ts,
                    events: &events,
                    views: &views,
                };
                self.strategy.on_bars(snap)
            };

            // 6. Emit signals → orders → broker (suppressed during warm-up).
            if is_trading {
                for sig in signals {
                    self.bus
                        .send(Event::Signal(alm_core::event::SignalEvent { signal: sig }));
                }
                while let Some(ev) = self.bus.try_recv() {
                    self.dispatch(ev);
                }
            }
        }

        // End-of-data force-close at last base-TF bar's close.
        let last_base_bar = self
            .windows
            .get(&self.base_tf)
            .and_then(|w| w.back().cloned());
        if let Some(last_bar) = last_base_bar {
            let open: Vec<(String, f64)> = self
                .portfolio
                .positions
                .values()
                .filter(|p| p.qty.abs() > f64::EPSILON)
                .map(|p| (p.symbol.clone(), p.qty))
                .collect();
            for (sym, qty) in open {
                let side = if qty > 0.0 { Side::Sell } else { Side::Buy };
                let tracker = self.position_trackers.remove(&sym);
                let fill = self.broker.force_close(
                    &sym,
                    qty.abs(),
                    side,
                    last_bar.timestamp,
                    last_bar.close,
                );
                self.portfolio.apply_fill(&fill);
                if let Some(tr) = tracker {
                    if let Some(trade) = self.portfolio.trades.last_mut() {
                        trade.mae_pct = tr.mae;
                        trade.mfe_pct = tr.mfe;
                        trade.bars_held = tr.bars_held;
                        trade.exit_reason = ExitReason::EndOfData;
                    }
                }
            }
        }

        let report = BacktestReport::generate(
            self.strategy.name(),
            &symbol,
            &self.portfolio,
            risk_free_annual,
        );
        debug!(
            symbol = %symbol,
            strategy = %strategy_name,
            bars = bar_count,
            tfs = self.feeds.len(),
            trades = report.total_trades,
            "mtf backtest complete",
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

    /// Per-base-bar housekeeping: broker fills, exit rules, equity. Called
    /// from the heap loop *before* `strategy.on_bars` so that the snapshot
    /// sees post-fill / post-exit state. Strategy-emitted signals are
    /// drained AFTER this returns.
    fn run_base_tick(&mut self, bar: &Bar) {
        self.last_price = bar.close;
        self.risk.on_bar(bar);

        // 1. Fill pending market orders at this bar's open.
        let fills = self.broker.process_pending(bar);
        let fill_count = fills.len();
        for fill in fills {
            self.bus.send(Event::Fill(FillEvent { fill }));
        }
        let mut guard = fill_count + 2;
        while let Some(early) = self.bus.try_recv() {
            if guard == 0 {
                self.bus.send(early);
                break;
            }
            guard -= 1;
            match early {
                Event::Fill(_) | Event::Equity(_) => self.dispatch(early),
                other => {
                    self.bus.send(other);
                    break;
                }
            }
        }

        // 2. Exit rules (BEFORE record_equity).
        let to_close: Vec<(String, f64, ExitReason)> = self
            .position_trackers
            .iter_mut()
            .filter_map(|(sym, tr)| {
                tr.update_and_check(
                    bar.open, bar.high, bar.low, bar.close, self.intra_bar_mode,
                )
                .map(|(fp, r)| (sym.clone(), fp, r))
            })
            .collect();
        for (sym, fp, reason) in to_close {
            let tracker = self.position_trackers.remove(&sym);
            self.broker.cancel_for_symbol(&sym);
            if let Some(pos) = self.portfolio.positions.get(&sym) {
                let qty = pos.qty.abs();
                let side = if pos.qty > 0.0 { Side::Sell } else { Side::Buy };
                let fill = self.broker.force_close(&sym, qty, side, bar.timestamp, fp);
                self.portfolio.apply_fill(&fill);
                self.last_price = fp;
                if let Some(tr) = tracker {
                    if let Some(trade) = self.portfolio.trades.last_mut() {
                        trade.mae_pct = tr.mae;
                        trade.mfe_pct = tr.mfe;
                        trade.bars_held = tr.bars_held;
                        trade.exit_reason = reason;
                    }
                }
            }
        }

        // 3. Record equity (post-exit).
        let prices = std::iter::once((bar.symbol.clone(), self.last_price))
            .collect::<HashMap<_, _>>();
        self.portfolio.record_equity(bar.timestamp, &prices);
        let eq = self.portfolio.equity(&prices);
        self.bus.send(Event::Equity(EquityEvent {
            timestamp: bar.timestamp,
            equity: eq,
            cash: self.portfolio.cash,
        }));
    }

    /// Handle Signal / Order / Fill / Equity events emitted into the bus
    /// after the strategy returns. Market events are no longer dispatched
    /// here — the heap loop handles bar processing inline via run_base_tick.
    fn dispatch(&mut self, event: Event) {
        match event {
            Event::Market(_) => {
                // Should not occur — bars are processed inline in the heap loop.
                debug!("unexpected Market event in dispatch — ignoring");
            }
            Event::Signal(ref sig_event) => self.handle_signal(&sig_event.signal),
            Event::Order(ref order_event) => {
                self.broker.submit(order_event.order.clone());
            }
            Event::Fill(ref fill_event) => self.handle_fill(&fill_event.fill),
            Event::Equity(_) => {
                // No-op in backtest: the equity curve is built directly by
                // `portfolio.record_equity()`. The event is emitted as an
                // extension hook for a live bus whose subscribers (dashboards,
                // realtime feeds) tap equity snapshots — SyncBus has none.
            }
        }
    }

    fn handle_signal(&mut self, signal: &Signal) {
        match signal.direction {
            Direction::Exit => {
                if let Some(pos) = self.portfolio.positions.get(&signal.symbol) {
                    let qty = pos.qty.abs();
                    if qty > f64::EPSILON {
                        let side = if pos.is_long() { Side::Sell } else { Side::Buy };
                        let order = OrderRequest::market(
                            signal.timestamp, &signal.symbol, side, qty,
                        );
                        self.bus
                            .send(Event::Order(alm_core::event::OrderEvent { order }));
                    }
                }
            }
            Direction::Long | Direction::Short => {
                if self.single_entry {
                    let blocked = self
                        .portfolio
                        .positions
                        .get(&signal.symbol)
                        .map_or(false, |p| match signal.direction {
                            Direction::Long => p.is_long(),
                            Direction::Short => p.is_short(),
                            Direction::Exit => false,
                        });
                    if blocked {
                        return;
                    }
                }
                if self.risk.validate(signal, &self.portfolio) {
                    let qty = self.risk.size(signal, &self.portfolio, self.last_price);
                    if qty > f64::EPSILON {
                        if signal.target_price.is_some() || signal.stop_price.is_some()
                            || signal.trailing_stop_pct.is_some() || signal.max_bars_held.is_some() {
                            self.pending_signal_levels.insert(
                                signal.symbol.clone(),
                                (signal.target_price, signal.stop_price, signal.trailing_stop_pct, signal.max_bars_held, signal.is_offset),
                            );
                        }
                        let side = match signal.direction {
                            Direction::Long => Side::Buy,
                            Direction::Short => Side::Sell,
                            Direction::Exit => unreachable!(),
                        };
                        let order =
                            OrderRequest::market(signal.timestamp, &signal.symbol, side, qty);
                        self.bus
                            .send(Event::Order(alm_core::event::OrderEvent { order }));
                    }
                }
            }
        }
    }

    fn handle_fill(&mut self, fill: &alm_core::order::Fill) {
        self.portfolio.apply_fill(fill);
        let still_open = self
            .portfolio
            .positions
            .get(&fill.symbol)
            .map_or(false, |p| p.qty.abs() > f64::EPSILON);
        if still_open {
            let sig_levels = self.pending_signal_levels.remove(&fill.symbol);
            let (tp, sl, trail, max_bars, is_offset) = sig_levels.unwrap_or((None, None, None, None, false));
            self.position_trackers
                .entry(fill.symbol.clone())
                .or_insert_with(|| {
                    let is_long = self
                        .portfolio
                        .positions
                        .get(&fill.symbol)
                        .map_or(true, |p| p.qty > 0.0);
                    let (tp_abs, sl_abs) = resolve_offset_levels(fill.price, tp, sl, is_offset, is_long);
                    PositionTracker::with_levels(fill.price, sl_abs, tp_abs, trail, max_bars, is_long)
                });
        } else {
            self.pending_signal_levels.remove(&fill.symbol);
            if let Some(tr) = self.position_trackers.remove(&fill.symbol) {
                if let Some(trade) = self.portfolio.trades.last_mut() {
                    trade.mae_pct = tr.mae;
                    trade.mfe_pct = tr.mfe;
                    trade.bars_held = tr.bars_held;
                }
            }
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

/// Resolve signal-level TP/SL to absolute prices. When `is_offset`, target/stop are
/// magnitudes whose side depends on direction (long → TP above / SL below; short →
/// TP below / SL above). When not offset they are already absolute and returned as-is.
fn resolve_offset_levels(
    price: f64,
    target: Option<f64>,
    stop: Option<f64>,
    is_offset: bool,
    is_long: bool,
) -> (Option<f64>, Option<f64>) {
    if !is_offset {
        return (target, stop);
    }
    let tp = target.map(|d| if is_long { price + d.abs() } else { price - d.abs() });
    let sl = stop.map(|d| if is_long { price - d.abs() } else { price + d.abs() });
    (tp, sl)
}

// ── Tests ────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_data::BarVecFeed;
    use alm_strategy::FixedFractional;

    fn mk_bar(ts: i64, c: f64, sym: &str) -> Bar {
        Bar::new(ts, sym, c, c + 0.05, c - 0.05, c, 10.0)
    }

    // ── Heap ordering ────────────────────────────────────────────────────────

    #[test]
    fn heap_pops_earliest_close_first_then_larger_tf_first_on_tie() {
        let mut h: BinaryHeap<HeapEntry> = BinaryHeap::new();
        // M1@11:59 closes at 12:00
        h.push(HeapEntry { close_ts: 12 * 60 * 60_000, tf_ms: 60_000,
                           tf: Timeframe::M1, bar: mk_bar(11 * 60 * 60_000 + 59 * 60_000, 1.0, "X") });
        // H1@11:00 closes at 12:00 (TIE with M1@11:59)
        h.push(HeapEntry { close_ts: 12 * 60 * 60_000, tf_ms: 3_600_000,
                           tf: Timeframe::H1, bar: mk_bar(11 * 60 * 60_000, 2.0, "X") });
        // M1@10:30 closes at 10:31 — earliest, should pop first
        h.push(HeapEntry { close_ts: 10 * 60 * 60_000 + 31 * 60_000, tf_ms: 60_000,
                           tf: Timeframe::M1, bar: mk_bar(10 * 60 * 60_000 + 30 * 60_000, 3.0, "X") });

        let first = h.pop().unwrap();
        assert_eq!(first.tf, Timeframe::M1);
        assert_eq!(first.bar.close, 3.0, "earliest close_ts wins");

        // Now the two tied entries — HTF must pop before base on tie.
        let second = h.pop().unwrap();
        assert_eq!(second.tf, Timeframe::H1, "larger TF pops first on tied close_ts");
        let third = h.pop().unwrap();
        assert_eq!(third.tf, Timeframe::M1);
    }

    // ── End-to-end ──────────────────────────────────────────────────────────

    /// MTF strategy reacting only when H1 just closed at this tick:
    /// long if last H1 close > prev, exit otherwise. Order fills on
    /// the next base bar's open.
    struct MtfTrend { symbol: String }

    impl MtfStrategy for MtfTrend {
        fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
            // Only act on ticks where H1 closed (regardless of base bar presence).
            if !snap.just_closed(Timeframe::H1) {
                return vec![];
            }
            let h1 = snap.get(Timeframe::H1).unwrap();
            if h1.len() < 2 { return vec![]; }
            let last = h1.back().unwrap();
            let prev = h1.iter().rev().nth(1).unwrap();
            // Use base bar timestamp if present, else fall back to close_ts.
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

    fn m1_uptrend(n_minutes: usize) -> Vec<Bar> {
        (0..n_minutes as i64)
            .map(|i| mk_bar(i * 60_000, 100.0 + (i as f64) * 0.1, "TEST"))
            .collect()
    }

    fn h1_uptrend(n_hours: usize) -> Vec<Bar> {
        (0..n_hours as i64)
            .map(|i| mk_bar(i * 3_600_000, 100.0 + (i as f64) * 6.0, "TEST"))
            .collect()
    }

    #[test]
    fn run_with_two_independent_feeds() {
        // 4 hours of M1 + matching H1 from independent feeds.
        let m1 = BarVecFeed::new(m1_uptrend(4 * 60), "TEST".into());
        let h1 = BarVecFeed::new(h1_uptrend(4), "TEST".into());

        let mut engine = MtfEngine::sync(
            10_000.0,
            MtfTrend { symbol: "TEST".into() },
            FixedFractional::fractional(0.95, 1),
            0.0, 0.0,
        )
        .with_base_tf(Timeframe::M1)
        .with_single_entry();
        engine.add_feed(Timeframe::M1, m1);
        engine.add_feed(Timeframe::H1, h1);

        let report = engine.run(0.0);

        // Equity curve has one entry per base bar (240).
        assert_eq!(engine.portfolio.equity_curve.len(), 240);
        // Strategy emits a Long on the first base bar after the SECOND H1
        // close — uptrend means at least one trade.
        assert!(report.total_trades >= 1, "expected ≥1 trade in uptrend");
    }

    #[test]
    fn warmup_suppresses_trading_before_until_mtf() {
        let m1 = BarVecFeed::new(m1_uptrend(4 * 60), "TEST".into()); // 240 base bars
        let h1 = BarVecFeed::new(h1_uptrend(4), "TEST".into());
        let until = 120 * 60_000; // warm the first 120 base bars; trade from there
        let mut engine = MtfEngine::sync(
            10_000.0, MtfTrend { symbol: "TEST".into() },
            FixedFractional::fractional(0.95, 1), 0.0, 0.0,
        )
        .with_base_tf(Timeframe::M1)
        .with_single_entry()
        .with_warmup_until(until);
        engine.add_feed(Timeframe::M1, m1);
        engine.add_feed(Timeframe::H1, h1);
        engine.run(0.0);

        // No equity / trade before the warm-up boundary, and fewer than the full 240.
        assert!(engine.portfolio.equity_curve.iter().all(|p| p.timestamp >= until),
            "equity recorded during warm-up");
        assert!(engine.portfolio.equity_curve.len() < 240, "warm-up bars still recorded equity");
        assert!(engine.portfolio.trades.iter().all(|t| t.entry_timestamp >= until),
            "trade entered during warm-up");
    }

    #[test]
    fn htf_visible_at_end_of_its_own_bucket_without_lookahead() {
        // At every base bar, verify that the latest H1 in `views[H1]` has
        // close_ts ≤ base_bar.close_ts. Tracking close_ts ourselves since
        // the snapshot only carries Bar.timestamp (open).
        struct Verifier {
            h1_tf_ms: i64,
            violations: usize,
            tied_inclusions: usize,
        }
        impl MtfStrategy for Verifier {
            fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
                // Skip ticks with no base bar (none happen in this aligned test).
                if let Some(h1) = snap.get(Timeframe::H1) {
                    if let Some(last) = h1.back() {
                        let h1_close = last.timestamp + self.h1_tf_ms;
                        if h1_close > snap.close_ts {
                            self.violations += 1;
                        } else if h1_close == snap.close_ts {
                            // H1 closed exactly at this tick → visible in views
                            // because batch pushes HTF before snapshot is built.
                            self.tied_inclusions += 1;
                        }
                    }
                }
                vec![]
            }
            fn name(&self) -> &str { "verifier" }
            fn reset(&mut self) {}
        }

        let m1 = BarVecFeed::new(m1_uptrend(4 * 60), "TEST".into());
        let h1 = BarVecFeed::new(h1_uptrend(4), "TEST".into());
        let mut engine = MtfEngine::sync(
            1_000.0,
            Verifier {
                h1_tf_ms: Timeframe::H1.duration_ms(),
                violations: 0,
                tied_inclusions: 0,
            },
            FixedFractional::fractional(0.95, 1),
            0.0, 0.0,
        );
        engine.add_feed(Timeframe::M1, m1);
        engine.add_feed(Timeframe::H1, h1);
        let _ = engine.run(0.0);

        assert_eq!(engine.strategy.violations, 0, "no look-ahead allowed");
        // The H1@h closes at the same moment as the LAST M1 of hour `h`
        // (M1@h:59), so we expect at least one tied-inclusion per H1 bar
        // after the first. With 4 H1 bars → 3 ties (at minute 60, 120, 180).
        assert!(
            engine.strategy.tied_inclusions >= 3,
            "expected ≥3 tied close_ts inclusions, got {}",
            engine.strategy.tied_inclusions,
        );
    }

    #[test]
    fn htf_independent_truth_unchanged_by_base_bars() {
        // The HTF stream is read directly from its feed — we should not see
        // resampler artefacts. Build an H1 series whose closes intentionally
        // disagree with what M1 aggregation would produce, and verify the
        // strategy sees the H1 feed's values verbatim.
        let m1 = BarVecFeed::new(m1_uptrend(2 * 60), "TEST".into());
        // Custom H1 bars with closes 999, 888 — wildly different from m1 trend.
        let h1_custom = vec![
            Bar::new(0,            "TEST", 100.0, 200.0, 50.0, 999.0, 1.0),
            Bar::new(3_600_000,    "TEST", 200.0, 300.0, 80.0, 888.0, 1.0),
        ];
        let h1 = BarVecFeed::new(h1_custom, "TEST".into());

        struct Capturer { last_h1_close: Option<f64> }
        impl MtfStrategy for Capturer {
            fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
                if let Some(h1) = snap.get(Timeframe::H1) {
                    if let Some(last) = h1.back() {
                        self.last_h1_close = Some(last.close);
                    }
                }
                vec![]
            }
            fn name(&self) -> &str { "cap" }
            fn reset(&mut self) {}
        }

        let mut engine = MtfEngine::sync(
            1_000.0, Capturer { last_h1_close: None },
            FixedFractional::fractional(0.95, 1), 0.0, 0.0,
        );
        engine.add_feed(Timeframe::M1, m1);
        engine.add_feed(Timeframe::H1, h1);
        let _ = engine.run(0.0);

        // After running through 120 M1 bars covering 2 H1 buckets, the strategy
        // must have seen the second H1's close = 888.0, NOT some aggregated value.
        assert_eq!(engine.strategy.last_h1_close, Some(888.0));
    }

    #[test]
    fn tick_batches_simultaneous_closes_into_one_callback() {
        // M1 + H1 + H4 all close at 12:00:00 (when minute 240 starts).
        // Strategy records the events array seen on each tick.
        struct Recorder { ticks: Vec<(i64, Vec<Timeframe>)> }
        impl MtfStrategy for Recorder {
            fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
                let tfs: Vec<Timeframe> = snap.events.iter().map(|e| e.tf).collect();
                self.ticks.push((snap.close_ts, tfs));
                vec![]
            }
            fn name(&self) -> &str { "rec" }
            fn reset(&mut self) {}
        }

        // 5 hours of M1 (300 bars) + matching H1 (5 bars) + H4 (1 bar).
        let m1_bars: Vec<Bar> = (0..300i64)
            .map(|i| mk_bar(i * 60_000, 100.0, "TEST"))
            .collect();
        let h1_bars: Vec<Bar> = (0..5i64)
            .map(|i| mk_bar(i * 3_600_000, 100.0, "TEST"))
            .collect();
        let h4_bars: Vec<Bar> = vec![mk_bar(0, 100.0, "TEST")]; // H4@0 closes at 4h.

        let mut engine = MtfEngine::sync(
            1_000.0,
            Recorder { ticks: Vec::new() },
            FixedFractional::fractional(0.95, 1),
            0.0, 0.0,
        );
        engine.add_feed(Timeframe::M1, BarVecFeed::new(m1_bars, "TEST".into()));
        engine.add_feed(Timeframe::H1, BarVecFeed::new(h1_bars, "TEST".into()));
        engine.add_feed(Timeframe::H4, BarVecFeed::new(h4_bars, "TEST".into()));
        let _ = engine.run(0.0);

        // Look for the tick at close_ts = 4h = 14_400_000 ms — should batch
        // M1@3:59, H1@3:00, and H4@0:00 all together.
        let four_h = 4 * 3_600_000;
        let big_batch = engine
            .strategy
            .ticks
            .iter()
            .find(|(ts, _)| *ts == four_h)
            .expect("tick at 4h must exist");
        assert!(big_batch.1.contains(&Timeframe::M1));
        assert!(big_batch.1.contains(&Timeframe::H1));
        assert!(big_batch.1.contains(&Timeframe::H4));
        // Largest TF first in the events array.
        assert_eq!(big_batch.1.first(), Some(&Timeframe::H4));
    }

    #[test]
    fn just_closed_helper_correctly_identifies_htf_event() {
        // Strategy reacts only when H1 fires AT THIS TICK (not on M1-only ticks).
        struct H1Reactor { h1_hits: usize, m1_only_hits: usize }
        impl MtfStrategy for H1Reactor {
            fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
                if snap.just_closed(Timeframe::H1) {
                    self.h1_hits += 1;
                } else if snap.just_closed(Timeframe::M1) {
                    self.m1_only_hits += 1;
                }
                vec![]
            }
            fn name(&self) -> &str { "h1_react" }
            fn reset(&mut self) {}
        }
        let m1 = BarVecFeed::new(m1_uptrend(180), "TEST".into()); // 3h M1
        let h1 = BarVecFeed::new(h1_uptrend(3), "TEST".into());   // 3 H1 bars
        let mut engine = MtfEngine::sync(
            1_000.0, H1Reactor { h1_hits: 0, m1_only_hits: 0 },
            FixedFractional::fractional(0.95, 1), 0.0, 0.0,
        );
        engine.add_feed(Timeframe::M1, m1);
        engine.add_feed(Timeframe::H1, h1);
        let _ = engine.run(0.0);
        // 3 H1 ticks (each batched with the last M1 of its hour) +
        // 180-3 = 177 M1-only ticks.
        assert_eq!(engine.strategy.h1_hits, 3);
        assert_eq!(engine.strategy.m1_only_hits, 177);
    }

    #[test]
    fn empty_when_no_base_feed_registered() {
        let h1 = BarVecFeed::new(h1_uptrend(4), "TEST".into());
        let mut engine = MtfEngine::sync(
            1_000.0, MtfTrend { symbol: "TEST".into() },
            FixedFractional::fractional(0.95, 1), 0.0, 0.0,
        );
        // Only HTF feed — no base TF.
        engine.add_feed(Timeframe::H1, h1);
        let report = engine.run(0.0);
        // No base bars → no strategy callbacks → no trades, no equity.
        assert_eq!(report.total_trades, 0);
        assert_eq!(engine.portfolio.equity_curve.len(), 0);
    }
}
