//! `EngineCore` — shared per-bar state and logic used by both [`Engine`] and
//! [`MtfEngine`].
//!
//! The two engines differ only in **how they feed bars and signals**:
//!
//! - [`Engine`]    : single [`BarFeed`] → `Strategy::on_bar` / `on_window`
//! - [`MtfEngine`] : N feeds, heap time-merge → `MtfStrategy::on_bars`
//!
//! Everything else — fills, exit rules (SL/TP/trailing/max-bars), position
//! sizing, pyramiding, `ReversePolicy`, regime tracking, MAE/MFE, equity
//! curve, metrics — lives here and is identical for both.

use std::collections::HashMap;

use alm_core::{
    bar::Bar,
    bus::EventBus,
    event::{EquityEvent, Event, FillEvent},
    exit::{ExitReason, IntraBarMode, PositionTracker},
    order::{Fill, OrderRequest, Side},
    portfolio::Portfolio,
    regime::{RegimeState, RegimeSummary, RegimeTradeStats},
    signal::{Direction, Signal},
    strategy::RiskManager,
};
use alm_report::BacktestReport;
use tracing::debug;

use crate::broker::SimBroker;
use crate::bus_sync::SyncBus;
use crate::engine::{base_symbol, resolve_offset_levels, ReversePolicy};

// ── Core struct ───────────────────────────────────────────────────────────────

/// Shared engine state and per-bar logic.
///
/// Embedded by both [`Engine`](crate::Engine) and
/// [`MtfEngine`](crate::MtfEngine). Never used standalone.
pub struct EngineCore<R: RiskManager> {
    pub portfolio: Portfolio,
    pub broker: SimBroker,
    pub risk: R,
    pub(crate) bus: SyncBus,

    /// Last known close (or force-close) price for the tracked symbol.
    pub last_price: f64,

    pub(crate) intra_bar_mode: IntraBarMode,
    pub(crate) position_trackers: HashMap<String, PositionTracker>,

    /// Pending exit levels from the entry Signal — consumed at Fill time.
    /// (target_price, stop_price, trailing_stop_pct, max_bars_held, is_offset)
    pub(crate) pending_signal_levels:
        HashMap<String, (Option<f64>, Option<f64>, Option<f64>, Option<usize>, bool)>,

    /// Block same-direction re-entry while a position is open.
    pub(crate) single_entry: bool,

    /// How to handle a directional signal that arrives while the opposite
    /// position is already open. `None` = original net-qty behaviour.
    pub(crate) reverse_policy: Option<ReversePolicy>,

    /// Pyramiding: max accumulated entry legs (1 = no pyramiding).
    pub(crate) max_units: usize,

    /// Pyramiding: hard cap on total exposure as a fraction of equity (0 = off).
    pub(crate) max_position_pct: f64,

    /// `true` = MERGE (one averaged position); `false` = INDEPENDENT legs.
    pub(crate) pyramid_merge: bool,

    /// Warm-up boundary (Unix ms). Bars before this only advance indicators.
    pub(crate) warmup_until: Option<i64>,

    /// Regime state active when each currently-open position was entered.
    pub(crate) regime_at_entry: HashMap<String, RegimeState>,

    /// Most-recently observed regime state (from strategy).
    pub last_regime: Option<RegimeState>,

    /// Timestamped regime label transitions over the run.
    pub regime_changes: Vec<(i64, String)>,

    /// Strategy name for metric labels — set by the outer engine before run().
    pub(crate) metrics_strategy: String,
}

// ── Constructor ───────────────────────────────────────────────────────────────

impl<R: RiskManager> EngineCore<R> {
    pub fn new(initial_capital: f64, risk: R, commission_pct: f64, slippage_pct: f64) -> Self {
        Self {
            portfolio: Portfolio::new(initial_capital),
            broker: SimBroker::new(commission_pct, slippage_pct),
            risk,
            bus: SyncBus::new(),
            last_price: 0.0,
            intra_bar_mode: IntraBarMode::default(),
            position_trackers: HashMap::new(),
            pending_signal_levels: HashMap::new(),
            single_entry: false,
            reverse_policy: None,
            max_units: 1,
            max_position_pct: 0.0,
            pyramid_merge: true,
            warmup_until: None,
            regime_at_entry: HashMap::new(),
            last_regime: None,
            regime_changes: Vec::new(),
            metrics_strategy: String::new(),
        }
    }

