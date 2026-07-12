use std::collections::HashMap;
use std::sync::Arc;

use anyhow::{anyhow, Result};
use alm_core::{signal::Signal, strategy::Strategy, Bar, Timeframe};
use alm_core::{MtfSnapshot, MtfStrategy, TfBarEvent, TfView};
use alm_ledger::Ledger;
use alm_strategy::{probe_script_htfs, ScriptStrategy, MtfScriptStrategy};
use tracing::{debug, info, trace};

// ── alignment-point helper ────────────────────────────────────────────────────

/// Returns true when the M1 bar at `m1_bar_ts` is the last bar of an HTF
/// bucket, i.e. the HTF bar closes exactly when this M1 bar closes.
///
/// `close_ts = m1_bar_ts + base_tf.duration_ms()`
/// alignment iff `close_ts % htf.duration_ms() == 0`
pub fn is_htf_align_point(m1_bar_ts: i64, base_tf: Timeframe, htf: Timeframe) -> bool {
    let close_ts = m1_bar_ts + base_tf.duration_ms();
    close_ts % htf.duration_ms() == 0
}

// ── LiveStrategy ──────────────────────────────────────────────────────────────

/// Discriminates between the v1 (pure single-TF) and v2 (MtfScriptStrategy
/// driven by real ledger HTF windows) execution paths.
enum LiveStrategy {
    /// v1: pure single-TF strategy. Receives every base-TF bar directly.
    /// No internal resampling — keeps memory flat and evaluation O(1).
    V1 {
        strategy: Box<dyn Strategy>,
    },
    /// v2: multi-TF strategy. On every M1 base tick, checks alignment points
    /// for each declared HTF. If aligned, reads the HTF bar from the ledger ring
    /// and includes it as an HTF event in the MtfSnapshot.
    V2 {
        strategy: Box<dyn MtfStrategy>,
        declared_htfs: Vec<Timeframe>,
        /// The strategy's own base TF — equals `Handle::target_tf`, which is
        /// the TF this hand evaluates at. Distinct from the *ledger* base TF
        /// (always M1 in production), which is the registry's `base_tf` field.
        /// Named `eval_tf` here to prevent the two from being confused at call sites.
        eval_tf: Timeframe,
    },
}

// ── Handle ────────────────────────────────────────────────────────────────────

pub struct Handle {
    pub hand_id: String,
    pub helm_id: String,
    pub symbol: String,   // raw ticker: "BTCUSDT", "BTC-USDT"
    pub exchange: String, // "binance" | "okx" | "alpaca" | "" (empty = test)
    pub is_future: bool,  // false = spot; true = perpetual/futures
    pub script: String,
    /// The timeframe this hand evaluates
    pub target_tf: Timeframe,
    live: LiveStrategy,
    ledger: Arc<Ledger>,
}

