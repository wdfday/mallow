use serde::{Deserialize, Serialize};

/// All recognised geometric / candlestick / harmonic pattern types.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum PatternKind {
    // ── Reversal ─────────────────────────────────────────────────────────────
    HeadAndShoulders,
    InverseHeadAndShoulders,
    DoubleTop,
    DoubleBottom,
    TripleTop,
    TripleBottom,

    // ── Continuation ─────────────────────────────────────────────────────────
    AscendingTriangle,
    DescendingTriangle,
    SymmetricTriangle,
    BullFlag,
    BearFlag,
    BullPennant,
    BearPennant,
    CupAndHandle,
    /// `rising = true` → rising wedge (bearish), `false` → falling wedge (bullish)
    Wedge { rising: bool },

    // ── Harmonic XABCD ───────────────────────────────────────────────────────
    /// `bullish = true` → buy setup, `false` → sell setup
    Gartley { bullish: bool },
    Butterfly { bullish: bool },
    Bat { bullish: bool },
    Crab { bullish: bool },
    Cypher { bullish: bool },
    DeepCrab { bullish: bool },

    // ── Multi-bar candlestick ─────────────────────────────────────────────────
    Engulfing { bullish: bool },
    MorningStar,
    EveningStar,
    ThreeWhiteSoldiers,
    ThreeBlackCrows,

    // ── Single-bar candlestick ────────────────────────────────────────────────
    Doji,
    Hammer,
    InvertedHammer,
    ShootingStar,
    HangingMan,
    MarubozuBull,
    MarubozuBear,
}

/// A price pivot identified during pattern scanning (swing high or swing low).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PivotPoint {
    /// Index within the detection window (0 = oldest bar in slice).
    pub bar_index: usize,
    /// Unix-ms timestamp of the bar.
    pub timestamp: i64,
    /// Price at the pivot.
    pub price: f64,
    /// `true` = swing high, `false` = swing low.
    pub is_high: bool,
}

/// Emitted by a [`PatternDetector`] when a pattern is confirmed.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PatternSignal {
    pub kind: PatternKind,
    /// How closely the geometry matches the ideal — \[0.0, 1.0\].
    pub confidence: f64,
    /// Suggested entry (typically the close of the confirmation bar).
    pub entry_price: f64,
    /// Projected price target (measured move = pattern height).
    pub target_price: Option<f64>,
    /// Natural stop-loss level defined by the pattern geometry.
    pub stop_price: Option<f64>,
    /// Key pivot points that define the pattern.
    /// Feed these to `alm-chart` `PatternOverlay` for visualization.
    pub pivots: Vec<PivotPoint>,
    /// Unix-ms timestamp of the bar that confirmed the pattern.
    pub confirmed_at: i64,
}
