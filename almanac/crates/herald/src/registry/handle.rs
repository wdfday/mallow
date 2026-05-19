use std::collections::HashMap;
use std::sync::Arc;

use anyhow::{anyhow, Result};
use alm_core::{signal::Signal, strategy::Strategy, Bar, Timeframe};
use alm_core::{MtfSnapshot, MtfStrategy, TfBarEvent, TfView};
use alm_ledger::{IndicatorHandle, IndicatorSpec, Ledger};
use alm_strategy::factory::{build_strategy_with_deps, IndicatorDep};
use alm_strategy::{probe_script_htfs, MtfScriptStrategy};
use tracing::{debug, trace, warn};

// ── LiveStrategy ──────────────────────────────────────────────────────────────

/// Discriminates between the v1 (pure single-TF) and v2 (MtfScriptStrategy
/// driven by real ledger HTF windows) execution paths.
enum LiveStrategy {
    /// v1: pure single-TF strategy. Receives every base-TF bar directly.
    /// No internal resampling — keeps memory flat and evaluation O(1).
    V1 {
        strategy: Box<dyn Strategy>,
    },
    /// v2: MtfScriptStrategy fed by confirmed HTF bars read directly from ledger.
    /// `last_htf_ts` tracks the last HTF bar timestamp fed per TF so we only
    /// push newly-confirmed bars into the strategy (no double-feed on warmup).
    V2 {
        strategy: MtfScriptStrategy,
        declared_htfs: Vec<Timeframe>,
        base_tf: Timeframe,
        /// Last bar.timestamp fed into the strategy per HTF, used to detect
        /// newly-confirmed bars on each M1 tick.
        last_htf_ts: HashMap<Timeframe, i64>,
    },
}

// ── Handle ────────────────────────────────────────────────────────────────────

pub struct Handle {
    pub hand_id: String,
    pub helm_id: String,
    pub symbol: String,
    pub script: String,
    /// The timeframe this hand evaluates at (may differ from registry base TF).
    pub target_tf: Timeframe,
    live: LiveStrategy,
    ledger: Arc<Ledger>,
    /// Live indicator handles keep each declared dependency warm in the ledger
    /// for the lifetime of the hand. On drop the handles decrement the refcount;
    /// unused cells are evicted after `LedgerConfig::drop_grace_bars`.
    pub(super) _indicator_handles: Vec<IndicatorHandle>,
}

impl Handle {
    pub fn new(
        hand_id: String,
        helm_id: String,
        symbol: String,
        script: String,
        ledger: &Arc<Ledger>,
        base_tf: Timeframe,
    ) -> Result<Self> {
        // ── v2 detection: script declares HTF indicators ──────────────────────
        //
        // Parse-only probe first — cheap, no AST compile. Only build the full
        // MtfScriptStrategy when we know the script actually needs V2.
        let htfs = probe_script_htfs(&script);
        if !htfs.is_empty() {
            let mut probe = MtfScriptStrategy::from_script_live(&script, base_tf)?;
            for &htf in &htfs {
                ledger.ensure_symbol(&symbol, htf, None);
            }
            let last_htf_ts = warmup_v2(&mut probe, &symbol, base_tf, &htfs, ledger);
            debug!(
                %hand_id, %symbol,
                base_tf = %base_tf, htfs = ?htfs,
                "v2 MTF strategy warmed from ledger"
            );
            return Ok(Self {
                hand_id,
                helm_id,
                symbol,
                script,
                target_tf: base_tf,
                live: LiveStrategy::V2 { strategy: probe, declared_htfs: htfs, base_tf, last_htf_ts },
                ledger: Arc::clone(ledger),
                _indicator_handles: vec![],
            });
        }

        // ── v1 path: pure single-TF ───────────────────────────────────────────
        let mut value = serde_json::json!({ "script": script, "_live": true });
        let (mut strategy, deps) = build_strategy_with_deps("script", &mut value)
            .map_err(|e| anyhow!("{e}"))?;
        let handles = acquire_dep_handles(ledger, &symbol, base_tf, &deps, &hand_id);

        let base_bars: Vec<Bar> = ledger
            .with_state(&symbol, base_tf, |s| s.bar_window.iter().cloned().collect())
            .unwrap_or_default();
        for bar in &base_bars {
            let _ = strategy.on_bar(bar);
        }

        debug!(
            %hand_id, %symbol,
            base_tf = %base_tf,
            "v1 strategy warmed up from ledger history"
        );

        Ok(Self {
            hand_id,
            helm_id,
            symbol,
            script,
            target_tf: base_tf,
            live: LiveStrategy::V1 { strategy },
            ledger: Arc::clone(ledger),
            _indicator_handles: handles,
        })
    }

