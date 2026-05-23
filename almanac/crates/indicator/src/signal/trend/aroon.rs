use std::collections::VecDeque;

/// Aroon — đo **thời gian** kể từ lần đạt high/low cao nhất/thấp nhất gần đây.
///
/// Được Tushar Chande phát triển năm 1995. Khác với hầu hết oscillator đo giá,
/// Aroon đo *khoảng cách thời gian* từ lần cao/thấp nhất trong `period` bar.
/// Indicator này giỏi phát hiện khi nào trend đang bắt đầu hoặc kết thúc.
///
/// # Công thức
/// ```text
/// Aroon Up   = ((period - bars_since_high) / period) × 100
/// Aroon Down = ((period - bars_since_low)  / period) × 100
/// Oscillator = Aroon Up − Aroon Down   (range: −100 đến +100)
/// ```
///
/// - `bars_since_high` = số bar tính từ khi đạt highest high (0 = bar hiện tại)
/// - `bars_since_low`  = số bar tính từ khi đạt lowest low
///
/// # Cách đọc tín hiệu
/// - **Aroon Up = 100**: high mới vừa đạt trong bar hiện tại → bullish mạnh
/// - **Aroon Down = 100**: low mới vừa đạt → bearish mạnh
/// - **Aroon Up > 70, Aroon Down < 30**: uptrend mạnh đang hình thành
/// - **Oscillator cắt 0 từ dưới lên**: chuyển từ bearish → bullish
/// - **Cả hai ~50**: consolidation, không có xu hướng rõ ràng
///
/// # So sánh với ADX
/// - ADX đo *độ mạnh* của trend; Aroon đo *thời gian* từ last extreme
/// - Aroon tốt hơn để phát hiện breakout sớm; ADX tốt hơn để xác nhận trend mạnh
///
/// # Warmup
/// Cần `period + 1` bar (period + bar hiện tại).
#[derive(Debug, Clone)]
pub struct AroonValue {
    /// Aroon Up: 100 = high vừa đạt trong bar hiện tại, 0 = high cách đây period bar
    pub up: f64,
    /// Aroon Down: 100 = low vừa đạt trong bar hiện tại, 0 = low cách đây period bar
    pub down: f64,
}

/// Aroon indicator — đo thời gian kể từ highest high / lowest low trong N bar.
#[allow(missing_docs)]
#[derive(Debug, Clone)]
pub struct Aroon {
    period: usize,
    highs: VecDeque<f64>,
    lows: VecDeque<f64>,
}

impl Aroon {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "Aroon period must be > 0");
        Self {
            period,
            highs: VecDeque::with_capacity(period + 1),
            lows: VecDeque::with_capacity(period + 1),
        }
    }

    pub fn description() -> &'static str {
        "Aroon — measures how recently the highest high and lowest low occurred within the period. High Aroon Up + low Aroon Down = strong uptrend."
    }

    pub fn update(&mut self, high: f64, low: f64) -> Option<AroonValue> {
        self.highs.push_back(high);
        self.lows.push_back(low);
        if self.highs.len() > self.period + 1 {
            self.highs.pop_front();
            self.lows.pop_front();
        }
        if self.highs.len() < self.period + 1 {
            return None;
        }

        // bars_since_high = index of max from the end (0 = current bar)
        let bars_since_high = self.highs
            .iter()
            .rev()
            .enumerate()
            .max_by(|a, b| a.1.partial_cmp(b.1).unwrap())
            .map(|(i, _)| i)
            .unwrap_or(0);

        let bars_since_low = self.lows
            .iter()
            .rev()
            .enumerate()
            .min_by(|a, b| a.1.partial_cmp(b.1).unwrap())
            .map(|(i, _)| i)
            .unwrap_or(0);

        let up = ((self.period - bars_since_high) as f64 / self.period as f64) * 100.0;
        let down = ((self.period - bars_since_low) as f64 / self.period as f64) * 100.0;

        Some(AroonValue { up, down })
    }

    pub fn reset(&mut self) {
        self.highs.clear();
        self.lows.clear();
    }
}

// ── AroonOscillator ───────────────────────────────────────────────────────────

/// Aroon Oscillator — emits only `Aroon Up − Aroon Down` as a single scalar.
///
/// Range: −100 to +100. Positive = uptrend dominates; negative = downtrend.
/// Use this when you only need the oscillator value and not the Up/Down components.
#[derive(Debug, Clone)]
pub struct AroonOscillator {
    inner: Aroon,
}

impl AroonOscillator {
    pub fn new(period: usize) -> Self {
        Self { inner: Aroon::new(period) }
    }

    pub fn description() -> &'static str {
        "Aroon Oscillator — Aroon Up minus Aroon Down. Positive = uptrend, negative = downtrend. Range: −100 to +100."
    }

    pub fn update(&mut self, high: f64, low: f64) -> Option<f64> {
        self.inner.update(high, low).map(|v| v.up - v.down)
    }

    pub fn reset(&mut self) {
        self.inner.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_aroon_new_high_gives_up_100() {
        let mut a = Aroon::new(5);
        for i in 0..5 { a.update(i as f64 + 1.0, i as f64); }
        let v = a.update(10.0, 5.0).unwrap();
        assert_eq!(v.up, 100.0);
    }

    #[test]
    fn test_aroon_oscillator_via_separate_indicator() {
        let mut a = AroonOscillator::new(5);
        let mut last = None;
        for i in 0..10 { last = a.update(i as f64, 10.0 - i as f64); }
        let v = last.unwrap();
        assert!(v >= -100.0 && v <= 100.0);
    }
}
