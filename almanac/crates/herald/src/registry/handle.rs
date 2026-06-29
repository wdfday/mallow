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
    pub exchange: String, // "binance" | "okx" | "alpaca" | "" (empty = backtest/test)
    pub is_future: bool,  // false = spot; true = perpetual/futures
    pub script: String,
    /// The timeframe this hand evaluates at (may differ from registry base TF).
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
            warmup_v2(probe.as_mut(), &symbol, target_tf, base_tf, &htfs, ledger);
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
/// Merges base-TF and HTF bar windows chronologically using alignment points:
/// for each base bar, we first deliver any HTF bars that closed at or before it
/// (using the ledger-resident HTF windows), then the base bar itself.
fn warmup_v2(
    strategy: &mut dyn MtfStrategy,
    symbol: &str,
    eval_tf: Timeframe,
    base_tf: Timeframe,
    declared_htfs: &[Timeframe],
    ledger: &Arc<Ledger>,
) {
    let base_bars: Vec<Bar> = ledger
        .with_state(symbol, base_tf, |s| s.bar_window.iter().cloned().collect())
        .unwrap_or_default();

    if base_bars.is_empty() {
        tracing::warn!(
            %symbol, %base_tf,
            "v2 warmup: no base-TF history in ledger — strategy starts cold; \
             indicators will not be warmed until the ledger window fills up. \
             Check that parquet bootstrap completed before the first hand was registered."
        );
        return;
    }

    // Load HTF windows from ledger into mutable queues for consumption.
    let mut htf_remaining: HashMap<Timeframe, std::collections::VecDeque<Bar>> = declared_htfs
        .iter()
        .map(|&htf| {
            let bars: std::collections::VecDeque<Bar> = ledger
                .with_state(symbol, htf, |s| s.bar_window.iter().cloned().collect::<Vec<_>>())
                .map(std::collections::VecDeque::from)
                .unwrap_or_default();
            (htf, bars)
        })
        .collect();

    let empty_views: HashMap<Timeframe, TfView<'_>> = HashMap::new();

    for base_bar in &base_bars {
        let m1_close = base_bar.timestamp + base_tf.duration_ms();

        // Deliver HTF bars whose close is at or before this base close.
        let mut htf_events: Vec<(Timeframe, Bar)> = Vec::new();
        for (&htf, deq) in &mut htf_remaining {
            let htf_dur = htf.duration_ms();
            while let Some(front) = deq.front() {
                let htf_close = front.timestamp + htf_dur;
                if htf_close <= m1_close {
                    let htf_bar = deq.pop_front().unwrap();
                    htf_events.push((htf, htf_bar));
                } else {
                    break;
                }
            }
        }
        htf_events.sort_by(|a, b| b.0.duration_ms().cmp(&a.0.duration_ms()));

        let mut events_owned: Vec<(Timeframe, Bar)> = htf_events;
        events_owned.push((eval_tf, base_bar.clone()));

        let tf_bar_events: Vec<TfBarEvent<'_>> = events_owned
            .iter()
            .map(|(tf, b)| TfBarEvent { tf: *tf, bar: b })
            .collect();

        let snap = MtfSnapshot {
            base_tf: eval_tf,
            close_ts: m1_close,
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
    #[allow(dead_code)]
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
                    "bot evaluate → signal"
                );
                results.push((h.hand_id.clone(), h.helm_id.clone(), sig));
            } else {
                trace!(
                    hand_id = %h.hand_id, symbol = %self.symbol,
                    bot_us,
                    "bot evaluate → DoNothing"
                );
            }
        }
        results
    }

    pub fn hand_infos(&self) -> impl Iterator<Item = &Handle> {
        self.hands.iter()
    }
}
