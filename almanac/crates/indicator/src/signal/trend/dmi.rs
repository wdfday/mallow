use crate::Smma;

/// Directional Movement Index (DMI) — nền tảng của ADX, đo hướng và độ mạnh trend.
///
/// Được Welles Wilder phát triển cùng lúc với ADX (1978). DMI gồm hai đường:
/// - **+DI** (Positive Directional Indicator): sức mạnh upward movement
/// - **-DI** (Negative Directional Indicator): sức mạnh downward movement
///
/// # Quan hệ với ADX
/// ```text
/// DMI  → cung cấp +DI và -DI (hướng trend)
/// ADX  = SMMA(DX)             (độ mạnh trend, không có hướng)
///
/// DX = |+DI − -DI| / (+DI + -DI) × 100
/// ```
/// ADX trong codebase (`adx.rs`) tính thêm một lớp SMMA trên DX.
/// DMI standalone hữu ích khi chỉ cần biết **hướng** mà không cần độ mạnh tổng thể.
///
/// # Tín hiệu giao dịch
/// - **+DI cắt lên trên -DI** → tín hiệu long (momentum upward)
/// - **-DI cắt lên trên +DI** → tín hiệu short (momentum downward)
/// - Hai đường hội tụ gần nhau → thị trường sideways / không xu hướng
/// - `dx` cao (> 25) kết hợp crossover → tín hiệu mạnh hơn
///
/// # Công thức (Wilder's smoothing = SMMA)
/// ```text
/// +DM(t) = max(high(t) − high(t-1), 0)   nếu > -DM(t), ngược lại = 0
/// -DM(t) = max(low(t-1) − low(t), 0)     nếu > +DM(t), ngược lại = 0
/// TR(t)  = max(high−low, |high−prev_close|, |low−prev_close|)
///
/// smooth_+DM = SMMA(n, +DM)
/// smooth_-DM = SMMA(n, -DM)
/// smooth_TR  = SMMA(n, TR)
///
/// +DI = 100 × smooth_+DM / smooth_TR
/// -DI = 100 × smooth_-DM / smooth_TR
/// DX  = 100 × |+DI − -DI| / (+DI + -DI)
/// ```
#[derive(Debug, Clone)]
pub struct Dmi {
    plus_dm_smma: Smma,
    minus_dm_smma: Smma,
    tr_smma: Smma,
    prev_high: Option<f64>,
    prev_low: Option<f64>,
    prev_close: Option<f64>,
}

/// Output của DMI.
#[derive(Debug, Clone, Copy)]
pub struct DmiValue {
    /// Positive Directional Indicator (0–100): sức mạnh upward trend
    pub plus_di: f64,
    /// Negative Directional Indicator (0–100): sức mạnh downward trend
    pub minus_di: f64,
}

impl Dmi {
    pub fn new(period: usize) -> Self {
        Self {
            plus_dm_smma: Smma::new(period),
            minus_dm_smma: Smma::new(period),
            tr_smma: Smma::new(period),
            prev_high: None,
            prev_low: None,
            prev_close: None,
        }
    }

    pub fn description() -> &'static str {
        "Directional Movement Index — raw +DI and -DI lines before ADX smoothing. Crossovers of +DI/-DI indicate directional shifts."
    }

    /// DMI(14) — period chuẩn của Wilder.
    pub fn standard() -> Self {
        Self::new(14)
    }

    /// Feed một bar mới (OHLC). Trả về `Some(DmiValue)` sau khi đủ dữ liệu.
    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<DmiValue> {
        let result = if let (Some(ph), Some(pl), Some(pc)) =
            (self.prev_high, self.prev_low, self.prev_close)
        {
            // Tính Directional Movement
            let up_move = high - ph;
            let down_move = pl - low;

            // +DM: chỉ tính khi upward move lớn hơn downward move và dương
            let plus_dm = if up_move > down_move && up_move > 0.0 { up_move } else { 0.0 };
            // -DM: chỉ tính khi downward move lớn hơn upward move và dương
            let minus_dm = if down_move > up_move && down_move > 0.0 { down_move } else { 0.0 };

            // True Range = khoảng biến động thực sự (tính cả gap qua đêm)
            let tr = (high - low)
                .max((high - pc).abs())
                .max((low - pc).abs());

            let s_plus = self.plus_dm_smma.update(plus_dm);
            let s_minus = self.minus_dm_smma.update(minus_dm);
            let s_tr = self.tr_smma.update(tr);

            match (s_plus, s_minus, s_tr) {
                (Some(sp), Some(sm), Some(st)) if st > f64::EPSILON => {
                    let plus_di  = sp / st * 100.0;
                    let minus_di = sm / st * 100.0;
                    Some(DmiValue { plus_di, minus_di })
                }
                _ => None,
            }
        } else {
            // Bar đầu tiên: chỉ lưu prev, chưa có DM
            None
        };

        self.prev_high = Some(high);
        self.prev_low = Some(low);
        self.prev_close = Some(close);

        result
    }

    pub fn is_ready(&self) -> bool {
        self.tr_smma.is_ready()
    }

    pub fn reset(&mut self) {
        self.plus_dm_smma.reset();
        self.minus_dm_smma.reset();
        self.tr_smma.reset();
        self.prev_high = None;
        self.prev_low = None;
        self.prev_close = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn feed_uptrend(dmi: &mut Dmi, n: usize) -> Option<DmiValue> {
        let mut r = None;
        for i in 0..n {
            let base = 100.0 + i as f64;
            r = dmi.update(base + 2.0, base - 1.0, base + 1.0);
        }
        r
    }

    #[test]
    fn test_dmi_range() {
        let mut dmi = Dmi::new(5);
        if let Some(v) = feed_uptrend(&mut dmi, 30) {
            assert!(v.plus_di >= 0.0 && v.plus_di <= 100.0);
            assert!(v.minus_di >= 0.0 && v.minus_di <= 100.0);
        }
    }

    #[test]
    fn test_dmi_uptrend_plus_di_dominates() {
        // Trong uptrend rõ ràng, +DI phải lớn hơn -DI
        let mut dmi = Dmi::new(5);
        let v = feed_uptrend(&mut dmi, 30).unwrap();
        assert!(
            v.plus_di > v.minus_di,
            "+DI={:.2} should dominate in uptrend, -DI={:.2}",
            v.plus_di,
            v.minus_di
        );
    }

    #[test]
    fn test_dmi_downtrend_minus_di_dominates() {
        let mut dmi = Dmi::new(5);
        let mut r = None;
        for i in 0..30 {
            let base = 200.0 - i as f64;
            r = dmi.update(base + 1.0, base - 2.0, base - 1.0);
        }
        let v = r.unwrap();
        assert!(
            v.minus_di > v.plus_di,
            "-DI={:.2} should dominate in downtrend, +DI={:.2}",
            v.minus_di,
            v.plus_di
        );
    }

    #[test]
    fn test_dmi_reset() {
        let mut dmi = Dmi::new(5);
        feed_uptrend(&mut dmi, 20);
        assert!(dmi.is_ready());
        dmi.reset();
        assert!(!dmi.is_ready());
    }
}