impl Handle {
    /// Build a hand.
    ///
    /// - `base_tf` is the ledger's external feed TF (typically M1). Used by V2
    ///   warmup to align HTF replay with base-TF history.
    /// - `target_tf` is the TF the hand evaluates at. For V1 this is the only
    ///   TF the strategy sees. For V2 it's the strategy's base TF — declared
    ///   HTFs in the script must be larger than `target_tf`.
    pub fn new(
        hand_id: String,
        helm_id: String,
        symbol: String,
        exchange: String,
        is_future: bool,
        script: String,
        ledger: &Arc<Ledger>,
        base_tf: Timeframe,
        target_tf: Timeframe,
    ) -> Result<Self> {
        // ── v2 detection: script declares HTF indicators ──────────────────────
        //
        // Parse-only probe first — cheap, no AST compile. Only build the full
        // MtfScriptStrategy when we know the script actually needs V2.
        let htfs = probe_script_htfs(&script);
        if !htfs.is_empty() {
            // Sanity: every declared HTF must be strictly larger than target_tf.
            if let Some(bad) = htfs.iter().find(|h| h.duration_ms() <= target_tf.duration_ms()) {
                anyhow::bail!(
                    "script declares TF `{bad}` not larger than hand target TF `{target_tf}`"
                );
            }
            let mut probe: Box<dyn MtfStrategy> = Box::new(MtfScriptStrategy::from_script_live(&script, target_tf)?);
            for &htf in &htfs {
                ledger.ensure_symbol(&symbol, htf, None);
            }
            warmup_v2(probe.as_mut(), &symbol, target_tf, &htfs, ledger);
            debug!(
                %hand_id, %symbol,
                base_tf = %base_tf, target_tf = %target_tf, htfs = ?htfs,
                "v2 MTF strategy warmed from ledger"
            );
            return Ok(Self {
                hand_id,
                helm_id,
                symbol,
                exchange,
                is_future,
                script,
                target_tf,
                live: LiveStrategy::V2 {
                    strategy: probe,
                    declared_htfs: htfs,
                    eval_tf: target_tf,
                },
                ledger: Arc::clone(ledger),
            });
        }

        // ── v1 path: pure single-TF, hand evaluates at target_tf ─────────────
        let mut strategy: Box<dyn Strategy> =
            Box::new(ScriptStrategy::from_script_live(&script).map_err(|e| anyhow!("{e}"))?);

        // Warm the strategy from ledger history at `target_tf`. Two cases:
        //
        // 1. `target_tf == base_tf` — feed the existing window directly.
        // 2. `target_tf > base_tf` — use alignment-point logic to reconstruct
        //    which bars to feed: walk the base_tf window and pick only the bars
        //    that sit at alignment points for target_tf. Read from ledger target_tf
        //    ring if populated; otherwise derive from base_tf history via alignment.
        let warm_bars: Vec<Bar> = if target_tf == base_tf {
            ledger
                .with_state(&symbol, target_tf, |s| s.bar_window.iter().cloned().collect())
                .unwrap_or_default()
        } else {
            let htf_window: Vec<Bar> = ledger
                .with_state(&symbol, target_tf, |s| s.bar_window.iter().cloned().collect())
                .unwrap_or_default();
            if !htf_window.is_empty() {
                htf_window
            } else {
                // Derive from base history: collect bars at alignment points.
                // Each alignment point corresponds to the last M1 bar in an HTF bucket.
                let base_window: Vec<Bar> = ledger
                    .with_state(&symbol, base_tf, |s| s.bar_window.iter().cloned().collect())
                    .unwrap_or_default();
                base_window
                    .into_iter()
                    .filter(|b| is_htf_align_point(b.timestamp, base_tf, target_tf))
                    .collect()
            }
        };
        for bar in &warm_bars {
            let _ = strategy.on_bar(bar);
        }

        debug!(
            %hand_id, %symbol,
            base_tf = %base_tf, target_tf = %target_tf,
            warmed_bars = warm_bars.len(),
            "v1 strategy warmed up from ledger history"
        );

        Ok(Self {
            hand_id,
            helm_id,
            symbol,
            exchange,
            is_future,
            script,
            target_tf,
            live: LiveStrategy::V1 { strategy },
            ledger: Arc::clone(ledger),
        })
    }

    /// Called from `SymbolGroup::evaluate_all` when a bar for this handle's
    /// `target_tf` has just closed.  The caller guarantees `bar.timeframe ==
    /// self.target_tf` via the TF filter in `evaluate_all`, so no alignment
    /// check is needed here.
    pub fn on_bar_base(&mut self, bar: &Bar) -> Vec<Signal> {
        let sigs = match &mut self.live {
            LiveStrategy::V1 { strategy } => strategy.on_bar(bar),
            LiveStrategy::V2 { strategy, declared_htfs, eval_tf } => {
                on_bar_v2(strategy.as_mut(), bar, *eval_tf, declared_htfs, &self.symbol, &self.ledger)
            }
        };
        if !sigs.is_empty() {
            trace!(
                hand_id = %self.hand_id, symbol = %self.symbol,
                n_signals = sigs.len(),
                "hand emitted signals"
            );
        }
        sigs
    }
}

// ── v2 live evaluation ────────────────────────────────────────────────────────

