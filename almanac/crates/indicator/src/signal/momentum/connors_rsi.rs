use std::collections::VecDeque;

use crate::Rsi;

/// ConnorsRSI (CRSI) — composite momentum indicator ba thành phần.
///
/// Được Larry Connors và Cesar Alvarez phát triển. ConnorsRSI kết hợp ba nguồn
/// thông tin khác nhau về momentum để tạo ra tín hiệu oversold/overbought tin cậy
/// hơn RSI đơn thuần, đặc biệt cho short-term mean-reversion trading.
///
/// # Công thức
/// ```text
/// Component 1 — RSI của giá đóng cửa:
///   rsi1 = RSI(close, rsi_period)       [thường dùng period=3]
///
/// Component 2 — RSI của streak:
///   streak: số bar liên tiếp tăng (+n) hoặc giảm (−n)
///   rsi2 = RSI(streak, streak_rsi_period)  [thường dùng period=2]
///
/// Component 3 — Percent Rank của 1-bar ROC%:
///   roc = (close − prev_close) / prev_close × 100
///   pct_rank = count(rank_period prior roc < roc_today) / rank_period × 100
///
/// CRSI = (rsi1 + rsi2 + pct_rank) / 3    (range: 0..100)
/// ```
///
/// # Tham số mặc định
/// - `rsi_period = 3`: RSI nhanh → nhạy với momentum ngắn hạn
/// - `streak_rsi_period = 2`: RSI của streak → đo exhaustion của move liên tục
/// - `rank_period = 100`: lookback dài → so sánh với lịch sử dài hạn
///
/// # Cách đọc tín hiệu
/// - **CRSI < 10**: extremely oversold → mean-reversion buy opportunity
/// - **CRSI > 90**: extremely overbought → mean-reversion sell opportunity
/// - **Tín hiệu tốt nhất**: khi cả ba components cùng extreme (oversold/overbought)
///
/// # Ứng dụng
/// CRSI thiết kế cho **mean-reversion trading** trong thị trường có xu hướng chính
/// (uptrend dài hạn). Entry khi pullback → CRSI < 10. Exit nhanh (1−3 ngày).
///
/// # Warmup
/// Cần `max(rsi_period, streak_rsi_period) + rank_period + 1` bar (ROC component
/// cần bar hiện tại + `rank_period` ROC lịch sử, mỗi ROC cần 1 prev_close).
/// Default CRSI(3,2,100): cần ~104 bar.
#[derive(Debug, Clone)]
pub struct ConnorsRsi {
    rsi_period: usize,
    streak_rsi_period: usize,
    rank_period: usize,
    /// RSI of close
    rsi_close: Rsi,
    /// RSI of streak
    rsi_streak: Rsi,
    prev_close: Option<f64>,
    /// Current consecutive streak value (+n up, -n down)
    streak: f64,
    /// Rolling window of 1-bar ROC% values for percent rank
    roc_window: VecDeque<f64>,
    value: Option<f64>,
}

impl ConnorsRsi {
    pub fn new(rsi_period: usize, streak_rsi_period: usize, rank_period: usize) -> Self {
        assert!(rsi_period > 0, "ConnorsRsi: rsi_period must be > 0");
        assert!(streak_rsi_period > 0, "ConnorsRsi: streak_rsi_period must be > 0");
        assert!(rank_period > 0, "ConnorsRsi: rank_period must be > 0");
        Self {
            rsi_period,
            streak_rsi_period,
            rank_period,
            rsi_close: Rsi::new(rsi_period),
            rsi_streak: Rsi::new(streak_rsi_period),
            prev_close: None,
            streak: 0.0,
            roc_window: VecDeque::with_capacity(rank_period + 1),
            value: None,
        }
    }

