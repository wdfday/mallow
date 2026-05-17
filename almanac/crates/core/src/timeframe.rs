use std::fmt;

use serde::{Deserialize, Serialize};

/// Standard trading timeframes. Used for bar aggregation, annualization, and display.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize, Default)]
pub enum Timeframe {
    #[default]
    M1,
    M3,
    M5,
    M10,
    M15,
    M30,
    H1,
    H2,
    H4,
    H6,
    H12,
    D1,
    W1,
    MN,
}

impl Timeframe {
    /// Duration in milliseconds.
    pub const fn duration_ms(self) -> i64 {
        match self {
            Timeframe::M1  =>          60_000,
            Timeframe::M3  =>         180_000,
            Timeframe::M5  =>         300_000,
            Timeframe::M10 =>         600_000,
            Timeframe::M15 =>         900_000,
            Timeframe::M30 =>       1_800_000,
            Timeframe::H1  =>       3_600_000,
            Timeframe::H2  =>       7_200_000,
            Timeframe::H4  =>      14_400_000,
            Timeframe::H6  =>      21_600_000,
            Timeframe::H12 =>      43_200_000,
            Timeframe::D1  =>      86_400_000,
            Timeframe::W1  =>     604_800_000,
            Timeframe::MN  =>   2_592_000_000, // 30-day month
        }
    }

    /// Fallback bars-per-year assuming 24/7 market (crypto default).
    /// Prefer computing from actual equity curve timestamps when available.
    pub fn bars_per_year(self) -> f64 {
        // 365.25 days × 24 h × 3600 s × 1000 ms
        365.25 * 24.0 * 3_600_000.0 / self.duration_ms() as f64
    }

    /// Detect the most likely timeframe from a series of bar timestamps.
    ///
    /// Uses the 10th-percentile consecutive interval to skip over gaps
    /// (weekend/holiday sessions, data holes) that would skew the estimate upward.
    pub fn detect(timestamps: &[i64]) -> Self {
        if timestamps.len() < 2 {
            return Timeframe::M1;
        }
        let mut intervals: Vec<i64> = timestamps
            .windows(2)
            .map(|w| w[1] - w[0])
            .filter(|&d| d > 0)
            .collect();

        if intervals.is_empty() {
            return Timeframe::M1;
        }
        intervals.sort_unstable();

        // 10th-percentile avoids holiday/weekend gaps
        let idx = (intervals.len() as f64 * 0.10) as usize;
        let typical_ms = intervals[idx].max(1);
        Self::snap(typical_ms)
    }

    /// Snap a raw interval (ms) to the nearest standard timeframe.
    fn snap(ms: i64) -> Self {
        const ALL: [Timeframe; 14] = [
            Timeframe::M1, Timeframe::M3, Timeframe::M5, Timeframe::M10,
            Timeframe::M15, Timeframe::M30,
            Timeframe::H1, Timeframe::H2, Timeframe::H4, Timeframe::H6,
            Timeframe::H12,
            Timeframe::D1, Timeframe::W1, Timeframe::MN,
        ];
        ALL.iter()
            .copied()
            .min_by_key(|tf| (tf.duration_ms() - ms).abs())
            .unwrap_or(Timeframe::M1)
    }
}

impl fmt::Display for Timeframe {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let s = match self {
            Timeframe::M1  => "M1",
            Timeframe::M3  => "M3",
            Timeframe::M5  => "M5",
            Timeframe::M10 => "M10",
            Timeframe::M15 => "M15",
            Timeframe::M30 => "M30",
            Timeframe::H1  => "H1",
            Timeframe::H2  => "H2",
            Timeframe::H4  => "H4",
            Timeframe::H6  => "H6",
            Timeframe::H12 => "H12",
            Timeframe::D1  => "D1",
            Timeframe::W1  => "W1",
            Timeframe::MN  => "MN",
        };
        write!(f, "{s}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detect_m1() {
        let ts: Vec<i64> = (0..100).map(|i| i * 60_000).collect();
        assert_eq!(Timeframe::detect(&ts), Timeframe::M1);
    }

    #[test]
    fn detect_h1() {
        let ts: Vec<i64> = (0..100).map(|i| i * 3_600_000).collect();
        assert_eq!(Timeframe::detect(&ts), Timeframe::H1);
    }

    #[test]
    fn detect_d1() {
        let ts: Vec<i64> = (0..100).map(|i| i * 86_400_000).collect();
        assert_eq!(Timeframe::detect(&ts), Timeframe::D1);
    }

    #[test]
    fn detect_d1_with_weekend_gaps() {
        // Mon-Fri pattern: 5 daily bars then 3-day gap
        let mut ts = Vec::new();
        for week in 0..20i64 {
            for day in 0..5i64 {
                ts.push(week * 7 * 86_400_000 + day * 86_400_000);
            }
        }
        assert_eq!(Timeframe::detect(&ts), Timeframe::D1);
    }

    #[test]
    fn bars_per_year_m1_crypto() {
        // 365.25 * 24 * 60 ≈ 525_960
        let bpy = Timeframe::M1.bars_per_year();
        assert!((bpy - 525_960.0).abs() < 10.0);
    }

    #[test]
    fn bars_per_year_d1_crypto() {
        let bpy = Timeframe::D1.bars_per_year();
        assert!((bpy - 365.25).abs() < 0.1);
    }
}