    pub fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let sigs = match &mut self.live {
            LiveStrategy::V1 { strategy } => strategy.on_bar(bar),
            LiveStrategy::V2 { strategy, declared_htfs, base_tf, last_htf_ts } => {
                on_bar_v2(strategy, bar, *base_tf, declared_htfs, last_htf_ts, &self.symbol, &self.ledger)
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
    strategy: &mut MtfScriptStrategy,
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
    strategy: &mut MtfScriptStrategy,
    symbol: &str,
    base_tf: Timeframe,
    declared_htfs: &[Timeframe],
    ledger: &Arc<Ledger>,
) -> HashMap<Timeframe, i64> {
    let base_bars: Vec<Bar> = ledger
        .with_state(symbol, base_tf, |s| s.bar_window.iter().cloned().collect())
        .unwrap_or_default();

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

    pub fn add(&mut self, hand: Handle) {
        // Replace existing hand with same id (re-register = reconfigure).
        self.hands.retain(|h| h.hand_id != hand.hand_id);
        self.hands.push(hand);
    }

    pub fn remove(&mut self, hand_id: &str) {
        self.hands.retain(|h| h.hand_id != hand_id);
    }

    pub fn is_empty(&self) -> bool {
        self.hands.is_empty()
    }

    pub fn len(&self) -> usize {
        self.hands.len()
    }

    /// Run every hand on `bar`. Returns one entry per hand that emitted a signal.
    pub fn evaluate(&mut self, bar: &Bar) -> Vec<(String, String, Signal)> {
        self.hands
            .iter_mut()
            .filter_map(|h| {
                h.on_bar(bar).into_iter().next()
                    .map(|sig| (h.hand_id.clone(), h.helm_id.clone(), sig))
            })
            .collect()
    }

    pub fn hand_infos(&self) -> impl Iterator<Item = &Handle> {
        self.hands.iter()
    }
}

// ── helpers ───────────────────────────────────────────────────────────────────

/// Convert declared `IndicatorDep`s to live `IndicatorHandle`s anchored in the
/// ledger. Dependencies that fail to acquire are logged and skipped — they do
/// NOT abort registration.
fn acquire_dep_handles(
    ledger: &Arc<Ledger>,
    symbol: &str,
    tf: Timeframe,
    deps: &[IndicatorDep],
    hand_id: &str,
) -> Vec<IndicatorHandle> {
    let mut handles = Vec::with_capacity(deps.len());
    for dep in deps {
        let spec = match IndicatorSpec::from_config(dep.config.clone(), dep.source_tf) {
            Ok(s) => s,
            Err(e) => {
                warn!(%hand_id, config = %dep.config, err = %e,
                      "skipping indicator dep — invalid config");
                continue;
            }
        };
        match ledger.acquire_indicator(symbol, tf, spec.clone()) {
            Ok(h) => {
                debug!(%hand_id, %symbol, spec = %spec.canonical_key(),
                       "acquired ledger indicator handle");
                handles.push(h);
            }
            Err(e) => {
                warn!(%hand_id, %symbol, spec = ?spec, err = %e,
                      "skipping indicator dep — acquire failed");
            }
        }
    }
    handles
}
