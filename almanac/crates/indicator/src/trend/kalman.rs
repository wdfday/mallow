/// Kalman Filter — bộ lọc tối ưu 2-state cho chuỗi giá.
///
/// State vector: `[position, velocity]`
/// - **position** = ước tính giá thực (smoothed price, loại bỏ noise)
/// - **velocity** = tốc độ thay đổi giá theo bar (slope estimate, dương = uptrend)
///
/// # Mô hình
/// ```text
/// State transition:  [x(t)]   [1  1] [x(t-1)]   [process_noise]
///                    [v(t)] = [0  1] [v(t-1)] + [               ]
///
/// Observation:       z(t) = [1  0] [x(t)] + measurement_noise
///                                  [v(t)]
/// ```
///
/// # Tham số
/// - `q_pos` — process noise cho position (mức độ giá có thể nhảy/bar, default 0.001)
/// - `q_vel` — process noise cho velocity (mức độ trend có thể đổi/bar, default 0.001)
/// - `r`     — measurement noise (độ ồn của price data, default 1.0)
///
/// Tỷ lệ `q/r` quyết định tốc độ phản ứng:
/// - `q/r` lớn → nhanh hơn (gần EMA ngắn hạn)
/// - `q/r` nhỏ → mượt hơn (gần EMA dài hạn)
///
/// # So sánh với EMA
/// | | Kalman | EMA |
/// |---|---|---|
/// | Lag | thích ứng (tự điều chỉnh gain) | cố định (1/period) |
/// | Output | position + velocity | chỉ value |
/// | Params | q, r | period |
/// | Ứng dụng | price estimation + trend detection | smoothing + crossover |
///
/// # Warmup
/// Không cần warmup — bar đầu tiên khởi tạo state = price, velocity = 0.
/// Giá trị ổn định sau ~10-20 bar.
///
/// # Ứng dụng
/// - `value` dùng như dynamic support/resistance (thay EMA)
/// - `velocity > 0` = uptrend, `velocity < 0` = downtrend → entry filter
/// - Signal: price crosses above/below `value` kết hợp với `velocity` sign
#[derive(Debug, Clone)]
pub struct KalmanFilter {
    /// Process noise — position component (q_x).
    pub q_pos: f64,
    /// Process noise — velocity component (q_v).
    pub q_vel: f64,
    /// Measurement noise (R).
    pub r: f64,

    // State estimates
    x_hat: f64,
    v_hat: f64,

    // Error covariance matrix (2×2, stored flat)
    p00: f64,
    p01: f64,
    p10: f64,
    p11: f64,

    initialized: bool,
}

/// Output of `KalmanFilter::update`.
#[derive(Debug, Clone, Copy)]
pub struct KalmanValue {
    /// Filtered price estimate (position state).
    pub value: f64,
    /// Trend slope per bar (velocity state). Positive = uptrend.
    pub velocity: f64,
}

impl KalmanFilter {
    /// Create with explicit parameters.
    ///
    /// Recommended starting point: `q_pos = 0.001, q_vel = 0.001, r = 1.0`.
    /// Increase `q_pos` / decrease `r` for faster reaction; decrease for smoother output.
    pub fn new(q_pos: f64, q_vel: f64, r: f64) -> Self {
        assert!(q_pos >= 0.0, "q_pos must be >= 0");
        assert!(q_vel >= 0.0, "q_vel must be >= 0");
        assert!(r > 0.0, "r (measurement noise) must be > 0");
        Self {
            q_pos,
            q_vel,
            r,
            x_hat: 0.0,
            v_hat: 0.0,
            // Large initial covariance = high uncertainty before any observation.
            p00: 1.0,
            p01: 0.0,
            p10: 0.0,
            p11: 1.0,
            initialized: false,
        }
    }

    /// Convenience constructor with defaults.
    pub fn default_params() -> Self {
        Self::new(0.001, 0.001, 1.0)
    }

    /// Feed next price observation. Returns filtered `(value, velocity)` immediately.
    ///
    /// On the very first bar the filter bootstraps: `value = price`, `velocity = 0`.
    pub fn update(&mut self, price: f64) -> KalmanValue {
        if !self.initialized {
            self.x_hat = price;
            self.v_hat = 0.0;
            self.initialized = true;
            return KalmanValue { value: price, velocity: 0.0 };
        }

        // ── Predict ──────────────────────────────────────────────────────────
        // State transition A = [[1,1],[0,1]]: x_prior = x + v, v_prior = v
        let x_prior = self.x_hat + self.v_hat;
        let v_prior = self.v_hat;

        // P_prior = A * P * A^T + Q
        // A*P: row0 = (p00+p10, p01+p11), row1 = (p10, p11)
        // (A*P)*A^T: col0 = (p00+p10+p01+p11, p10+p11), col1 = (p01+p11, p11)
        let p00_p = self.p00 + self.p10 + self.p01 + self.p11 + self.q_pos;
        let p01_p = self.p01 + self.p11;
        let p10_p = self.p10 + self.p11;
        let p11_p = self.p11 + self.q_vel;

        // ── Update ────────────────────────────────────────────────────────────
        // H = [1, 0]  →  S = H*P_prior*H^T + R = p00_p + R
        let s = p00_p + self.r;

        // Kalman gain: K = P_prior * H^T / S  →  K = [p00_p/s, p10_p/s]
        let k0 = p00_p / s;
        let k1 = p10_p / s;

        // Innovation
        let y = price - x_prior;

        self.x_hat = x_prior + k0 * y;
        self.v_hat = v_prior + k1 * y;

        // P = (I - K*H) * P_prior
        // (I - K*H) = [[1-k0, 0], [-k1, 1]]
        self.p00 = (1.0 - k0) * p00_p;
        self.p01 = (1.0 - k0) * p01_p;
        self.p10 = -k1 * p00_p + p10_p;
        self.p11 = -k1 * p01_p + p11_p;

        KalmanValue { value: self.x_hat, velocity: self.v_hat }
    }

