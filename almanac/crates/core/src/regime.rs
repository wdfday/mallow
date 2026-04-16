use serde::{Deserialize, Serialize};

/// Trend dimension of the current market regime.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum TrendRegime {
    /// ADX > threshold — market is making directional moves.
    Trending,
    /// Chop > threshold — market is oscillating in a range.
    Ranging,
    /// Neither strongly trending nor clearly ranging.
    Neutral,
}

/// Volatility dimension of the current market regime.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum VolRegime {
    /// ATR(short) / ATR(long) > high_ratio — volatility expanding.
    High,
    /// ATR(short) / ATR(long) < low_ratio — volatility compressing.
    Low,
    /// Volatility is within normal historical range.
    Normal,
}

/// Combined two-dimensional market regime state.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct RegimeState {
    pub trend: TrendRegime,
    pub volatility: VolRegime,
}

impl RegimeState {
    pub fn new(trend: TrendRegime, volatility: VolRegime) -> Self {
        Self { trend, volatility }
    }

    /// Human-readable label, e.g. `"Trending/High"`.
    pub fn label(&self) -> String {
        let t = match self.trend {
            TrendRegime::Trending => "Trending",
            TrendRegime::Ranging  => "Ranging",
            TrendRegime::Neutral  => "Neutral",
        };
        let v = match self.volatility {
            VolRegime::High   => "High",
            VolRegime::Low    => "Low",
            VolRegime::Normal => "Normal",
        };
        format!("{t}/{v}")
    }

    pub fn is_trending(&self) -> bool { self.trend == TrendRegime::Trending }
    pub fn is_ranging(&self)  -> bool { self.trend == TrendRegime::Ranging }
    pub fn is_high_vol(&self) -> bool { self.volatility == VolRegime::High }
    pub fn is_low_vol(&self)  -> bool { self.volatility == VolRegime::Low }
}

/// Aggregated regime statistics from a completed backtest.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct RegimeSummary {
    /// % of detected bars in Trending regime.
    pub trending_pct: f64,
    /// % of detected bars in Ranging regime.
    pub ranging_pct: f64,
    /// % of detected bars in Neutral regime.
    pub neutral_pct: f64,
    /// % of detected bars in High volatility regime.
    pub high_vol_pct: f64,
    /// % of detected bars in Low volatility regime.
    pub low_vol_pct: f64,
    /// Regime transitions: `(timestamp_ms, label)`, on-change only.
    pub changes: Vec<(i64, String)>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn label_format() {
        let r = RegimeState::new(TrendRegime::Trending, VolRegime::High);
        assert_eq!(r.label(), "Trending/High");
        let r2 = RegimeState::new(TrendRegime::Ranging, VolRegime::Low);
        assert_eq!(r2.label(), "Ranging/Low");
    }

    #[test]
    fn helpers() {
        let r = RegimeState::new(TrendRegime::Trending, VolRegime::High);
        assert!(r.is_trending());
        assert!(!r.is_ranging());
        assert!(r.is_high_vol());
        assert!(!r.is_low_vol());
    }
}
