//! Multi-TF bar resampler pool.
//!
//! `SymbolResamplers` maintains one [`TimeBarResampler`] per `(symbol, HTF)`
//! so the herald handler can aggregate base-TF bars into higher timeframes
//! and advance those slices of the [`alm_ledger::Ledger`] in parallel.
//!
//! # Usage
//!
//! ```text
//! let mut rs = SymbolResamplers::new(Timeframe::M1);
//! // on every M1 bar:
//! for (htf, htf_bar) in rs.push(&m1_bar) {
//!     ledger.advance(htf, htf_bar)?;
//! }
//! // peek forming bar (for live_ semantics):
//! let forming_h1 = rs.peek("BTCUSDT", Timeframe::H1);
//! ```

use std::collections::HashMap;

use alm_core::{Bar, Timeframe};
use alm_strategy::bar_resampler::TimeBarResampler;

// ── Canonical HTF set (ascending order) ──────────────────────────────────────

/// All standard resampling targets, from the smallest to the largest.
/// At construction time, only those **strictly above** the base TF are kept.
///
/// H2, H6, H8, H12, MN are included so that strategies and watch entries
/// using those timeframes get live ledger state without extra config.
const ALL_LEVELS: &[Timeframe] = &[
    Timeframe::M3,
    Timeframe::M5,
    Timeframe::M10,
    Timeframe::M15,
    Timeframe::M30,
    Timeframe::H1,
    Timeframe::H2,
    Timeframe::H4,
    Timeframe::H6,
    Timeframe::H8,
    Timeframe::H12,
    Timeframe::D1,
    Timeframe::W1,
    Timeframe::MN,
];

// ── SymbolResamplers ──────────────────────────────────────────────────────────

/// Per-symbol resampler pool for all timeframes above the configured base TF.
///
/// When the base TF is M1, this tracks M3 / M5 / M10 / M15 / M30 / H1 / H2
/// / H4 / H6 / H8 / H12 / D1 / W1 / MN.
/// When the base TF is H1, only H2 / H4 / H6 / … are tracked.
pub struct SymbolResamplers {
    /// TF values kept, ordered ascending. Parallel to `intervals`.
    levels: Vec<Timeframe>,
    /// Interval in ms for each level. Parallel to `levels`.
    intervals: Vec<i64>,
    /// Per-symbol ring of resamplers, parallel to `levels`.
    by_symbol: HashMap<String, Vec<TimeBarResampler>>,
}

impl SymbolResamplers {
    /// Create with all standard HTFs strictly above `base_tf`.
    pub fn new(base_tf: Timeframe) -> Self {
        let base_ms = base_tf.duration_ms();
        let mut levels = Vec::new();
        let mut intervals = Vec::new();
        for &tf in ALL_LEVELS {
            let ms = tf.duration_ms();
            if ms > base_ms {
                levels.push(tf);
                intervals.push(ms);
            }
        }
        Self { levels, intervals, by_symbol: HashMap::new() }
    }

    /// Feed one base-TF bar. Returns any completed HTF bars as `(tf, bar)`.
    ///
    /// Lazily creates per-symbol resamplers on first call for that symbol.
    pub fn push(&mut self, bar: &Bar) -> Vec<(Timeframe, Bar)> {
        if self.levels.is_empty() {
            return Vec::new();
        }

        // Lazy init — split borrow avoids closure capture of `self`.
        if !self.by_symbol.contains_key(&bar.symbol) {
            let rss = self.intervals
                .iter()
                .map(|&ms| TimeBarResampler::new(ms))
                .collect::<Vec<_>>();
            self.by_symbol.insert(bar.symbol.clone(), rss);
        }

        let levels = &self.levels;
        let resamplers = self.by_symbol.get_mut(&bar.symbol).unwrap();
        let mut out = Vec::new();
        for (i, rs) in resamplers.iter_mut().enumerate() {
            if let Some(htf_bar) = rs.push(bar) {
                out.push((levels[i], htf_bar));
            }
        }
        out
    }

    /// Peek the currently-forming (incomplete) bar for `(symbol, tf)`.
    ///
    /// Returns `None` when the symbol has never been seen, or when `tf`
    /// is not in the active level set.
    pub fn peek(&self, symbol: &str, tf: Timeframe) -> Option<Bar> {
        let idx = self.levels.iter().position(|t| *t == tf)?;
        self.by_symbol.get(symbol)?.get(idx)?.peek()
    }

    /// Iterator over the actively-tracked timeframes (above base TF).
    pub fn active_tfs(&self) -> impl Iterator<Item = Timeframe> + '_ {
        self.levels.iter().copied()
    }
}