    // ── Builder helpers ───────────────────────────────────────────────────────

    pub fn single_entry(mut self) -> Self {
        self.single_entry = true;
        self
    }

    pub fn reverse_policy(mut self, policy: ReversePolicy) -> Self {
        self.reverse_policy = Some(policy);
        self
    }

    pub fn pyramiding(mut self, max_units: usize, max_position_pct: f64) -> Self {
        self.max_units = max_units.max(1);
        self.max_position_pct = max_position_pct.max(0.0);
        self
    }

    pub fn independent_legs(mut self) -> Self {
        self.pyramid_merge = false;
        self
    }

    pub fn warmup_until(mut self, until: i64) -> Self {
        self.warmup_until = if until > 0 { Some(until) } else { None };
        self
    }

    pub fn intra_bar_mode(mut self, mode: IntraBarMode) -> Self {
        self.intra_bar_mode = mode;
        self
    }
}

// ── Per-bar housekeeping ──────────────────────────────────────────────────────

impl<R: RiskManager> EngineCore<R> {
    /// Process one base bar: fill pending orders, run exit rules, record
    /// equity. Called by the outer engine BEFORE strategy signal generation.
    ///
    /// Returns `false` during warm-up (bar before `warmup_until`): in that
    /// case only `risk.on_bar` runs and the caller should also skip signal
    /// dispatch.
    pub fn run_base_tick(&mut self, bar: &Bar) -> bool {
        self.last_price = bar.close;
        self.risk.on_bar(bar);

        if let Some(until) = self.warmup_until {
            if bar.timestamp < until {
                return false;
            }
        }

        // 1. Fill pending market orders at this bar's open (no look-ahead).
        let fills = self.broker.process_pending(bar);
        let fill_count = fills.len();
        for fill in fills {
            self.bus.send(Event::Fill(FillEvent { fill }));
        }

        // 2. Drain fill events before exit rules to prevent double-close:
        //    if bar[N-1] emitted an Exit signal (pending Sell) AND the
        //    PositionTracker SL fires on bar[N], the position is already flat
        //    after draining the pending fill, so run_exit_checks skips it.
        let mut guard = fill_count + 2;
        while let Some(early) = self.bus.try_recv() {
            if guard == 0 {
                tracing::error!("early-drain guard tripped — bailing out");
                self.bus.send(early);
                break;
            }
            guard -= 1;
            match early {
                Event::Fill(_) | Event::Equity(_) => self.dispatch(early),
                other => {
                    tracing::warn!("unexpected non-Fill/Equity event during early drain");
                    self.bus.send(other);
                    break;
                }
            }
        }

        // 3. Exit rules (BEFORE record_equity so exits are in this bar's point).
        self.run_exit_checks(bar);

        // 4. Snapshot equity post-exit.
        let prices = self.leg_price_map(&bar.symbol, self.last_price);
        self.portfolio.record_equity(bar.timestamp, &prices);
        let eq = self.portfolio.equity(&prices);
        self.bus.send(Event::Equity(EquityEvent {
            timestamp: bar.timestamp,
            equity: eq,
            cash: self.portfolio.cash,
        }));

        true
    }

    /// Emit strategy-produced signals onto the bus and drain resulting events.
    pub fn dispatch_signals(&mut self, signals: Vec<Signal>) {
        for sig in signals {
            metrics::counter!("alm_engine_signals_total",
                "strategy"  => self.metrics_strategy.clone(),
                "direction" => format!("{:?}", sig.direction),
            ).increment(1);
            self.bus.send(Event::Signal(alm_core::event::SignalEvent { signal: sig }));
        }
        while let Some(ev) = self.bus.try_recv() {
            self.dispatch(ev);
        }
    }