    /// Current filtered value without feeding new data.
    pub fn value(&self) -> Option<f64> {
        if self.initialized { Some(self.x_hat) } else { None }
    }

    /// Current velocity estimate without feeding new data.
    pub fn velocity(&self) -> Option<f64> {
        if self.initialized { Some(self.v_hat) } else { None }
    }

    pub fn is_ready(&self) -> bool {
        self.initialized
    }

    pub fn reset(&mut self) {
        self.x_hat = 0.0;
        self.v_hat = 0.0;
        self.p00 = 1.0;
        self.p01 = 0.0;
        self.p10 = 0.0;
        self.p11 = 1.0;
        self.initialized = false;
    }
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_first_bar_equals_price() {
        let mut kf = KalmanFilter::default_params();
        let v = kf.update(100.0);
        assert_eq!(v.value, 100.0);
        assert_eq!(v.velocity, 0.0);
    }

    #[test]
    fn test_constant_price_converges() {
        let mut kf = KalmanFilter::default_params();
        let price = 50.0;
        let mut last = KalmanValue { value: 0.0, velocity: 0.0 };
        for _ in 0..200 {
            last = kf.update(price);
        }
        // After many constant-price bars, filtered value should be very close to the price.
        assert!((last.value - price).abs() < 0.01, "value={}", last.value);
        assert!(last.velocity.abs() < 0.01, "velocity={}", last.velocity);
    }

    #[test]
    fn test_uptrend_velocity_positive() {
        let mut kf = KalmanFilter::new(0.01, 0.01, 1.0);
        let mut last = KalmanValue { value: 0.0, velocity: 0.0 };
        for i in 0..100 {
            last = kf.update(100.0 + i as f64);
        }
        assert!(last.velocity > 0.0, "velocity should be positive in uptrend");
        // Velocity should be close to 1.0 (price rises by 1 per bar)
        assert!((last.velocity - 1.0).abs() < 0.1, "velocity={}", last.velocity);
    }

    #[test]
    fn test_downtrend_velocity_negative() {
        let mut kf = KalmanFilter::new(0.01, 0.01, 1.0);
        let mut last = KalmanValue { value: 0.0, velocity: 0.0 };
        for i in 0..100 {
            last = kf.update(200.0 - i as f64);
        }
        assert!(last.velocity < 0.0, "velocity should be negative in downtrend");
    }

    #[test]
    fn test_smooth_is_less_noisy_than_raw() {
        // Add noise to a flat line; filtered output should have smaller variance.
        use std::f64::consts::PI;
        let mut kf = KalmanFilter::default_params();
        let mut raw_sum_sq = 0.0f64;
        let mut filtered_sum_sq = 0.0f64;
        let base = 100.0f64;
        let n = 500usize;
        for i in 0..n {
            // Sinusoidal noise
            let noise = 2.0 * (i as f64 * 0.3 * PI).sin();
            let price = base + noise;
            let v = kf.update(price);
            raw_sum_sq += noise.powi(2);
            filtered_sum_sq += (v.value - base).powi(2);
        }
        assert!(filtered_sum_sq < raw_sum_sq,
            "filter should reduce noise: filtered={:.2} raw={:.2}", filtered_sum_sq, raw_sum_sq);
    }

    #[test]
    fn test_reset() {
        let mut kf = KalmanFilter::default_params();
        for i in 0..50 { kf.update(100.0 + i as f64); }
        assert!(kf.is_ready());
        kf.reset();
        assert!(!kf.is_ready());
        // After reset, behaves like fresh start
        let v = kf.update(200.0);
        assert_eq!(v.value, 200.0);
    }

    #[test]
    fn test_responsiveness_vs_smoothness() {
        // Fast filter (high q/r) should track price more closely than slow filter
        let mut fast = KalmanFilter::new(0.1, 0.1, 1.0);
        let mut slow = KalmanFilter::new(0.001, 0.001, 1.0);
        let prices: Vec<f64> = (0..50).map(|i| 100.0 + i as f64).collect();
        let last_price = *prices.last().unwrap();
        let mut fast_v = KalmanValue { value: 0.0, velocity: 0.0 };
        let mut slow_v = KalmanValue { value: 0.0, velocity: 0.0 };
        for &p in &prices {
            fast_v = fast.update(p);
            slow_v = slow.update(p);
        }
        let fast_err = (fast_v.value - last_price).abs();
        let slow_err = (slow_v.value - last_price).abs();
        assert!(fast_err < slow_err,
            "fast filter should track closer: fast_err={fast_err:.3} slow_err={slow_err:.3}");
    }
}
