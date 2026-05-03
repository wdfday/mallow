use std::collections::VecDeque;

/// Rank Correlation Index (RCI) — đo mức độ tương quan giữa thứ tự thời gian và thứ tự giá.
///
/// Phát triển bởi Welles Wilder và phổ biến tại Nhật Bản. RCI dựa trên
/// hệ số tương quan thứ hạng Spearman (Spearman's ρ) — đo xem giá có
/// thực sự tăng **theo đúng thứ tự thời gian** không.
///
/// # Khác biệt với RSI/CMO
/// RSI/CMO đo tốc độ thay đổi; RCI đo **tính nhất quán của xu hướng**.
/// Nếu giá tăng rải rác (không đơn điệu), RSI cao nhưng RCI thấp.
/// RCI cao = giá tăng **đều đặn, nhất quán** qua từng bar.
///
/// # Công thức — Spearman Rank Correlation
/// ```text
/// time_rank[i] = i + 1  (1 = cũ nhất, n = mới nhất)
/// price_rank[i] = rank của close[i] từ cao (1) đến thấp (n)
///
/// d[i] = time_rank[i] - price_rank[i]
///
/// RCI = (1 - 6 × Σd²ᵢ / (n³ - n)) × 100
/// ```
///
/// # Range và ý nghĩa
/// - RCI ∈ [−100, +100]
/// - RCI = +100: giá tăng hoàn toàn đơn điệu (mỗi bar cao hơn bar trước)
/// - RCI = −100: giá giảm hoàn toàn đơn điệu
/// - RCI ≈ 0: không có tương quan giữa thời gian và giá (random walk)
/// - |RCI| > 80: xu hướng mạnh; < 50: không đủ xu hướng
///
/// # RCI Ribbon
/// Dùng nhiều RCI với period khác nhau (e.g., 9, 26, 52) để nhìn xu hướng
/// ở nhiều timeframe. `RciRibbon` trong crate này cung cấp 3 giá trị cùng lúc.
#[derive(Debug, Clone)]
pub struct Rci {
    period: usize,
    buf: VecDeque<f64>,
}

impl Rci {
    pub fn new(period: usize) -> Self {
        assert!(period >= 2, "RCI period must be >= 2");
        Self {
            period,
            buf: VecDeque::with_capacity(period),
        }
    }

    pub fn update(&mut self, price: f64) -> Option<f64> {
        if self.buf.len() == self.period {
            self.buf.pop_front();
        }
        self.buf.push_back(price);

        if self.buf.len() < self.period {
            return None;
        }

        Some(Self::compute(&self.buf))
    }

    fn compute(buf: &VecDeque<f64>) -> f64 {
        let n = buf.len();

        // Tính price rank: sắp xếp giảm dần, rank 1 = giá cao nhất
        // time_rank: index 0 (cũ nhất) = rank 1, index n-1 (mới nhất) = rank n
        let mut indexed: Vec<(usize, f64)> = buf.iter().cloned().enumerate().collect();
        // Sắp xếp theo giá giảm dần để gán rank
        indexed.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));

        let mut price_ranks = vec![0usize; n];
        for (rank_0, &(orig_idx, _)) in indexed.iter().enumerate() {
            price_ranks[orig_idx] = rank_0 + 1; // rank bắt đầu từ 1
        }

        // d² = (time_rank - price_rank)²
        // time_rank của buf[i]: i=0 (cũ nhất) → rank n, i=n-1 (mới nhất) → rank 1
        let sum_d_sq: f64 = (0..n)
            .map(|i| {
                let time_rank = n - i; // newest = 1, oldest = n
                let price_rank = price_ranks[i];
                let d = time_rank as f64 - price_rank as f64;
                d * d
            })
            .sum();

        let n = n as f64;
        // Spearman: ρ = 1 - 6Σd² / (n³-n)
        let rci = (1.0 - 6.0 * sum_d_sq / (n * n * n - n)) * 100.0;
        rci.clamp(-100.0, 100.0)
    }

    pub fn is_ready(&self) -> bool {
        self.buf.len() >= self.period
    }

    pub fn reset(&mut self) {
        self.buf.clear();
    }
}

/// RCI Ribbon — ba RCI cùng lúc ở 3 timeframe khác nhau.
///
/// Pattern phổ biến ở Nhật: short(9), medium(26), long(52).
/// - Cả 3 > 80: xu hướng tăng rất mạnh
/// - Short cắt xuống Medium: warning signal sớm
/// - Cả 3 hội tụ ở 0: không xu hướng
#[derive(Debug, Clone)]
pub struct RciRibbon {
    short: Rci,
    medium: Rci,
    long: Rci,
}

/// Output của RCI Ribbon.
#[derive(Debug, Clone, Copy)]
pub struct RciRibbonValue {
    pub short: f64,
    pub medium: f64,
    pub long: f64,
}

impl RciRibbon {
    pub fn new(short_period: usize, medium_period: usize, long_period: usize) -> Self {
        Self {
            short: Rci::new(short_period),
            medium: Rci::new(medium_period),
            long: Rci::new(long_period),
        }
    }

    /// RCI Ribbon(9, 26, 52) — cấu hình tiêu chuẩn Nhật.
    pub fn standard() -> Self {
        Self::new(9, 26, 52)
    }

    /// Trả về `Some` khi cả 3 period đều warm up xong.
    pub fn update(&mut self, price: f64) -> Option<RciRibbonValue> {
        let s = self.short.update(price);
        let m = self.medium.update(price);
        let l = self.long.update(price);
        match (s, m, l) {
            (Some(sv), Some(mv), Some(lv)) => Some(RciRibbonValue {
                short: sv,
                medium: mv,
                long: lv,
            }),
            _ => None,
        }
    }

    pub fn reset(&mut self) {
        self.short.reset();
        self.medium.reset();
        self.long.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_rci_perfect_uptrend() {
        // Giá tăng đơn điệu → RCI = +100
        let mut rci = Rci::new(5);
        let mut last = None;
        for i in 0..5 { last = rci.update(i as f64 + 1.0); }
        assert!((last.unwrap() - 100.0).abs() < 1e-8, "RCI={}", last.unwrap());
    }

    #[test]
    fn test_rci_perfect_downtrend() {
        // Giá giảm đơn điệu → RCI = -100
        let mut rci = Rci::new(5);
        let mut last = None;
        for i in 0..5 { last = rci.update(5.0 - i as f64); }
        assert!((last.unwrap() + 100.0).abs() < 1e-8, "RCI={}", last.unwrap());
    }

    #[test]
    fn test_rci_range() {
        let mut rci = Rci::new(9);
        let mut last = None;
        for i in 0..30 {
            last = rci.update(100.0 + (i as f64 * 0.7).sin() * 10.0);
        }
        let v = last.unwrap();
        assert!(v >= -100.0 && v <= 100.0, "RCI={v}");
    }

    #[test]
    fn test_rci_ribbon_warmup() {
        let mut ribbon = RciRibbon::new(3, 5, 9);
        let mut last = None;
        for i in 0..20 { last = ribbon.update(100.0 + i as f64); }
        assert!(last.is_some());
    }

    #[test]
    fn test_rci_reset() {
        let mut rci = Rci::new(5);
        for i in 0..10 { rci.update(i as f64); }
        assert!(rci.is_ready());
        rci.reset();
        assert!(!rci.is_ready());
    }
}
