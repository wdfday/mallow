use std::collections::VecDeque;

/// Relative Vigor Index (RVI) — đo "conviction" của chuyển động giá.
///
/// Được John Ehlers phát triển dựa trên quan sát: trong bull market, giá
/// thường đóng cửa cao hơn mở cửa; trong bear market, thường thấp hơn.
/// RVI chuẩn hoá sự chênh lệch close-open bằng high-low range để tạo
/// một oscillator đo "mức độ quyết tâm" (vigor) của phong trào giá.
///
/// # Công thức
/// Sử dụng 4-bar symmetrical weighted average (giảm noise):
/// ```text
/// weights: [1, 2, 2, 1] / 6
///
/// Numerator(t)   = (C-O) + 2*(C1-O1) + 2*(C2-O2) + (C3-O3)
/// Denominator(t) = (H-L) + 2*(H1-L1) + 2*(H2-L2) + (H3-L3)
///
/// Num_bar   = Numerator(t) / 6
/// Denom_bar = Denominator(t) / 6
///
/// RVI  = SMA(Num_bar, period) / SMA(Denom_bar, period)
/// Signal = (RVI + 2*RVI1 + 2*RVI2 + RVI3) / 6   (4-bar weighted avg)
/// ```
///
/// # Cách đọc
/// - RVI > 0 và tăng → bulls chiếm ưu thế rõ ràng
/// - RVI cắt lên trên Signal → buy signal
/// - RVI cắt xuống dưới Signal → sell signal
/// - Divergence vs giá → tín hiệu đảo chiều sớm
///
/// # Warmup
/// Cần `period + 3` bar (3 bar thêm cho weighted calculation).
#[derive(Debug, Clone)]
pub struct Rvi {
    period: usize,
    /// OHLC buffer cho 4-bar weighted calc
    ohlc_buf: VecDeque<(f64, f64, f64, f64)>,
    /// Rolling sums của Num_bar và Denom_bar
    num_buf: VecDeque<f64>,
    denom_buf: VecDeque<f64>,
    sum_num: f64,
    sum_denom: f64,
    /// Lưu 3 giá trị RVI trước để tính signal
    rvi_buf: VecDeque<f64>,
}

/// Kết quả RVI.
#[derive(Debug, Clone, Copy)]
pub struct RviValue {
    /// RVI Line
    pub rvi: f64,
    /// Signal Line (4-bar weighted average của RVI)
    pub signal: f64,
}

impl Rvi {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "RVI period must be > 0");
        Self {
            period,
            ohlc_buf: VecDeque::with_capacity(4),
            num_buf: VecDeque::with_capacity(period),
            denom_buf: VecDeque::with_capacity(period),
            sum_num: 0.0,
            sum_denom: 0.0,
            rvi_buf: VecDeque::with_capacity(4),
        }
    }

    /// RVI(10) — period chuẩn.
    pub fn standard() -> Self {
        Self::new(10)
    }

    pub fn update(&mut self, open: f64, high: f64, low: f64, close: f64) -> Option<RviValue> {
        // Bước 1: tích lũy 4 bar OHLC
        if self.ohlc_buf.len() == 4 {
            self.ohlc_buf.pop_front();
        }
        self.ohlc_buf.push_back((open, high, low, close));

        if self.ohlc_buf.len() < 4 {
            return None;
        }

        // Bước 2: tính 4-bar weighted numerator và denominator
        // Weights: [1,2,2,1]/6 (oldest → newest)
        let w = [1.0, 2.0, 2.0, 1.0];
        let mut num = 0.0;
        let mut denom = 0.0;
        for (i, &(o, h, l, c)) in self.ohlc_buf.iter().enumerate() {
            num += w[i] * (c - o);
            denom += w[i] * (h - l);
        }
        num /= 6.0;
        denom /= 6.0;

        // Bước 3: rolling SMA
        if self.num_buf.len() == self.period {
            self.sum_num -= self.num_buf.pop_front().unwrap();
            self.sum_denom -= self.denom_buf.pop_front().unwrap();
        }
        self.num_buf.push_back(num);
        self.denom_buf.push_back(denom);
        self.sum_num += num;
        self.sum_denom += denom;

        if self.num_buf.len() < self.period {
            return None;
        }

        if self.sum_denom.abs() < f64::EPSILON {
            return None;
        }

        let rvi = self.sum_num / self.sum_denom;

        // Bước 4: signal = 4-bar weighted average của RVI
        if self.rvi_buf.len() == 4 {
            self.rvi_buf.pop_front();
        }
        self.rvi_buf.push_back(rvi);

        if self.rvi_buf.len() < 4 {
            return None;
        }

        let signal = (self.rvi_buf[0] + 2.0 * self.rvi_buf[1]
            + 2.0 * self.rvi_buf[2] + self.rvi_buf[3]) / 6.0;

        Some(RviValue { rvi, signal })
    }

    pub fn is_ready(&self) -> bool {
        self.rvi_buf.len() >= 4 && self.num_buf.len() >= self.period
    }

    pub fn reset(&mut self) {
        self.ohlc_buf.clear();
        self.num_buf.clear();
        self.denom_buf.clear();
        self.sum_num = 0.0;
        self.sum_denom = 0.0;
        self.rvi_buf.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn feed(rvi: &mut Rvi, n: usize) -> Option<RviValue> {
        let mut r = None;
        for i in 0..n {
            let base = 100.0 + i as f64;
            r = rvi.update(base, base + 2.0, base - 1.0, base + 1.5);
        }
        r
    }

    #[test]
    fn test_rvi_uptrend_positive() {
        // Trong uptrend (close > open mỗi bar), RVI > 0
        let mut rvi = Rvi::new(5);
        let v = feed(&mut rvi, 30).unwrap();
        assert!(v.rvi > 0.0, "RVI={}", v.rvi);
    }

    #[test]
    fn test_rvi_warmup() {
        let mut rvi = Rvi::new(3);
        let mut count = 0;
        for i in 0..20 {
            let base = 100.0 + i as f64;
            if rvi.update(base, base + 1.0, base - 1.0, base + 0.5).is_some() {
                count += 1;
            }
        }
        assert!(count > 0, "Should produce values after warmup");
    }

    #[test]
    fn test_rvi_reset() {
        let mut rvi = Rvi::new(5);
        feed(&mut rvi, 30);
        assert!(rvi.is_ready());
        rvi.reset();
        assert!(!rvi.is_ready());
    }
}
