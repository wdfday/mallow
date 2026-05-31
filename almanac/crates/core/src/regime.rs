use serde::{Deserialize, Serialize};

/// One dimension of a market regime — a raw indicator value plus a user-defined status label.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct RegimeDimension {
    /// Raw indicator value driving this dimension (e.g. ADX=28.4, ATR_ratio=1.3, vol_ratio=0.8).
    pub value: f64,
    /// User-defined classification label (e.g. "trending", "high", "thin").
    /// Empty string means the dimension has not been set / is unknown.
    pub status: String,
}

impl RegimeDimension {
    pub fn new(value: f64, status: impl Into<String>) -> Self {
        Self { value, status: status.into() }
    }

    pub fn unknown() -> Self {
        Self { value: 0.0, status: String::new() }
    }

    pub fn is(&self, s: &str) -> bool { self.status == s }
    pub fn is_known(&self) -> bool { !self.status.is_empty() }
}

/// Two-dimensional market regime state.
///
/// Each dimension carries both the raw indicator value and a user-defined status label,
/// allowing scripts to classify markets however they choose without being locked into
/// hardcoded thresholds.
///
/// Liquidity was dropped as a regime dimension: it belongs to market microstructure
/// (spread, order-book depth) which OHLCV bars cannot measure — see
/// `docs/strategy-regime-agentic-design.md`.
///
/// Produced by the regime script (if configured) and pushed into the main strategy
/// script scope as flattened variables: `trend`, `trend_value`, `vol`, `vol_value`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct RegimeState {
    pub trend:      RegimeDimension,
    pub volatility: RegimeDimension,
}

impl RegimeState {
    pub fn new(trend: RegimeDimension, volatility: RegimeDimension) -> Self {
        Self { trend, volatility }
    }

    pub fn unknown() -> Self {
        Self {
            trend:      RegimeDimension::unknown(),
            volatility: RegimeDimension::unknown(),
        }
    }

    /// Human-readable label, e.g. `"trending/high"`.
    pub fn label(&self) -> String {
        format!("{}/{}", self.trend.status, self.volatility.status)
    }
}

/// Aggregated regime statistics from a completed backtest.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct RegimeSummary {
    /// Regime transitions: `(timestamp_ms, label)`, on-change only.
    pub changes: Vec<(i64, String)>,
    /// Trade breakdown by regime label: label → (trades, win_rate_pct, avg_return_pct).
    pub trade_breakdown: Vec<RegimeTradeStats>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RegimeTradeStats {
    pub label:           String,
    pub trades:          usize,
    pub win_rate_pct:    f64,
    pub avg_return_pct:  f64,
    pub profit_factor:   f64,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn label_format() {
        let r = RegimeState::new(
            RegimeDimension::new(28.0, "trending"),
            RegimeDimension::new(1.3, "high"),
        );
        assert_eq!(r.label(), "trending/high");
    }

    #[test]
    fn dimension_is() {
        let d = RegimeDimension::new(28.0, "trending");
        assert!(d.is("trending"));
        assert!(!d.is("ranging"));
    }

    #[test]
    fn unknown() {
        let r = RegimeState::unknown();
        assert!(!r.trend.is_known());
        assert_eq!(r.label(), "/");
    }
}
