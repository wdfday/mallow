use std::collections::VecDeque;

/// Commodity Channel Index (CCI) — đo mức độ lệch của giá so với trung bình thống kê.
///
/// Được Donald Lambert phát triển năm 1980 cho commodity futures, nhưng ngày nay
/// được dùng rộng rãi cho cổ phiếu, forex, crypto. CCI đo khoảng cách giữa
/// Typical Price và SMA của nó, chuẩn hóa bằng mean deviation.
///
/// # Công thức
/// ```text
/// TP = (High + Low + Close) / 3          ← Typical Price
/// SMA = (1/n) × Σ TP_i                  ← trung bình TP trong n bar
/// MeanDev = (1/n) × Σ|TP_i − SMA|       ← mean absolute deviation
///
/// CCI = (TP − SMA) / (0.015 × MeanDev)
/// ```
///
/// Hằng số 0.015 được Donald Lambert chọn để khoảng 70–80% giá trị CCI
/// nằm trong dải −100 đến +100 với phân phối thông thường.
///
/// # Ngưỡng và cách đọc
/// - **CCI > +100**: overbought / strong uptrend — giá vượt xa trung bình thống kê
/// - **CCI < −100**: oversold / strong downtrend
/// - **CCI cắt +100 từ dưới lên**: buy signal (breakout upward)
/// - **CCI cắt −100 từ trên xuống**: sell signal
/// - **CCI cắt 0**: momentum đổi hướng
/// - **Divergence** CCI vs giá: tín hiệu đảo chiều
///
/// # Ứng dụng thực tế
/// - Kết hợp với ADX: chỉ trade CCI signal khi ADX > 25 (trend đủ mạnh)
/// - Dùng period ngắn (14–20) cho swing trade; period dài (40–50) cho position trade
///
/// # Warmup
/// Cần đúng `period` bar.
#[derive(Clone)]
pub struct Cci {
    period: usize,
    buffer: VecDeque<f64>,
    sum: f64,
}

impl Cci {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "CCI period must be > 0");
        Self {
            period,
            buffer: VecDeque::with_capacity(period),
            sum: 0.0,
        }
    }

    /// Feed a new bar and return CCI if enough data has been accumulated.
    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<f64> {
        let tp = (high + low + close) / 3.0;

        self.sum += tp;
        self.buffer.push_back(tp);

        if self.buffer.len() > self.period {
            let oldest = self.buffer.pop_front().unwrap();
            self.sum -= oldest;
        }

        if self.buffer.len() < self.period {
            return None;
        }

        let sma = self.sum / self.period as f64;

        let mean_dev: f64 = self.buffer.iter().map(|&x| (x - sma).abs()).sum::<f64>()
            / self.period as f64;

        if mean_dev == 0.0 {
            return Some(0.0);
        }

        Some((tp - sma) / (0.015 * mean_dev))
    }

    pub fn is_ready(&self) -> bool {
        self.buffer.len() >= self.period
    }

    pub fn reset(&mut self) {
        self.buffer.clear();
        self.sum = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn returns_none_until_warm() {
        let mut cci = Cci::new(5);
        for i in 0..4 {
            let v = i as f64 + 1.0;
            assert!(cci.update(v + 0.5, v - 0.5, v).is_none());
        }
        assert!(cci.update(5.5, 4.5, 5.0).is_some());
    }

    #[test]
    fn reset_clears_state() {
        let mut cci = Cci::new(3);
        for i in 1..=3 {
            let v = i as f64;
            cci.update(v, v - 0.5, v - 0.25);
        }
        cci.reset();
        assert!(!cci.is_ready());
    }
}
