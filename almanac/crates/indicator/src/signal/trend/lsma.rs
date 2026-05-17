use std::collections::VecDeque;

/// Least Squares Moving Average (LSMA) — đường hồi quy tuyến tính qua N bar.
///
/// Còn được gọi là **Linear Regression Value** hoặc **End Point Moving Average**.
/// Thay vì lấy trung bình đơn giản, LSMA fit một đường thẳng qua N bar gần nhất
/// bằng phương pháp bình phương nhỏ nhất, rồi trả về **điểm cuối** (bar mới nhất)
/// của đường đó.
///
/// # Tại sao dùng LSMA thay SMA?
/// - SMA lag N/2 bar (tâm của window)
/// - LSMA lag gần như 0 vì đường hồi quy được "kéo" về phía cuối dữ liệu
/// - LSMA smooth hơn giá thực nhưng vẫn phản ứng nhanh với trend
///
/// # Công thức
/// Đặt các bar trong window là `(x₀, p₀), (x₁, p₁), ..., (xₙ₋₁, pₙ₋₁)` với
/// `x = 0` (bar cũ nhất) đến `x = n-1` (bar mới nhất).
///
/// ```text
/// Σx   = n(n-1)/2
/// Σx²  = n(n-1)(2n-1)/6
/// Σp   = tổng giá
/// Σxp  = Σ(x × price[x])
///
/// slope     = (n·Σxp − Σx·Σp) / (n·Σx² − (Σx)²)
/// intercept = (Σp − slope·Σx) / n
///
/// LSMA = intercept + slope × (n − 1)   ← giá trị tại điểm cuối window
/// ```
///
/// # So sánh với ALMA
/// ALMA cũng giảm lag nhưng dùng weighted average; LSMA dùng hồi quy nên
/// còn trả thêm thông tin về **slope** (xu hướng hiện tại).
#[derive(Debug, Clone)]
pub struct Lsma {
    period: usize,
    /// Rolling buffer giá close, index 0 = bar cũ nhất
    buf: VecDeque<f64>,
    /// Hằng số tính trước: n(n-1)/2
    sum_x: f64,
    /// Hằng số tính trước: n(n-1)(2n-1)/6 — dùng để tính denom
    #[allow(dead_code)]
    sum_x2: f64,
    /// Mẫu số của slope: n·Σx² − (Σx)²
    denom: f64,
}

impl Lsma {
    pub fn new(period: usize) -> Self {
        assert!(period >= 2, "LSMA period must be >= 2 for regression");
        let n = period as f64;
        let sum_x = n * (n - 1.0) / 2.0;
        let sum_x2 = n * (n - 1.0) * (2.0 * n - 1.0) / 6.0;
        let denom = n * sum_x2 - sum_x * sum_x;
        Self {
            period,
            buf: VecDeque::with_capacity(period),
            sum_x,
            sum_x2,
            denom,
        }
    }

    pub fn description() -> &'static str {
        "Least Squares MA (Linear Regression MA) — fits a linear regression line to the last N closes and returns the endpoint. Also exposes slope for trend-strength measurement."
    }

    /// Feed một bar mới. Trả về `(lsma, slope)` sau khi đủ `period` bar.
    ///
    /// `slope` là độ dốc của đường hồi quy — dương khi uptrend, âm khi downtrend.
    pub fn update(&mut self, price: f64) -> Option<LsmaValue> {
        if self.buf.len() == self.period {
            self.buf.pop_front();
        }
        self.buf.push_back(price);

        if self.buf.len() < self.period {
            return None;
        }

        let n = self.period as f64;

        // Tính Σp và Σxp trong một lần duyệt
        let mut sum_p = 0.0;
        let mut sum_xp = 0.0;
        for (i, &p) in self.buf.iter().enumerate() {
            sum_p += p;
            sum_xp += i as f64 * p;
        }

        let slope = (n * sum_xp - self.sum_x * sum_p) / self.denom;
        let intercept = (sum_p - slope * self.sum_x) / n;

        // Giá trị LSMA = điểm cuối đường hồi quy
        let value = intercept + slope * (n - 1.0);

        Some(LsmaValue { value, slope })
    }

    pub fn is_ready(&self) -> bool {
        self.buf.len() >= self.period
    }

    pub fn reset(&mut self) {
        self.buf.clear();
    }
}

/// Kết quả LSMA gồm giá trị MA và slope của đường hồi quy.
#[derive(Debug, Clone, Copy)]
pub struct LsmaValue {
    /// Giá trị LSMA (điểm cuối đường hồi quy)
    pub value: f64,
    /// Độ dốc đường hồi quy (đơn vị: price/bar)
    /// - Dương → uptrend
    /// - Âm   → downtrend
    /// - ≈ 0  → sideways
    pub slope: f64,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_lsma_perfect_trend() {
        // Với chuỗi tăng đều [1,2,3,4,5], hồi quy hoàn hảo → LSMA = 5.0, slope = 1.0
        let mut lsma = Lsma::new(5);
        let mut r = None;
        for p in [1.0, 2.0, 3.0, 4.0, 5.0] {
            r = lsma.update(p);
        }
        let v = r.unwrap();
        assert!((v.value - 5.0).abs() < 1e-10, "LSMA={}", v.value);
        assert!((v.slope - 1.0).abs() < 1e-10, "slope={}", v.slope);
    }

    #[test]
    fn test_lsma_flat_slope_zero() {
        // Giá không đổi → slope = 0, LSMA = giá đó
        let mut lsma = Lsma::new(5);
        let mut r = None;
        for _ in 0..5 { r = lsma.update(42.0); }
        let v = r.unwrap();
        assert!((v.slope).abs() < 1e-10, "slope should be 0, got {}", v.slope);
        assert!((v.value - 42.0).abs() < 1e-10);
    }

    #[test]
    fn test_lsma_downtrend_negative_slope() {
        let mut lsma = Lsma::new(5);
        let mut r = None;
        for p in [10.0, 8.0, 6.0, 4.0, 2.0] { r = lsma.update(p); }
        let v = r.unwrap();
        assert!(v.slope < 0.0, "slope should be negative in downtrend");
    }

    #[test]
    fn test_lsma_warmup() {
        let mut lsma = Lsma::new(3);
        assert!(lsma.update(1.0).is_none());
        assert!(lsma.update(2.0).is_none());
        assert!(lsma.update(3.0).is_some());
    }

    #[test]
    fn test_lsma_reset() {
        let mut lsma = Lsma::new(3);
        for p in [1.0, 2.0, 3.0] { lsma.update(p); }
        assert!(lsma.is_ready());
        lsma.reset();
        assert!(!lsma.is_ready());
    }
}
