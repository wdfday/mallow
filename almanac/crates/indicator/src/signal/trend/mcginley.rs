/// McGinley Dynamic Indicator — MA tự điều chỉnh tốc độ theo tốc độ của giá.
///
/// Được John R. McGinley phát triển năm 1997. Ý tưởng cốt lõi: EMA và SMA có
/// period cố định, nhưng thị trường không chuyển động đều. McGinley Dynamic
/// tự làm chậm khi giá di chuyển chậm và tăng tốc khi giá phi nhanh — nhờ đó
/// bám sát giá hơn EMA mà ít whipsaw hơn.
///
/// # Cơ chế hoạt động
/// Mẫu số chứa `(price / MGD)^4`:
/// - Khi giá *trên* MGD (uptrend): `price/MGD > 1` → `(...)^4 > 1` → bước nhảy nhỏ → MA chạy chậm
/// - Khi giá *dưới* MGD (downtrend): `price/MGD < 1` → `(...)^4 < 1` → bước nhảy lớn → MA chạy nhanh
///
/// Điều này khiến McGinley Dynamic **không lag** khi giá giảm (follow thị trường
/// gấu nhanh) nhưng **không bị whipsaw** khi giá tăng chậm.
///
/// # Công thức
/// ```text
/// Seed: MGD(n) = SMA(n) bar đầu tiên
///
/// MGD(t) = MGD(t-1) + (price(t) - MGD(t-1)) / (N × (price(t) / MGD(t-1))^4)
/// ```
/// Với `N` là period. Khuyến nghị N = 12–14 cho daily bars.
///
/// # Lưu ý thực tế
/// - Khi `price / MGD` rất khác 1 (giá gap mạnh), `^4` khuếch đại hiệu ứng rất lớn
/// - Không nên dùng với tick data nhiều noise (SMMA hoặc ALMA phù hợp hơn)
#[derive(Debug, Clone)]
pub struct McGinleyDynamic {
    period: usize,
    /// Hệ số N trong công thức (float để tránh chia nguyên)
    n: f64,
    /// Giá trị hiện tại của indicator
    value: Option<f64>,
    /// Bộ đếm và tổng để seed bằng SMA
    count: usize,
    seed_sum: f64,
}

impl McGinleyDynamic {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "McGinley period must be > 0");
        Self {
            period,
            n: period as f64,
            value: None,
            count: 0,
            seed_sum: 0.0,
        }
    }

    pub fn description() -> &'static str {
        "McGinley Dynamic — self-adjusting MA that automatically varies its speed to track price regardless of market conditions. Reduces whipsaws in choppy environments."
    }

    /// McGinley Dynamic(14) — period được McGinley khuyến nghị cho daily.
    pub fn standard() -> Self {
        Self::new(14)
    }

    /// Feed một bar mới. Trả về `Some(mgd)` sau khi đủ `period` bar.
    pub fn update(&mut self, price: f64) -> Option<f64> {
        self.count += 1;

        if self.count < self.period {
            self.seed_sum += price;
            return None;
        }

        if self.count == self.period {
            // Seed = SMA(n) — giống EMA để bắt đầu từ một giá trị hợp lý
            self.seed_sum += price;
            self.value = Some(self.seed_sum / self.n);
            return self.value;
        }

        let prev = self.value.unwrap();

        // Tránh division-by-zero nếu prev = 0
        if prev.abs() < f64::EPSILON {
            self.value = Some(price);
            return self.value;
        }

        // ratio = price / MGD(t-1)
        let ratio = price / prev;
        // Bước nhảy = (price - prev) / (N × ratio^4)
        // ratio^4 < 1 khi price < prev → bước nhảy lớn hơn (bám nhanh khi giảm)
        // ratio^4 > 1 khi price > prev → bước nhảy nhỏ hơn (không lag khi tăng chậm)
        let step = (price - prev) / (self.n * ratio.powi(4));
        self.value = Some(prev + step);
        self.value
    }

    pub fn value(&self) -> Option<f64> {
        self.value
    }

    pub fn is_ready(&self) -> bool {
        self.value.is_some()
    }

    pub fn reset(&mut self) {
        self.value = None;
        self.count = 0;
        self.seed_sum = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_mcginley_warmup() {
        let mut mgd = McGinleyDynamic::new(5);
        for i in 0..4 {
            assert_eq!(mgd.update(i as f64 + 1.0), None);
        }
        assert!(mgd.update(5.0).is_some());
    }

    #[test]
    fn test_mcginley_constant_converges() {
        // Khi giá không đổi, MGD phải ổn định tại giá đó sau vài bar
        let mut mgd = McGinleyDynamic::new(5);
        let mut last = 0.0;
        for _ in 0..100 {
            if let Some(v) = mgd.update(50.0) { last = v; }
        }
        assert!((last - 50.0).abs() < 0.1, "MGD should converge to 50, got {last}");
    }

    #[test]
    fn test_mcginley_tracks_down_faster() {
        // McGinley bám nhanh hơn khi giá giảm so với khi tăng cùng biên độ
        let period = 5;
        let seed = [50.0; 5];

        let mut mgd_up = McGinleyDynamic::new(period);
        let mut mgd_down = McGinleyDynamic::new(period);

        for &p in &seed {
            mgd_up.update(p);
            mgd_down.update(p);
        }

        // Tăng đột biến lên 60
        let up_v = mgd_up.update(60.0).unwrap();
        // Giảm đột biến xuống 40 (cùng biên độ 10)
        let down_v = mgd_down.update(40.0).unwrap();

        let up_gap = (60.0 - up_v).abs();     // MGD lag so với giá tăng
        let down_gap = (40.0 - down_v).abs();  // MGD lag so với giá giảm

        // MGD bám giá giảm nhanh hơn (down_gap < up_gap)
        assert!(
            down_gap < up_gap,
            "McGinley should follow drops faster: up_gap={up_gap:.4}, down_gap={down_gap:.4}"
        );
    }

    #[test]
    fn test_mcginley_reset() {
        let mut mgd = McGinleyDynamic::new(3);
        for p in [10.0, 20.0, 30.0, 40.0] { mgd.update(p); }
        assert!(mgd.is_ready());
        mgd.reset();
        assert!(!mgd.is_ready());
    }
}