    /// Record a regime state observed from the strategy on this bar.
    /// Pushes a transition entry only when the label changes.
    pub fn update_regime(&mut self, timestamp: i64, state: Option<RegimeState>) {
        if let Some(state) = state {
            let prev_label = self.last_regime.as_ref().map(|s| s.label());
            let new_label = state.label();
            if prev_label.as_deref() != Some(new_label.as_str()) {
                self.regime_changes.push((timestamp, new_label));
            }
            self.last_regime = Some(state);
        }
    }

    /// Force-close all open positions at `last_bar.close` (end of data).
    pub fn force_close_all(&mut self, last_bar: &Bar) {
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
            let regime_state = self.regime_at_entry.remove(&sym);
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
                    if let Some(state) = regime_state {
                        trade.regime_at_entry = Some(state);
                    }
                }
            }
        }
    }

    /// Build a `RegimeSummary` from collected regime transitions and per-trade
    /// regime labels. Returns `None` when the strategy never produced a regime.
    pub fn build_regime_summary(&self) -> Option<RegimeSummary> {
        if self.regime_changes.is_empty() {
            return None;
        }
        let mut buckets: HashMap<String, (usize, usize, f64, f64, f64)> = HashMap::new();
        for t in &self.portfolio.trades {
            let key = match &t.regime_at_entry {
                Some(state) => state.label(),
                None => continue,
            };
            let entry = buckets.entry(key).or_insert((0, 0, 0.0, 0.0, 0.0));
            entry.0 += 1;
            if t.pnl > 0.0 {
                entry.1 += 1;
                entry.3 += t.pnl;
            } else {
                entry.4 += -t.pnl;
            }
            entry.2 += t.pnl_pct;
        }
        let mut trade_breakdown: Vec<RegimeTradeStats> = buckets
            .into_iter()
            .map(|(label, (trades, wins, sum_pct, gain, loss))| {
                let win_rate_pct =
                    if trades > 0 { 100.0 * wins as f64 / trades as f64 } else { 0.0 };
                let avg_return_pct =
                    if trades > 0 { 100.0 * sum_pct / trades as f64 } else { 0.0 };
                let profit_factor = if loss > f64::EPSILON {
                    gain / loss
                } else if gain > 0.0 {
                    f64::INFINITY
                } else {
                    0.0
                };
                RegimeTradeStats { label, trades, win_rate_pct, avg_return_pct, profit_factor }
            })
            .collect();
        trade_breakdown.sort_by(|a, b| b.trades.cmp(&a.trades));
        Some(RegimeSummary { changes: self.regime_changes.clone(), trade_breakdown })
    }

    /// Generate a `BacktestReport` from the current portfolio state.
    pub fn build_report(
        &self,
        strategy_name: &str,
        symbol: &str,
        risk_free_annual: f64,
    ) -> BacktestReport {
        let mut report =
            BacktestReport::generate(strategy_name, symbol, &self.portfolio, risk_free_annual);
        report.regime_summary = self.build_regime_summary();
        report
    }

    /// Reset all mutable state for reuse (batch optimisation).
    pub fn reset(&mut self, initial_capital: f64) {
        self.portfolio = Portfolio::new(initial_capital);
        self.position_trackers.clear();
        self.pending_signal_levels.clear();
        self.regime_at_entry.clear();
        self.last_regime = None;
        self.regime_changes.clear();
    }
}

// ── Internal per-bar logic ────────────────────────────────────────────────────