/// V2 live evaluation.
///
/// `tf` is the strategy's base TF (= handle's `target_tf`).  Called once per
/// `tf`-bar close.  For each declared HTF, checks whether this bar sits at an
/// alignment boundary and, if so, merges the latest HTF bar from the ledger.
fn on_bar_v2(
    strategy: &mut dyn MtfStrategy,
    bar: &Bar,
    tf: Timeframe,
    declared_htfs: &[Timeframe],
    symbol: &str,
    ledger: &Arc<Ledger>,
) -> Vec<Signal> {
    let mut htf_events: Vec<(Timeframe, Bar)> = Vec::new();
    for &htf in declared_htfs {
        if is_htf_align_point(bar.timestamp, tf, htf) {
            let latest = ledger
                .with_state(symbol, htf, |s| s.bar_window.back().cloned())
                .flatten();
            if let Some(htf_bar) = latest {
                // Override close/high/low with the base bar so the HTF the
                // strategy sees matches the exact final tick — no stale-close
                // artifacts when the HTF feed lags slightly.
                let merged = Bar::new(
                    htf_bar.timestamp,
                    &htf_bar.symbol,
                    htf_bar.open,
                    htf_bar.high.max(bar.high),
                    htf_bar.low.min(bar.low),
                    bar.close,
                    htf_bar.volume,
                );
                htf_events.push((htf, merged));
            }
        }
    }
    // Largest TF first — matches MtfEngine heap tie-breaker order.
    htf_events.sort_by(|a, b| b.0.duration_ms().cmp(&a.0.duration_ms()));

    let mut events_owned: Vec<(Timeframe, Bar)> = htf_events;
    events_owned.push((tf, bar.clone()));

    let tf_bar_events: Vec<TfBarEvent<'_>> = events_owned
        .iter()
        .map(|(t, b)| TfBarEvent { tf: *t, bar: b })
        .collect();

    let empty_views: HashMap<Timeframe, TfView<'_>> = HashMap::new();

    let snap = MtfSnapshot {
        base_tf: tf,
        close_ts: bar.timestamp + tf.duration_ms(),
        events: &tf_bar_events,
        views: &empty_views,
    };
    strategy.on_bars(snap)
}

// ── v2 warmup ─────────────────────────────────────────────────────────────────