    pub fn description() -> &'static str {
        "ConnorsRSI — composite of short-term RSI(3), streak RSI, and percentile rank of daily returns. Optimised for short-term mean-reversion entries. Outputs a single 0–100 value."
    }

    /// Default parameters: rsi_period=3, streak_rsi_period=2, percent_rank_period=100
    pub fn default() -> Self {
        Self::new(3, 2, 100)
    }

    /// Feed a new closing price. Returns `Some(crsi)` once all components are ready.
    pub fn update(&mut self, close: f64) -> Option<f64> {
        // Component 1: RSI of close
        let rsi1 = self.rsi_close.update(close);

        let Some(prev) = self.prev_close else {
            self.prev_close = Some(close);
            return None;
        };

        // Update streak
        if close > prev {
            if self.streak < 0.0 {
                self.streak = 1.0;
            } else {
                self.streak += 1.0;
            }
        } else if close < prev {
            if self.streak > 0.0 {
                self.streak = -1.0;
            } else {
                self.streak -= 1.0;
            }
        } else {
            self.streak = 0.0;
        }
        self.prev_close = Some(close);

        // Component 2: RSI of streak
        let rsi2 = self.rsi_streak.update(self.streak);

        // Component 3: 1-bar ROC% and percent rank
        let roc = if prev.abs() > f64::EPSILON {
            (close - prev) / prev * 100.0
        } else {
            0.0
        };
        self.roc_window.push_back(roc);
        // Keep the current bar (at the back) plus `rank_period` historical ROC
        // values in front of it → window holds `rank_period + 1` elements.
        if self.roc_window.len() > self.rank_period + 1 {
            self.roc_window.pop_front();
        }

        // Need the current bar + a full `rank_period` of history behind it.
        if self.roc_window.len() < self.rank_period + 1 {
            return None;
        }

        // Percent rank (Connors / TradingView `percentrank`): of the
        // `rank_period` historical ROC values (everything except the current
        // bar at the back), how many are strictly below today's ROC.
        let count_below = self.roc_window.iter().take(self.rank_period).filter(|&&r| r < roc).count();
        let pct_rank = count_below as f64 / self.rank_period as f64 * 100.0;

        match (rsi1, rsi2) {
            (Some(r1), Some(r2)) => {
                self.value = Some((r1 + r2 + pct_rank) / 3.0);
                self.value
            }
            _ => None,
        }
    }

    pub fn value(&self) -> Option<f64> {
        self.value
    }

    pub fn is_ready(&self) -> bool {
        self.value.is_some()
    }

    pub fn reset(&mut self) {
        self.rsi_close = Rsi::new(self.rsi_period);
        self.rsi_streak = Rsi::new(self.streak_rsi_period);
        self.prev_close = None;
        self.streak = 0.0;
        self.roc_window.clear();
        self.value = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_connors_rsi_warmup() {
        let mut crsi = ConnorsRsi::new(3, 2, 10);
        // Not ready during warmup
        for i in 0..9 {
            let v = crsi.update(100.0 + i as f64);
            assert!(v.is_none(), "should not be ready before warmup: bar {i}");
        }
    }

    #[test]
    fn test_connors_rsi_ready_and_range() {
        let mut crsi = ConnorsRsi::new(3, 2, 10);
        for i in 0..30 {
            if let Some(v) = crsi.update(100.0 + (i % 5) as f64 * 2.0) {
                assert!(v >= 0.0 && v <= 100.0, "CRSI out of range: {v}");
            }
        }
        assert!(crsi.is_ready(), "CRSI should be ready");
    }

    #[test]
    fn test_connors_rsi_uptrend() {
        let mut crsi = ConnorsRsi::new(3, 2, 10);
        for i in 0..30 {
            crsi.update(100.0 + i as f64 * 0.5);
        }
        if let Some(v) = crsi.value() {
            assert!(v > 50.0, "CRSI should be high in uptrend: {v}");
        }
    }

    #[test]
    fn test_connors_rsi_downtrend() {
        let mut crsi = ConnorsRsi::new(3, 2, 10);
        for i in 0..30 {
            crsi.update(200.0 - i as f64 * 0.5);
        }
        if let Some(v) = crsi.value() {
            assert!(v < 50.0, "CRSI should be low in downtrend: {v}");
        }
    }

    #[test]
    fn test_connors_rsi_reset() {
        let mut crsi = ConnorsRsi::new(3, 2, 10);
        for i in 0..30 {
            crsi.update(100.0 + i as f64);
        }
        assert!(crsi.is_ready());
        crsi.reset();
        assert!(!crsi.is_ready());
        assert!(crsi.value().is_none());
    }
}