impl<R: RiskManager> EngineCore<R> {
    /// Tick every open position tracker; force-close any that fire an exit rule.
    pub(crate) fn run_exit_checks(&mut self, bar: &Bar) {
        let to_close: Vec<(String, f64, ExitReason)> = self
            .position_trackers
            .iter_mut()
            .filter_map(|(sym, tracker)| {
                tracker
                    .update_and_check(bar.open, bar.high, bar.low, bar.close, self.intra_bar_mode)
                    .map(|(fill_price, reason)| (sym.clone(), fill_price, reason))
            })
            .collect();

        for (sym, fill_price, reason) in to_close {
            let tracker = self.position_trackers.remove(&sym);
            let regime_state = self.regime_at_entry.remove(&sym);
            // Cancel any pending close for this symbol so it doesn't fire again
            // in the same bar's event loop and accidentally re-open a position.
            self.broker.cancel_for_symbol(&sym);
            if let Some(pos) = self.portfolio.positions.get(&sym) {
                let qty = pos.qty.abs();
                let side = if pos.is_long() { Side::Sell } else { Side::Buy };
                let fill =
                    self.broker.force_close(&sym, qty, side, bar.timestamp, fill_price);
                self.portfolio.apply_fill(&fill);
                self.last_price = fill_price;
                if let Some(tr) = tracker {
                    if let Some(trade) = self.portfolio.trades.last_mut() {
                        trade.mae_pct = tr.mae;
                        trade.mfe_pct = tr.mfe;
                        trade.bars_held = tr.bars_held;
                        trade.exit_reason = reason.clone();
                        if let Some(state) = regime_state.clone() {
                            trade.regime_at_entry = Some(state);
                        }
                    }
                }
            }
            debug!(symbol = %sym, fill_price, ?reason, "exit rule triggered → force_close");
        }
    }

    /// Route an event to its handler. Market events are not expected here
    /// (outer engine handles bar processing inline).
    pub(crate) fn dispatch(&mut self, event: Event) {
        match event {
            Event::Market(_) => {
                debug!("unexpected Market event in EngineCore::dispatch — ignoring");
            }
            Event::Signal(ref sig_event) => self.on_signal(&sig_event.signal),
            Event::Order(ref order_event) => {
                self.broker.submit(order_event.order.clone());
            }
            Event::Fill(ref fill_event) => self.on_fill(&fill_event.fill),
            Event::Equity(_) => {
                // No-op in backtest: equity curve built by record_equity().
            }
        }
    }

    fn on_signal(&mut self, signal: &Signal) {
        match signal.direction {
            Direction::Exit => self.close_position_market(signal),
            Direction::Long => self.open_position(signal, Side::Buy),
            Direction::Short => self.open_position(signal, Side::Sell),
        }
    }

    /// Emit market orders that close EVERY open leg for the signal's symbol.
    fn close_position_market(&mut self, signal: &Signal) {
        let base = signal.symbol.as_str();
        let legs: Vec<(String, f64, bool)> = self
            .portfolio
            .positions
            .iter()
            .filter(|(k, p)| base_symbol(k) == base && p.qty.abs() > f64::EPSILON)
            .map(|(k, p)| (k.clone(), p.qty.abs(), p.is_long()))
            .collect();
        for (key, qty, is_long) in legs {
            let side = if is_long { Side::Sell } else { Side::Buy };
            let order = OrderRequest::market(signal.timestamp, &key, side, qty);
            metrics::counter!("alm_engine_orders_total",
                "strategy" => self.metrics_strategy.clone(),
                "side"     => "sell",
            ).increment(1);
            self.bus.send(Event::Order(alm_core::event::OrderEvent { order }));
            debug!(symbol = %key, qty, ?side, strength = signal.strength, "close signal → order");
        }
    }