/// Replay ledger history into `strategy` to warm up indicator state.
///
/// Two phases, both anchored on the hand's own `target_tf` (not any
/// ledger-wide "base" feed):
///
/// 1. **Pre-window HTF backlog** — every declared HTF bar that closed
///    *before* `target_tf`'s own window even starts gets fed as an HTF-only
///    tick (no base-TF event in the snapshot). `MtfScriptStrategy::on_bars`
///    is built to handle this: it always advances HTF-side indicator
///    bindings first, then only evaluates the script body if a base bar is
///    present (`snap.base_bar()` — see `script/v2/strategy.rs`). Skipping
///    this phase (an earlier version of this function did) leaves any
///    `ind.X(period, "HTF")` binding cold, since `target_tf`'s window is
///    almost always far shorter in calendar time than a longer-period HTF's
///    own window (both ledger rings are bootstrapped to a fixed *bar count*
///    of their own granularity, so the HTF one covers much more real time
///    per bar) — there just aren't enough in-window HTF closes to warm it
///    otherwise.
/// 2. **In-window merge-join** — once past `window_start_ts`, deliver each
///    `target_tf` bar together with any HTF bars that closed at or before
///    it, same shape as live evaluation (`on_bar_v2`), just walking stored
///    history instead of being driven by live ticks. Unlike `on_bar_v2`,
///    which always reads the ledger's *latest* HTF bar (correct only
///    because live time has actually elapsed to match), replay must thread
///    through the real historical HTF sequence — reusing `on_bar_v2` here
///    would seed every historical alignment point with today's single
///    latest HTF bar instead of the one that was actually current then.
///
/// The split at `window_start_ts` is also what keeps phase 2 paced at one
/// HTF bar per real alignment point instead of dumping the whole backlog
/// into the first `on_bars()` call — phase 1 already drained everything
/// older.
fn warmup_v2(
    strategy: &mut dyn MtfStrategy,
    symbol: &str,
    target_tf: Timeframe,
    declared_htfs: &[Timeframe],
    ledger: &Arc<Ledger>,
) {
    let target_bars: Vec<Bar> = ledger
        .with_state(symbol, target_tf, |s| s.bar_window.iter().cloned().collect())
        .unwrap_or_default();

    let Some(first_bar) = target_bars.first() else {
        tracing::warn!(
            %symbol, %target_tf,
            "v2 warmup: no history at hand's own target_tf in ledger — strategy starts cold; \
             indicators will not be warmed until the ledger window fills up. \
             Check that parquet bootstrap / gap-fill completed before the first hand was registered."
        );
        return;
    };
    let window_start_ts = first_bar.timestamp;
    let empty_views: HashMap<Timeframe, TfView<'_>> = HashMap::new();

    // ── Phase 1: pre-window HTF backlog, HTF-only ticks (no base bar) ──────
    let mut pre_window: Vec<(Timeframe, Bar)> = Vec::new();
    for &htf in declared_htfs {
        let htf_dur = htf.duration_ms();
        let bars: Vec<Bar> = ledger
            .with_state(symbol, htf, |s| s.bar_window.iter().cloned().collect())
            .unwrap_or_default();
        pre_window.extend(
            bars.into_iter()
                .filter(|b| b.timestamp + htf_dur < window_start_ts)
                .map(|b| (htf, b)),
        );
    }
    // Chronological by close time; ties (simultaneous HTF closes) largest-TF
    // first, matching the ordering `on_bar_v2`/live use.
    pre_window.sort_by(|(tf_a, a), (tf_b, b)| {
        let close_a = a.timestamp + tf_a.duration_ms();
        let close_b = b.timestamp + tf_b.duration_ms();
        close_a.cmp(&close_b).then_with(|| tf_b.duration_ms().cmp(&tf_a.duration_ms()))
    });
    for (htf, bar) in &pre_window {
        let ev = [TfBarEvent { tf: *htf, bar }];
        let snap = MtfSnapshot {
            base_tf: target_tf,
            close_ts: bar.timestamp + htf.duration_ms(),
            events: &ev,
            views: &empty_views,
        };
        let _ = strategy.on_bars(snap);
    }

    // ── Phase 2: in-window merge-join (base_tf bar + due HTF bars) ─────────
    let mut htf_remaining: HashMap<Timeframe, std::collections::VecDeque<Bar>> = declared_htfs
        .iter()
        .map(|&htf| {
            let htf_dur = htf.duration_ms();
            let bars: std::collections::VecDeque<Bar> = ledger
                .with_state(symbol, htf, |s| s.bar_window.iter().cloned().collect::<Vec<_>>())
                .unwrap_or_default()
                .into_iter()
                .filter(|b| b.timestamp + htf_dur >= window_start_ts)
                .collect();
            (htf, bars)
        })
        .collect();

    for base_bar in &target_bars {
        let close_ts = base_bar.timestamp + target_tf.duration_ms();

        // Deliver HTF bars whose close is at or before this base close.
        let mut htf_events: Vec<(Timeframe, Bar)> = Vec::new();
        for (&htf, deq) in &mut htf_remaining {
            let htf_dur = htf.duration_ms();
            while let Some(front) = deq.front() {
                let htf_close = front.timestamp + htf_dur;
                if htf_close <= close_ts {
                    let htf_bar = deq.pop_front().unwrap();
                    htf_events.push((htf, htf_bar));
                } else {
                    break;
                }
            }
        }
        htf_events.sort_by(|a, b| b.0.duration_ms().cmp(&a.0.duration_ms()));

        let mut events_owned: Vec<(Timeframe, Bar)> = htf_events;
        events_owned.push((target_tf, base_bar.clone()));

        let tf_bar_events: Vec<TfBarEvent<'_>> = events_owned
            .iter()
            .map(|(tf, b)| TfBarEvent { tf: *tf, bar: b })
            .collect();

        let snap = MtfSnapshot {
            base_tf: target_tf,
            close_ts,
            events: &tf_bar_events,
            views: &empty_views,
        };
        let _ = strategy.on_bars(snap);
    }
}

// ── SymbolGroup ───────────────────────────────────────────────────────────────

/// All hands watching the same symbol. The bar window is NOT stored here —
/// it lives in the ledger and is read on demand in `evaluate`.
pub struct SymbolGroup {
    pub symbol: String,
    hands: Vec<Handle>,
}

impl SymbolGroup {
    pub fn new(symbol: String) -> Self {
        Self { symbol, hands: Vec::new() }
    }

