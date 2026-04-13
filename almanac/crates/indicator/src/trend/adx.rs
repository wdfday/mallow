use crate::Ema;

/// Average Directional Index (ADX) — đo độ mạnh của xu hướng (0–100).
///
/// Được Welles Wilder phát triển năm 1978, cùng với DMI. ADX đo **mức độ
/// mạnh yếu** của trend mà không cho biết hướng. ADX cao = trend mạnh (dù
/// tăng hay giảm); ADX thấp = sideways/choppy.
///
/// # Công thức (dùng EMA thay vì SMMA gốc của Wilder trong codebase này)
/// ```text
/// +DM(t) = max(high - prev_high, 0) nếu > -DM, ngược lại = 0
/// -DM(t) = max(prev_low - low, 0)   nếu > +DM, ngược lại = 0
/// TR(t)  = max(H-L, |H-PC|, |L-PC|)
///
/// +DI = EMA(+DM, n) / EMA(TR, n) × 100
/// -DI = EMA(-DM, n) / EMA(TR, n) × 100
/// DX  = |+DI − -DI| / (+DI + -DI) × 100
/// ADX = EMA(DX, n)
/// ```
///
/// # Ngưỡng thông dụng
/// - ADX < 20: không có xu hướng rõ ràng (sideways)
/// - ADX 20–25: xu hướng bắt đầu hình thành
/// - ADX > 25: xu hướng mạnh
/// - ADX > 50: xu hướng rất mạnh (có thể sắp kiệt sức)
///
/// # Cách dùng ADX + DI lines
/// - ADX > 25 VÀ +DI > -DI → long signal mạnh
/// - ADX > 25 VÀ -DI > +DI → short signal mạnh
/// - ADX đang tăng → trend đang mạnh lên (momentum)
/// - ADX đang giảm → trend yếu đi / sắp reversal
///
/// # Warmup
/// Do cascade EMA: cần khoảng `2 × period` bar để ADX EMA warm up đầy đủ.
#[derive(Debug, Clone)]
pub struct Adx {
    _period: usize,
    plus_dm_ema: Ema,
    minus_dm_ema: Ema,
    tr_ema: Ema,
    adx_ema: Ema,
    prev_high: Option<f64>,
    prev_low: Option<f64>,
    prev_close: Option<f64>,
}

#[derive(Debug, Clone, Copy)]
pub struct AdxValue {
    pub adx: f64,
    pub plus_di: f64,
    pub minus_di: f64,
}

impl Adx {
    pub fn new(period: usize) -> Self {
        Self {
            _period: period,
            plus_dm_ema: Ema::new(period),
            minus_dm_ema: Ema::new(period),
            tr_ema: Ema::new(period),
            adx_ema: Ema::new(period),
            prev_high: None,
            prev_low: None,
            prev_close: None,
        }
    }

    /// ADX(14) — period Wilder khuyến nghị.
    pub fn standard() -> Self {
        Self::new(14)
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<AdxValue> {
        let result = if let (Some(ph), Some(pl), Some(pc)) =
            (self.prev_high, self.prev_low, self.prev_close)
        {
            // Directional movement
            let up_move = high - ph;
            let down_move = pl - low;

            let plus_dm = if up_move > down_move && up_move > 0.0 {
                up_move
            } else {
                0.0
            };
            let minus_dm = if down_move > up_move && down_move > 0.0 {
                down_move
            } else {
                0.0
            };

            // True range
            let hl = high - low;
            let hc = (high - pc).abs();
            let lc = (low - pc).abs();
            let tr = hl.max(hc).max(lc);

            let smoothed_plus = self.plus_dm_ema.update(plus_dm);
            let smoothed_minus = self.minus_dm_ema.update(minus_dm);
            let smoothed_tr = self.tr_ema.update(tr);

            match (smoothed_plus, smoothed_minus, smoothed_tr) {
                (Some(sp), Some(sm), Some(st)) if st > f64::EPSILON => {
                    let plus_di = sp / st * 100.0;
                    let minus_di = sm / st * 100.0;
                    let di_sum = plus_di + minus_di;
                    let dx = if di_sum > f64::EPSILON {
                        ((plus_di - minus_di).abs() / di_sum) * 100.0
                    } else {
                        0.0
                    };

                    self.adx_ema.update(dx).map(|adx| AdxValue {
                        adx,
                        plus_di,
                        minus_di,
                    })
                }
                _ => None,
            }
        } else {
            None
        };

        self.prev_high = Some(high);
        self.prev_low = Some(low);
        self.prev_close = Some(close);

        result
    }

    pub fn is_ready(&self) -> bool {
        self.adx_ema.is_ready()
    }

    pub fn reset(&mut self) {
        self.plus_dm_ema.reset();
        self.minus_dm_ema.reset();
        self.tr_ema.reset();
        self.adx_ema.reset();
        self.prev_high = None;
        self.prev_low = None;
        self.prev_close = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_adx_range() {
        let mut adx = Adx::new(5);
        for i in 0..40 {
            let base = 100.0 + (i as f64) * 0.5;
            if let Some(v) = adx.update(base + 2.0, base - 1.0, base + 1.0) {
                assert!(v.adx >= 0.0, "ADX should be >= 0, got {}", v.adx);
                assert!(v.plus_di >= 0.0);
                assert!(v.minus_di >= 0.0);
            }
        }
    }
}