    /// Open a position in `side`, honouring `ReversePolicy`, `single_entry`,
    /// pyramiding guards, and risk validation.
    fn open_position(&mut self, signal: &Signal, side: Side) {
        let base = signal.symbol.as_str();

        // ── ReversePolicy guard ───────────────────────────────────────────────
        if let Some(policy) = self.reverse_policy {
            let opp_side = match side {
                Side::Buy => Side::Sell,
                Side::Sell => Side::Buy,
            };
            let opp_legs: Vec<(String, f64, bool)> = self
                .portfolio
                .positions
                .iter()
                .filter(|(k, p)| {
                    base_symbol(k) == base
                        && p.qty.abs() > f64::EPSILON
                        && match opp_side {
                            Side::Buy => p.is_long(),
                            Side::Sell => p.is_short(),
                        }
                })
                .map(|(k, p)| (k.clone(), p.qty.abs(), p.is_long()))
                .collect();
            if !opp_legs.is_empty() {
                for (key, qty, is_long) in opp_legs {
                    let close_side = if is_long { Side::Sell } else { Side::Buy };
                    let order = OrderRequest::market(signal.timestamp, &key, close_side, qty);
                    metrics::counter!("alm_engine_orders_total",
                        "strategy" => self.metrics_strategy.clone(),
                        "side"     => if is_long { "sell" } else { "buy" },
                    ).increment(1);
                    self.bus.send(Event::Order(alm_core::event::OrderEvent { order }));
                    debug!(symbol = %key, qty, ?policy, "reverse signal → close opposite position");
                }
                match policy {
                    ReversePolicy::Exit => return,
                    ReversePolicy::Flip => {}
                }
            }
        }
        // ─────────────────────────────────────────────────────────────────────

        let legs = self.same_dir_legs(base, side);
        let in_pos_same = !legs.is_empty();
        let mut is_pyramid_add = false;
        let mut leg_key = signal.symbol.clone();

        if in_pos_same {
            if self.max_units > 1 {
                let count = if self.pyramid_merge {
                    self.position_trackers.get(base).map_or(1, |t| t.legs)
                } else {
                    legs.len()
                };
                if count >= self.max_units {
                    return;
                }
                let last_px = if self.pyramid_merge {
                    self.position_trackers
                        .get(base)
                        .map_or(self.last_price, |t| t.last_entry_price)
                } else {
                    match side {
                        Side::Buy => legs.iter().map(|l| l.1).fold(f64::MIN, f64::max),
                        Side::Sell => legs.iter().map(|l| l.1).fold(f64::MAX, f64::min),
                    }
                };
                let advanced = match side {
                    Side::Buy => self.last_price > last_px,
                    Side::Sell => self.last_price < last_px,
                };
                if !advanced {
                    return;
                }
                if self.max_position_pct > 0.0 {
                    let prices = self.leg_price_map(base, self.last_price);
                    let eq = self.portfolio.equity(&prices);
                    let pos_val: f64 =
                        legs.iter().map(|l| l.2.abs() * self.last_price).sum();
                    if eq > 0.0 && pos_val >= self.max_position_pct * eq {
                        return;
                    }
                }
                is_pyramid_add = true;
                if !self.pyramid_merge {
                    leg_key = self.mint_leg_key(base);
                }
            } else if self.single_entry {
                return;
            }
        }

        if is_pyramid_add || self.risk.validate(signal, &self.portfolio) {
            let qty = self.risk.size(signal, &self.portfolio, self.last_price);
            if qty > f64::EPSILON {
                if signal.target_price.is_some()
                    || signal.stop_price.is_some()
                    || signal.trailing_stop_pct.is_some()
                    || signal.max_bars_held.is_some()
                {
                    self.pending_signal_levels.insert(
                        leg_key.clone(),
                        (
                            signal.target_price,
                            signal.stop_price,
                            signal.trailing_stop_pct,
                            signal.max_bars_held,
                            signal.is_offset,
                        ),
                    );
                }
                let order = OrderRequest::market(signal.timestamp, &leg_key, side, qty);
                let side_label = match side {
                    Side::Buy => "buy",
                    Side::Sell => "sell",
                };
                metrics::counter!("alm_engine_orders_total",
                    "strategy" => self.metrics_strategy.clone(),
                    "side"     => side_label,
                ).increment(1);
                self.bus.send(Event::Order(alm_core::event::OrderEvent { order }));
                debug!(
                    symbol = %signal.symbol, qty,
                    strength = signal.strength, ?side,
                    "entry signal → order"
                );
            }
        }
    }