    /// Insert / replace a hand. Returns whether any hand was replaced.
    pub fn add(&mut self, hand: Handle) {
        self.hands.retain(|h| h.hand_id != hand.hand_id);
        self.hands.push(hand);
    }

    /// Remove a hand by id.
    pub fn remove(&mut self, hand_id: &str) {
        self.hands.retain(|h| {
            if h.hand_id == hand_id {
                info!(
                    hand_id = %h.hand_id, helm_id = %h.helm_id,
                    symbol = %self.symbol, target_tf = %h.target_tf,
                    "hand deregistered"
                );
                false
            } else { true }
        });
    }

    /// Drain every hand.
    pub fn drain_all(&mut self) {
        self.hands.clear();
    }

    pub fn is_empty(&self) -> bool {
        self.hands.is_empty()
    }

    pub fn len(&self) -> usize {
        self.hands.len()
    }

    /// Count hands that have at least one hand registered (any TF).
    pub fn count_all(&self) -> usize {
        self.hands.len()
    }

    /// Run every hand whose `target_tf` matches `tf`, passing the just-closed
    /// bar.  Returns one entry per emitting hand.
    pub fn evaluate_all(&mut self, bar: &Bar, tf: Timeframe) -> Vec<(String, String, Signal)> {
        let mut results = Vec::new();
        for h in self.hands.iter_mut() {
            if h.target_tf != tf {
                continue;
            }
            let strategy_type = match &h.live {
                LiveStrategy::V1 { .. } => "v1",
                LiveStrategy::V2 { .. } => "v2",
            };
            let bot_span = tracing::info_span!(
                "hand.eval",
                hand_id = %h.hand_id,
                strategy_type = strategy_type,
            );
            let _bot_enter = bot_span.enter();

            let bot_start = std::time::Instant::now();
            let sigs = h.on_bar_base(bar);
            let bot_us = bot_start.elapsed().as_micros();

            metrics::histogram!(
                "herald_registry_hand_eval_us",
                "symbol" => self.symbol.clone(),
                "tf"     => h.target_tf.to_string(),
            ).record(bot_us as f64);

            if let Some(sig) = sigs.into_iter().next() {
                trace!(
                    hand_id = %h.hand_id, helm_id = %h.helm_id,
                    symbol = %self.symbol, direction = ?sig.direction,
                    strength = sig.strength, bot_us,
                    "hand evaluate → signal"
                );
                results.push((h.hand_id.clone(), h.helm_id.clone(), sig));
            } else {
                trace!(
                    hand_id = %h.hand_id, symbol = %self.symbol,
                    bot_us,
                    "hand evaluate → DoNothing"
                );
            }
        }
        results
    }

    pub fn hand_infos(&self) -> impl Iterator<Item = &Handle> {
        self.hands.iter()
    }
}

#[cfg(test)]
mod warmup_v2_tests {
    use super::*;
    use alm_core::regime::RegimeState;
    use alm_ledger::LedgerConfig;

    fn mk_bar(t: i64, c: f64) -> Bar {
        Bar::new(t, "BTCUSDT", c, c, c, c, 1.0)
    }

    /// Records the shape of every `on_bars` call it receives — how many HTF
    /// events came bundled with the base bar, and each HTF bar's own
    /// timestamp — without running any real strategy logic.
    struct SpyStrategy {
        /// One entry per `on_bars` call: (base bar ts, HTF bar timestamps seen).
        calls: Vec<(i64, Vec<i64>)>,
    }

    impl MtfStrategy for SpyStrategy {
        fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> {
            let base_ts = snap
                .events
                .iter()
                .find(|e| e.tf == snap.base_tf)
                .map(|e| e.bar.timestamp)
                .unwrap_or(-1);
            let htf_ts: Vec<i64> = snap
                .events
                .iter()
                .filter(|e| e.tf != snap.base_tf)
                .map(|e| e.bar.timestamp)
                .collect();
            self.calls.push((base_ts, htf_ts));
            vec![]
        }
        fn name(&self) -> &str { "spy" }
        fn reset(&mut self) {}
        fn current_regime(&self) -> Option<&RegimeState> { None }
    }

