//! Position sizing / risk managers.
//!
//! Each sizer is a separate module; all implement [`alm_core::strategy::RiskManager`].
//!
//! | Type | Description |
//! |------|-------------|
//! | [`FixedFractional`] | Ralph Vince: risk f% of equity to the signal's own stop |
//! | [`PercentEquity`]   | % of cash per trade (notional allocation, no risk/stop concept) |
//! | [`AtrSizing`]       | risk-parity via `signal.atr` (fixed_fractional with stop forced to ATR) |
//! | [`EqualWeight`]     | equal equity slot per position |
//! | [`FixedUsd`]        | fixed notional per trade |
//! | [`FixedQuantity`]   | fixed unit count |
//! | [`KellySizing`]     | Kelly criterion (with fractional cap) |
//! | [`AnySizer`]        | runtime-selectable enum wrapping all of the above |

pub mod atr;
pub mod equal_weight;
pub mod fixed_fractional;
pub mod fixed_qty;
pub mod fixed_usd;
pub mod kelly;
pub mod percent_equity;

pub use atr::AtrSizing;
pub use equal_weight::EqualWeight;
pub use fixed_fractional::FixedFractional;
pub use fixed_qty::FixedQuantity;
pub use fixed_usd::FixedUsd;
pub use kelly::KellySizing;
pub use percent_equity::PercentEquity;

// ── AnySizer ──────────────────────────────────────────────────────────────────

use alm_core::{bar::Bar, portfolio::Portfolio, signal::Signal, strategy::RiskManager};

/// Runtime-selectable sizer — wraps all sizing strategies behind one concrete type.
///
/// Allows the engine to choose a sizer at runtime from request fields without
/// boxing or trait objects.
pub enum AnySizer {
    FixedFractional(FixedFractional),
    PercentEquity(PercentEquity),
    FixedUsd(FixedUsd),
    FixedQuantity(FixedQuantity),
    EqualWeight(EqualWeight),
    Atr(AtrSizing),
    Kelly(KellySizing),
}

impl RiskManager for AnySizer {
    fn validate(&self, signal: &Signal, portfolio: &Portfolio) -> bool {
        match self {
            Self::FixedFractional(s) => s.validate(signal, portfolio),
            Self::PercentEquity(s)   => s.validate(signal, portfolio),
            Self::FixedUsd(s)        => s.validate(signal, portfolio),
            Self::FixedQuantity(s)   => s.validate(signal, portfolio),
            Self::EqualWeight(s)     => s.validate(signal, portfolio),
            Self::Atr(s)             => s.validate(signal, portfolio),
            Self::Kelly(s)           => s.validate(signal, portfolio),
        }
    }

    fn size(&self, signal: &Signal, portfolio: &Portfolio, price: f64) -> f64 {
        match self {
            Self::FixedFractional(s) => s.size(signal, portfolio, price),
            Self::PercentEquity(s)   => s.size(signal, portfolio, price),
            Self::FixedUsd(s)        => s.size(signal, portfolio, price),
            Self::FixedQuantity(s)   => s.size(signal, portfolio, price),
            Self::EqualWeight(s)     => s.size(signal, portfolio, price),
            Self::Atr(s)             => s.size(signal, portfolio, price),
            Self::Kelly(s)           => s.size(signal, portfolio, price),
        }
    }

    fn on_bar(&mut self, _bar: &Bar) {
        // All sizers read from signal; no internal indicator state to update.
    }
}
