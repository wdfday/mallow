use std::collections::VecDeque;

/// Arnaud Legoux Moving Average (ALMA) — MA dùng phân phối Gaussian làm trọng số.
///
/// Được giới thiệu năm 2009. ALMA giảm lag đáng kể so với SMA/EMA trong khi
/// vẫn lọc nhiễu tốt nhờ hình chuông Gaussian — các bar gần cuối window được
/// nhấn mạnh hơn, còn bars ở xa được giảm trọng số mượt mà thay vì cắt đứt
/// như SMA.
///
/// # Tham số
/// - `period` : độ dài cửa sổ (thường 9–21)
/// - `offset` : dịch chỉnh tâm Gaussian về phía cuối (0.0–1.0, mặc định **0.85**)
///              → 1.0 = tâm ở bar mới nhất (ít smooth), 0.0 = tâm ở bar cũ nhất
/// - `sigma`  : độ rộng chuông Gaussian (mặc định **6.0**)
///              → nhỏ hơn = chuông hẹp hơn = nhạy hơn với giá gần cuối
///
/// # Công thức
/// ```text
/// m    = floor(offset × (period − 1))   ← vị trí tâm chuông (index)
/// s    = period / sigma                  ← độ lệch chuẩn
/// w[i] = exp(−(i − m)² / (2 × s²))     ← trọng số Gaussian, i ∈ [0, period)
/// norm = Σ w[i]
/// ALMA = Σ(price[i] × w[i]) / norm
/// ```
/// Trong đó `i = 0` là bar cũ nhất trong window, `i = period-1` là mới nhất.
///
/// # Độ phức tạp
/// - Khởi tạo: O(period) để tính weights
/// - Update: O(period) vì phải tổng toàn window — đây là điểm yếu so với EMA
#[derive(Debug, Clone)]
pub struct Alma {
    period: usize,
    /// Trọng số Gaussian đã chuẩn hoá (đã chia cho norm), index 0 = bar cũ nhất
    weights: Vec<f64>,
    /// Rolling buffer các giá close gần nhất
    buf: VecDeque<f64>,
}

impl Alma {
    /// Tạo ALMA với tham số tùy chỉnh.
    pub fn new(period: usize, offset: f64, sigma: f64) -> Self {
        assert!(period > 0, "ALMA period must be > 0");
        assert!(sigma > 0.0, "ALMA sigma must be > 0");

        // Tính trọng số Gaussian
        let m = (offset * (period as f64 - 1.0)).floor();
        let s = period as f64 / sigma;
        let denom = 2.0 * s * s;

        let raw: Vec<f64> = (0..period)
            .map(|i| {
                let diff = i as f64 - m;
                (-diff * diff / denom).exp()
            })
            .collect();

        let norm: f64 = raw.iter().sum();
        let weights = raw.iter().map(|w| w / norm).collect();

        Self {
            period,
            weights,
            buf: VecDeque::with_capacity(period),
        }
    }

    pub fn description() -> &'static str {
        "Arnaud Legoux MA — Gaussian-weighted MA centred near the most recent bar. Combines low lag with minimal ripple; configurable via offset and sigma."
    }

    /// ALMA(9) với tham số mặc định được Legoux khuyến nghị.
    pub fn default_fast() -> Self {
        Self::new(9, 0.85, 6.0)
    }

    /// ALMA(21) cho swing trading — cân bằng lag / noise.
    pub fn default_swing() -> Self {
        Self::new(21, 0.85, 6.0)
    }

    /// Feed một bar mới. Trả về `Some(alma)` sau khi đủ `period` bar.
    pub fn update(&mut self, price: f64) -> Option<f64> {
        if self.buf.len() == self.period {
            self.buf.pop_front();
        }
        self.buf.push_back(price);

        if self.buf.len() < self.period {
            return None;
        }

        // Tổng có trọng số: trọng số index 0 ứng với bar cũ nhất trong buf
        let value = self
            .buf
            .iter()
            .zip(self.weights.iter())
            .map(|(p, w)| p * w)
            .sum();

        Some(value)
    }

    pub fn is_ready(&self) -> bool {
        self.buf.len() >= self.period
    }

    pub fn reset(&mut self) {
        self.buf.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_alma_warmup() {
        let mut alma = Alma::new(5, 0.85, 6.0);
        for i in 0..4 {
            assert_eq!(alma.update(i as f64 + 1.0), None);
        }
        assert!(alma.update(5.0).is_some());
    }

    #[test]
    fn test_alma_constant_price() {
        // Khi giá không đổi, ALMA = giá đó (vì tổng trọng số = 1)
        let mut alma = Alma::new(5, 0.85, 6.0);
        let mut last = None;
        for _ in 0..10 {
            last = alma.update(42.0);
        }
        let v = last.unwrap();
        assert!((v - 42.0).abs() < 1e-10, "constant price ALMA={v}");
    }

    #[test]
    fn test_alma_uptrend_above_sma() {
        // Với offset cao (0.85), ALMA tập trung vào bars gần → lag thấp hơn SMA,
        // nghĩa là trong uptrend ALMA nằm cao hơn SMA cùng period
        let mut alma = Alma::new(5, 0.85, 6.0);
        let prices = [1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0];
        let mut av = 0.0;
        for &p in &prices {
            if let Some(v) = alma.update(p) { av = v; }
        }
        // SMA(5) of [3,4,5,6,7] = 5.0; ALMA với offset cao sẽ > 5.0
        assert!(av > 5.0, "ALMA={av} should be above SMA=5.0 in uptrend with high offset");
    }

    #[test]
    fn test_alma_reset() {
        let mut alma = Alma::new(3, 0.85, 6.0);
        for p in [1.0, 2.0, 3.0] { alma.update(p); }
        assert!(alma.is_ready());
        alma.reset();
        assert!(!alma.is_ready());
    }
}
