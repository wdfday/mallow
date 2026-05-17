/// Relative Strength Index (RSI) — chỉ báo momentum đo tốc độ và mức độ thay đổi giá.
///
/// Được J. Welles Wilder Jr. phát triển năm 1978 trong cuốn *New Concepts in
/// Technical Trading Systems*. RSI là một trong những oscillator được dùng nhiều
/// nhất trong giao dịch.
///
/// # Công thức (Wilder's smoothing)
/// ```text
/// gain = max(close - prev_close, 0)
/// loss = max(prev_close - close, 0)
///
/// Seed (n bar đầu):
///   avg_gain = SMA(gain, n)
///   avg_loss = SMA(loss, n)
///
/// Các bar sau (Wilder's exponential):
///   avg_gain = avg_gain_prev × (n-1)/n + gain / n
///   avg_loss = avg_loss_prev × (n-1)/n + loss / n
///
/// RS  = avg_gain / avg_loss
/// RSI = 100 − 100 / (1 + RS)
/// ```
/// Note: Wilder dùng alpha = 1/n (SMMA), không phải 2/(n+1) (EMA).
///
/// # Range và ngưỡng thông dụng
/// - RSI ∈ [0, 100]
/// - RSI > 70 → overbought (xem xét sell/short)
/// - RSI < 30 → oversold (xem xét buy/long)
/// - RSI = 50 → midpoint (không momentum rõ ràng)
///
/// # Divergence
/// Kỹ thuật quan trọng hơn ngưỡng overbought/oversold:
/// - Bullish divergence: giá tạo đáy mới nhưng RSI tạo đáy cao hơn → sắp đảo chiều lên
/// - Bearish divergence: giá tạo đỉnh mới nhưng RSI tạo đỉnh thấp hơn → sắp đảo chiều xuống
///
/// # Warmup
/// Cần `period + 1` bar: `period` bar để seed SMA, thêm 1 bar để tính delta đầu tiên.
#[derive(Debug, Clone)]
pub struct Rsi {
    period: usize,
    avg_gain: Option<f64>,
    avg_loss: Option<f64>,
    prev_close: Option<f64>,
    count: usize,
    seed_gains: Vec<f64>,
    seed_losses: Vec<f64>,
}

impl Rsi {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "RSI period must be > 0");
        Self {
            period,
            avg_gain: None,
            avg_loss: None,
            prev_close: None,
            count: 0,
            seed_gains: Vec::with_capacity(period),
            seed_losses: Vec::with_capacity(period),
        }
    }

    pub fn description() -> &'static str {
        "Relative Strength Index — oscillator (0–100) comparing average gains to average losses over N bars. Above 70 = overbought; below 30 = oversold."
    }

    /// Feed một giá đóng cửa. Trả về `Some(rsi)` sau `period + 1` bar.
    pub fn update(&mut self, close: f64) -> Option<f64> {
        let Some(prev) = self.prev_close else {
            self.prev_close = Some(close);
            return None;
        };

        let change = close - prev;
        self.prev_close = Some(close);

        let gain = change.max(0.0);
        let loss = (-change).max(0.0);

        self.count += 1;

        if self.count < self.period {
            self.seed_gains.push(gain);
            self.seed_losses.push(loss);
            return None;
        }

        if self.count == self.period {
            self.seed_gains.push(gain);
            self.seed_losses.push(loss);
            let avg_g = self.seed_gains.iter().sum::<f64>() / self.period as f64;
            let avg_l = self.seed_losses.iter().sum::<f64>() / self.period as f64;
            self.avg_gain = Some(avg_g);
            self.avg_loss = Some(avg_l);
            return Some(Self::rsi_from(avg_g, avg_l));
        }

        // Wilder smoothing
        let alpha = 1.0 / self.period as f64;
        let ag = gain * alpha + self.avg_gain.unwrap() * (1.0 - alpha);
        let al = loss * alpha + self.avg_loss.unwrap() * (1.0 - alpha);
        self.avg_gain = Some(ag);
        self.avg_loss = Some(al);
        Some(Self::rsi_from(ag, al))
    }

    fn rsi_from(avg_gain: f64, avg_loss: f64) -> f64 {
        if avg_loss < f64::EPSILON {
            return 100.0;
        }
        let rs = avg_gain / avg_loss;
        100.0 - (100.0 / (1.0 + rs))
    }

    pub fn value(&self) -> Option<f64> {
        match (self.avg_gain, self.avg_loss) {
            (Some(g), Some(l)) => Some(Self::rsi_from(g, l)),
            _ => None,
        }
    }

    pub fn is_ready(&self) -> bool {
        self.avg_gain.is_some()
    }

    pub fn reset(&mut self) {
        self.avg_gain = None;
        self.avg_loss = None;
        self.prev_close = None;
        self.count = 0;
        self.seed_gains.clear();
        self.seed_losses.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_rsi_bounds() {
        let mut rsi = Rsi::new(14);
        // Feed 16 ascending prices → RSI should be high
        for i in 0..16 {
            rsi.update(100.0 + i as f64);
        }
        let val = rsi.value().unwrap();
        assert!(val > 70.0, "RSI should be overbought: {val}");
    }

    #[test]
    fn test_rsi_oversold() {
        let mut rsi = Rsi::new(14);
        for i in 0..16 {
            rsi.update(100.0 - i as f64);
        }
        let val = rsi.value().unwrap();
        assert!(val < 30.0, "RSI should be oversold: {val}");
    }
}