    /// Reproduces the scenario that used to either dump the whole HTF
    /// backlog into the first `on_bars()` call, or (an intermediate,
    /// also-wrong fix) discard it outright and leave HTF-side indicators
    /// cold: an H4 ledger window that reaches calendar-further back than the
    /// M15 window (same bar-count convention, wildly different calendar
    /// depth per TF).
    ///
    /// Correct behavior: the 50-bar backlog is fed as HTF-only ticks (phase
    /// 1, warms indicator state, no base bar / no signal evaluation), then
    /// exactly one M15 bar in the merge-join (phase 2) carries the one H4
    /// bar that genuinely falls inside the M15 window — no call ever carries
    /// more than one HTF bar, and every HTF bar delivered is the historically
    /// correct one, never "whatever is latest in the ledger".
    #[test]
    fn warmup_v2_paces_htf_bars_and_still_warms_pre_window_backlog() {
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let m15_ms = Timeframe::M15.duration_ms();
        let h4_ms = Timeframe::H4.duration_ms();
        let w = 1_000 * h4_ms; // window_start_ts — arbitrary anchor

        // M15 window: 20 bars covering 5h (> 1 H4 period), starting at `w`.
        for i in 0..20i64 {
            ledger.advance(Timeframe::M15, mk_bar(w + i * m15_ms, 100.0 + i as f64)).unwrap();
        }

        // Backlog: 50 H4 bars all closing *strictly before* `w` (close = w -
        // k*h4_ms, k=1..=50, oldest first) — this is the pre-window history
        // that used to get dumped in full into the first replayed M15 bar,
        // and which the clip-only fix wrongly discarded entirely.
        let mut backlog_closes = Vec::new();
        for k in (1..=50i64).rev() {
            let open_ts = w - (k + 1) * h4_ms; // close = open_ts + h4_ms = w - k*h4_ms
            backlog_closes.push(open_ts + h4_ms);
            ledger.advance(Timeframe::H4, mk_bar(open_ts, 200.0 + k as f64)).unwrap();
        }
        // The one H4 bar that genuinely closes inside the M15 replay window
        // (opens at `w`, closes at `w + h4_ms`, which falls within the 5h span).
        ledger.advance(Timeframe::H4, mk_bar(w, 999.0)).unwrap();

        let mut spy = SpyStrategy { calls: Vec::new() };
        warmup_v2(&mut spy, "BTCUSDT", Timeframe::M15, &[Timeframe::H4], &ledger);

        assert_eq!(spy.calls.len(), 70, "50 HTF-only warmup ticks + 20 M15 replay ticks");

        // Phase 1: first 50 calls are HTF-only (no base bar), chronological,
        // one bar each — the backlog that must NOT be discarded.
        let (phase1, phase2) = spy.calls.split_at(50);
        for (i, (base_ts, htf_ts)) in phase1.iter().enumerate() {
            assert_eq!(*base_ts, -1, "call {i}: pre-window tick must carry no base bar");
            assert_eq!(htf_ts.len(), 1, "call {i}: expected exactly one HTF bar");
            assert_eq!(htf_ts[0] + h4_ms, backlog_closes[i], "call {i}: wrong backlog bar / out of order");
        }

        // Phase 2: one on_bars() call per M15 bar, no call carries more than
        // one HTF bar (no dump), and exactly one carries the in-window H4 bar.
        assert_eq!(phase2.len(), 20);
        for (base_ts, htf_ts) in phase2 {
            assert!(
                htf_ts.len() <= 1,
                "base_ts={base_ts}: expected at most 1 HTF bar per call, got {htf_ts:?} — backlog dump regression"
            );
        }
        let with_htf: Vec<_> = phase2.iter().filter(|(_, h)| !h.is_empty()).collect();
        assert_eq!(with_htf.len(), 1, "exactly one M15 bar should align with the in-window H4 close");
        assert_eq!(with_htf[0].1[0], w, "must carry the historically-correct H4 bar, not backlog or latest");
    }

    #[test]
    fn warmup_v2_empty_target_history_starts_cold_without_panic() {
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let mut spy = SpyStrategy { calls: Vec::new() };
        warmup_v2(&mut spy, "BTCUSDT", Timeframe::M15, &[Timeframe::H4], &ledger);
        assert!(spy.calls.is_empty());
    }
}
