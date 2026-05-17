use std::sync::Arc;

use anyhow::{anyhow, Result};
use alm_core::{signal::Signal, strategy::Strategy, Bar, Timeframe};
use alm_ledger::{IndicatorHandle, IndicatorSpec, Ledger};
use alm_strategy::bar_resampler::TimeBarResampler;
use alm_strategy::factory::{build_strategy_with_deps, IndicatorDep};
use tracing::{debug, trace, warn};

/// Maximum number of completed higher-TF bars kept in a hand's local window.
const HTF_WINDOW_SIZE: usize = 200;

// ── Handle ────────────────────────────────────────────────────────────────────

pub struct Handle {
    pub hand_id: String,
    pub helm_id: String,
    pub symbol: String,
    pub script: String,
    /// The timeframe this hand evaluates at (may differ from registry base TF).
    pub target_tf: Timeframe,
    pub(super) strategy: Box<dyn Strategy>,
    /// Live indicator handles keep each declared dependency warm in the ledger
    /// for the lifetime of the hand. On drop the handles decrement the refcount;
    /// unused cells are evicted after `LedgerConfig::drop_grace_bars`.
    pub(super) _indicator_handles: Vec<IndicatorHandle>,
    /// Per-hand higher-TF resampler. `None` when the hand operates at the
    /// registry's base TF — no aggregation needed.
    resampler: Option<TimeBarResampler>,
    /// Rolling window of completed higher-TF bars (used when `resampler` is Some).
    htf_window: Vec<Bar>,
}

impl Handle {
    pub fn new(
        hand_id: String,
        helm_id: String,
        symbol: String,
        script: String,
        ledger: &Arc<Ledger>,
        base_tf: Timeframe,
        target_tf: Timeframe,
    ) -> Result<Self> {
        let mut value = serde_json::json!({ "script": script, "_live": true });
        let (mut strategy, deps) = build_strategy_with_deps("script", &mut value)
            .map_err(|e| anyhow!("{e}"))?;
        let handles = acquire_dep_handles(ledger, &symbol, base_tf, &deps, &hand_id);

        // Warm strategy state from ledger history so any internal accumulators
        // (VWAP, consecutive-bar counters, etc.) reflect current market state.
        // Signals emitted during replay are discarded — only state matters.
        let (resampler, htf_window) = if target_tf != base_tf {
            // HTF strategy: replay completed target-TF bars directly from ledger.
            // The resampler is left fresh; it will pick up the current partial bar
            // naturally from the live feed.
            let htf_bars: Vec<Bar> = ledger
                .with_state(&symbol, target_tf, |s| s.bar_window.iter().cloned().collect())
                .unwrap_or_default();
            for bar in &htf_bars {
                let _ = strategy.on_bar(bar);
            }
            let mut window = htf_bars;
            if window.len() > HTF_WINDOW_SIZE {
                window.drain(..window.len() - HTF_WINDOW_SIZE);
            }
            (Some(TimeBarResampler::new(target_tf.duration_ms())), window)
        } else {
            // Base-TF strategy: replay base-TF bar window.
            let base_bars: Vec<Bar> = ledger
                .with_state(&symbol, base_tf, |s| s.bar_window.iter().cloned().collect())
                .unwrap_or_default();
            for bar in &base_bars {
                let _ = strategy.on_bar(bar);
            }
            (None, Vec::new())
        };

        debug!(
            %hand_id, %symbol,
            base_tf = %base_tf, target_tf = %target_tf,
            "strategy warmed up from ledger history"
        );

        Ok(Self {
            hand_id,
            helm_id,
            symbol,
            script,
            target_tf,
            strategy,
            _indicator_handles: handles,
            resampler,
            htf_window,
        })
    }

    pub fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let sigs = match &mut self.resampler {
            None => self.strategy.on_bar(bar),
            Some(rs) => match rs.push(bar) {
                None => vec![],
                Some(htf_bar) => {
                    self.htf_window.push(htf_bar.clone());
                    if self.htf_window.len() > HTF_WINDOW_SIZE {
                        self.htf_window.drain(..1);
                    }
                    let mut sigs = self.strategy.on_bar(&htf_bar);
                    sigs.extend(self.strategy.on_window(&self.htf_window));
                    sigs
                }
            },
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
