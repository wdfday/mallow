use std::collections::HashMap;
use std::sync::Arc;

use anyhow::{anyhow, Result};
use alm_core::{signal::Signal, strategy::Strategy, Bar, Timeframe};
use alm_core::{MtfSnapshot, MtfStrategy, TfBarEvent, TfView};
use alm_ledger::Ledger;
use alm_data::aggregator::StandaloneAggregator;
use alm_strategy::{probe_script_htfs, ScriptStrategy, MtfScriptStrategy};
use tracing::{debug, info, trace};

// ── LiveStrategy ──────────────────────────────────────────────────────────────

/// Discriminates between the v1 (pure single-TF) and v2 (MtfScriptStrategy
/// driven by real ledger HTF windows) execution paths.
enum LiveStrategy {
    /// v1: pure single-TF strategy. Receives every base-TF bar directly.
    /// No internal resampling — keeps memory flat and evaluation O(1).
    V1 {
        strategy: Box<dyn Strategy>,
    },
    /// v2: multi-TF strategy fed by confirmed HTF bars read directly from ledger.
    /// `last_htf_ts` tracks the last HTF bar timestamp fed per TF so we only
    /// push newly-confirmed bars into the strategy (no double-feed on warmup).
    V2 {
        strategy: Box<dyn MtfStrategy>,
        declared_htfs: Vec<Timeframe>,
        /// The strategy's own base TF — equals `Handle::target_tf`, which is
        /// the TF this hand evaluates at. Distinct from the *ledger* base TF
        /// (always M1 in production), which is the registry's `base_tf` field.
        /// Named `eval_tf` here to prevent the two from being confused at call sites.
        eval_tf: Timeframe,
        /// Last bar.timestamp fed into the strategy per HTF, used to detect
        /// newly-confirmed bars on each M1 tick.
        last_htf_ts: HashMap<Timeframe, i64>,
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
    /// Resample subscriptions this hand acquired at register time. Released
    /// when the hand is removed from the registry. Each tuple is
    /// `(source_tf, target_tf)` matching what `Registry::register` called
    /// `resample.ensure` with.
    pub(super) resample_subs: Vec<(Timeframe, Timeframe)>,
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
            let last_htf_ts = warmup_v2(probe.as_mut(), &symbol, target_tf, &htfs, ledger);
            debug!(
                %hand_id, %symbol,
                base_tf = %base_tf, target_tf = %target_tf, htfs = ?htfs,
                "v2 MTF strategy warmed from ledger"
            );
            // V2 subscriptions: target_tf (if > base) + every declared HTF (if > base).
            let mut resample_subs: Vec<(Timeframe, Timeframe)> = Vec::new();
            if target_tf != base_tf {
                resample_subs.push((base_tf, target_tf));
            }
            for htf in &htfs {
                if *htf != base_tf {
                    resample_subs.push((base_tf, *htf));
                }
            }
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
                    last_htf_ts,
                },
                ledger: Arc::clone(ledger),
                resample_subs,
            });
        }

        // ── v1 path: pure single-TF, hand evaluates at target_tf ─────────────
        let mut strategy: Box<dyn Strategy> =
            Box::new(ScriptStrategy::from_script_live(&script).map_err(|e| anyhow!("{e}"))?);

        // Warm the strategy from ledger history at `target_tf`. Two cases:
        //
        // 1. `target_tf == base_tf` — feed the existing window directly.
        // 2. `target_tf > base_tf` and the ledger already has HTF bars (e.g.
        //    from a parallel V2 hand or an external HTF feed) — use them.
        // 3. `target_tf > base_tf` and the HTF window is empty (most common
        //    on first registration after restart) — aggregate the BASE-TF
        //    history into one-shot HTF bars via `StandaloneAggregator` so
        //    the strategy lands ready instead of waiting up to one full
        //    bucket period before the first signal.
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
                // One-shot aggregate base history → HTF bars in memory. We
                // don't push these into the ledger to avoid racing with the
                // ResampleManager subscription that will be wired right after.
                let base_window: Vec<Bar> = ledger
                    .with_state(&symbol, base_tf, |s| s.bar_window.iter().cloned().collect())
                    .unwrap_or_default();
                let mut agg = StandaloneAggregator::new(target_tf);
                let mut out = Vec::new();
                for b in &base_window {
                    if let Some(htf) = agg.update(b) {
                        out.push(htf);
                    }
                }
                out
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

        // V1 only needs a subscription if target_tf > base_tf (script-declared
        // HTFs were rejected by V1 build, so no HTF leak here).
        let resample_subs: Vec<(Timeframe, Timeframe)> = if target_tf != base_tf {
            vec![(base_tf, target_tf)]
        } else {
            Vec::new()
        };
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
            resample_subs,
        })
    }

    pub fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let sigs = match &mut self.live {
            // V1: single-TF — bar arrives already at target_tf (either base TF
            // or a resampled HTF bar from ResampleManager). Push straight through;
            // no ledger look-up needed because the strategy maintains its own
            // rolling indicator state internally.
            LiveStrategy::V1 { strategy } => strategy.on_bar(bar),

            // V2: multi-TF — before feeding the base-TF bar we check the ledger
            // for any newly-confirmed HTF bars and deliver them first (largest TF
            // first), then the base bar. See `on_bar_v2` for the full ordering.
            LiveStrategy::V2 { strategy, declared_htfs, eval_tf, last_htf_ts } => {
                on_bar_v2(strategy.as_mut(), bar, *eval_tf, declared_htfs, last_htf_ts, &self.symbol, &self.ledger)
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

fn on_bar_v2(
    strategy: &mut dyn MtfStrategy,
    bar: &Bar,
    base_tf: Timeframe,
    declared_htfs: &[Timeframe],
    last_htf_ts: &mut HashMap<Timeframe, i64>,
    symbol: &str,
    ledger: &Arc<Ledger>,
) -> Vec<Signal> {
    // Collect HTF bars newly confirmed in ledger since last evaluation.
    // "Query the H1 ring, fallback = implicit (binding keeps last confirmed)."
    let mut htf_events: Vec<(Timeframe, Bar)> = Vec::new();
    for &htf in declared_htfs {
        let latest = ledger
            .with_state(symbol, htf, |s| s.bar_window.back().cloned())
            .flatten();
        if let Some(htf_bar) = latest {
            let last = last_htf_ts.entry(htf).or_insert(-1);
            if htf_bar.timestamp > *last {
                *last = htf_bar.timestamp;
                htf_events.push((htf, htf_bar));
            }
        }
    }
    // Largest TF first — matches MtfEngine heap tie-breaker order.
    htf_events.sort_by(|a, b| b.0.duration_ms().cmp(&a.0.duration_ms()));

    // Build owned event list: HTF events + base bar.
    let mut events_owned: Vec<(Timeframe, Bar)> = htf_events;
    events_owned.push((base_tf, bar.clone()));

    let tf_bar_events: Vec<TfBarEvent<'_>> = events_owned
        .iter()
        .map(|(tf, b)| TfBarEvent { tf: *tf, bar: b })
        .collect();

    // MtfScriptStrategy does not use views (it maintains its own binding state).
    // Pass empty map — consistent with backtest path where views are ledger-owned.
    let empty_views: HashMap<Timeframe, TfView<'_>> = HashMap::new();

    let snap = MtfSnapshot {
        base_tf,
        close_ts: bar.timestamp + base_tf.duration_ms(),
        events: &tf_bar_events,
        views: &empty_views,
    };
    strategy.on_bars(snap)
}

// ── v2 warmup ─────────────────────────────────────────────────────────────────

/// Replay ledger history into `strategy` to warm up indicator state.
///
/// Merges base-TF and HTF bar windows chronologically: for each base bar,
/// we first deliver any HTF bars that closed at or before it, then the base
/// bar itself — matching the live ordering guarantee (HTFs advance before M1
/// observer fires).
///
/// Returns `last_htf_ts` initialized to the last HTF bar timestamp fed, so
/// the live `on_bar_v2` path doesn't re-feed them.
fn warmup_v2(
    strategy: &mut dyn MtfStrategy,
    symbol: &str,
    base_tf: Timeframe,
    declared_htfs: &[Timeframe],
    ledger: &Arc<Ledger>,
) -> HashMap<Timeframe, i64> {
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
        return HashMap::new();
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

    let mut last_htf_ts: HashMap<Timeframe, i64> = HashMap::new();
    let empty_views: HashMap<Timeframe, TfView<'_>> = HashMap::new();

    for base_bar in &base_bars {
        let m1_close = base_bar.timestamp + base_tf.duration_ms();

        // Deliver HTF bars whose close is at or before this M1 close.
        let mut htf_events: Vec<(Timeframe, Bar)> = Vec::new();
        for (&htf, deq) in &mut htf_remaining {
            let htf_dur = htf.duration_ms();
            while let Some(front) = deq.front() {
                let htf_close = front.timestamp + htf_dur;
                if htf_close <= m1_close {
                    let htf_bar = deq.pop_front().unwrap();
                    last_htf_ts.insert(htf, htf_bar.timestamp);
                    htf_events.push((htf, htf_bar));
                } else {
                    break;
                }
            }
        }
        htf_events.sort_by(|a, b| b.0.duration_ms().cmp(&a.0.duration_ms()));

        let mut events_owned: Vec<(Timeframe, Bar)> = htf_events;
        events_owned.push((base_tf, base_bar.clone()));

        let tf_bar_events: Vec<TfBarEvent<'_>> = events_owned
            .iter()
            .map(|(tf, b)| TfBarEvent { tf: *tf, bar: b })
            .collect();

        let snap = MtfSnapshot {
            base_tf,
            close_ts: m1_close,
            events: &tf_bar_events,
            views: &empty_views,
        };
        let _ = strategy.on_bars(snap);
    }

    last_htf_ts
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

    /// Insert / replace a hand. Returns the resample subscriptions of any
    /// previously-existing hand with the same id so the caller can release
    /// them (re-register may have changed the subscription set).
    pub fn add(&mut self, hand: Handle) -> Vec<(Timeframe, Timeframe)> {
        let mut dropped = Vec::new();
        self.hands.retain(|h| {
            if h.hand_id == hand.hand_id {
                dropped.extend(h.resample_subs.iter().copied());
                false
            } else { true }
        });
        self.hands.push(hand);
        dropped
    }

    /// Remove a hand and return its resample subscriptions so the caller can
    /// release them.
    pub fn remove(&mut self, hand_id: &str) -> Vec<(Timeframe, Timeframe)> {
        let mut dropped = Vec::new();
        self.hands.retain(|h| {
            if h.hand_id == hand_id {
                info!(
                    hand_id = %h.hand_id, helm_id = %h.helm_id,
                    symbol = %self.symbol, target_tf = %h.target_tf,
                    "hand deregistered"
                );
                dropped.extend(h.resample_subs.iter().copied());
                false
            } else { true }
        });
        dropped
    }

    /// Drain every hand and return all their subscriptions for batch release.
    pub fn drain_subs(&mut self) -> Vec<(Timeframe, Timeframe)> {
        let mut subs = Vec::new();
        for h in self.hands.drain(..) {
            subs.extend(h.resample_subs);
        }
        subs
    }

    pub fn is_empty(&self) -> bool {
        self.hands.is_empty()
    }

    pub fn len(&self) -> usize {
        self.hands.len()
    }

    /// Count hands that evaluate at the given timeframe.
    pub fn count_for_tf(&self, tf: Timeframe) -> usize {
        self.hands.iter().filter(|h| h.target_tf == tf).count()
    }

    /// Run every hand whose `target_tf` matches `tf`. Other hands are skipped
    /// — they'll evaluate on their own TF's advance event. Returns one entry
    /// per emitting hand.
    pub fn evaluate_at(&mut self, tf: Timeframe, bar: &Bar) -> Vec<(String, String, Signal)> {
        let mut results = Vec::new();
        for h in self.hands.iter_mut().filter(|h| h.target_tf == tf) {
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
            let sigs = h.on_bar(bar);
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