    /// Apply a fill to the portfolio and maintain the per-position exit tracker.
    pub(crate) fn on_fill(&mut self, fill: &Fill) {
        metrics::counter!("alm_engine_fills_total",
            "strategy" => self.metrics_strategy.clone()
        ).increment(1);
        debug!(
            symbol = %fill.symbol, side = ?fill.side,
            qty = fill.qty, price = fill.price, commission = fill.commission,
            "fill"
        );
        self.portfolio.apply_fill(fill);
        debug!(cash = format!("{:.2}", self.portfolio.cash), "portfolio after fill");

        let still_open = self
            .portfolio
            .positions
            .get(&fill.symbol)
            .map(|p| p.qty.abs() > f64::EPSILON)
            .unwrap_or(false);

        if still_open {
            let sig_levels = self.pending_signal_levels.remove(&fill.symbol);
            let (sig_tp, sig_sl, sig_trail, sig_max_bars, sig_offset) =
                sig_levels.unwrap_or((None, None, None, None, false));
            let is_long = self
                .portfolio
                .positions
                .get(&fill.symbol)
                .map_or(true, |p| p.qty > 0.0);
            let (sig_tp, sig_sl) = resolve_offset_levels(
                fill.price, sig_tp, sig_sl, sig_offset, is_long,
            );
            let was_new = !self.position_trackers.contains_key(&fill.symbol);
            if was_new {
                self.position_trackers.insert(
                    fill.symbol.clone(),
                    PositionTracker::with_levels(
                        fill.price, sig_sl, sig_tp, sig_trail, sig_max_bars, is_long,
                    ),
                );
                if let Some(state) = &self.last_regime {
                    self.regime_at_entry.insert(fill.symbol.clone(), state.clone());
                }
            } else if self.max_units > 1 {
                if let Some(tr) = self.position_trackers.get_mut(&fill.symbol) {
                    tr.add_leg(fill.price, sig_sl, sig_tp, sig_trail, sig_max_bars);
                }
            }
        } else {
            self.pending_signal_levels.remove(&fill.symbol);
            let regime_state = self.regime_at_entry.remove(&fill.symbol);
            if let Some(tr) = self.position_trackers.remove(&fill.symbol) {
                if let Some(trade) = self.portfolio.trades.last_mut() {
                    trade.mae_pct = tr.mae;
                    trade.mfe_pct = tr.mfe;
                    trade.bars_held = tr.bars_held;
                    if let Some(state) = regime_state {
                        trade.regime_at_entry = Some(state);
                    }
                }
            }
        }
    }
}

// ── Position helpers ──────────────────────────────────────────────────────────

impl<R: RiskManager> EngineCore<R> {
    /// All currently-open position keys for `base` in the given direction.
    /// Returns `(key, avg_price, qty)`.
    pub(crate) fn same_dir_legs(
        &self,
        base: &str,
        side: Side,
    ) -> Vec<(String, f64, f64)> {
        self.portfolio
            .positions
            .iter()
            .filter(|(k, p)| {
                base_symbol(k) == base
                    && p.qty.abs() > f64::EPSILON
                    && match side {
                        Side::Buy => p.is_long(),
                        Side::Sell => p.is_short(),
                    }
            })
            .map(|(k, p)| (k.clone(), p.avg_price, p.qty))
            .collect()
    }

    /// Mint the next free independent-leg key (`base#2`, `base#3`, …).
    pub(crate) fn mint_leg_key(&self, base: &str) -> String {
        let mut i = 2;
        loop {
            let key = format!("{base}#{i}");
            if !self.portfolio.positions.contains_key(&key) {
                return key;
            }
            i += 1;
        }
    }

    /// Price map covering `base` and all its open independent legs (`base#n`),
    /// every entry set to `price`. Used for equity/exposure of a pyramided symbol.
    pub(crate) fn leg_price_map(&self, base: &str, price: f64) -> HashMap<String, f64> {
        let mut m = HashMap::new();
        m.insert(base.to_string(), price);
        for k in self.portfolio.positions.keys() {
            if base_symbol(k) == base {
                m.insert(k.clone(), price);
            }
        }
        m
    }
}
