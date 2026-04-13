use crate::Ema;

/// Average True Range (ATR) — đo volatility của thị trường.
///
/// Được Welles Wilder phát triển năm 1978. ATR không cho biết hướng giá —
/// chỉ đo mức độ biến động. Là nền tảng của nhiều indicator khác (SuperTrend,
/// Keltner, Chandelier Exit, Chande Kroll Stop, v.v.).
///
/// # Công thức
/// ```text
/// TR = max(
///   high - low,              ← biên độ trong bar
///   |high - prev_close|,     ← gap lên + high
///   |low  - prev_close|      ← gap xuống + low
/// )
///
/// ATR = EMA(TR, period)
/// ```
/// Note: Wilder gốc dùng SMMA (alpha=1/n); codebase này dùng EMA (alpha=2/(n+1))
/// vì EMA phổ biến hơn trong các thư viện hiện đại. Kết quả gần tương đương.
///
/// # Ứng dụng thực tế
/// - **Position sizing**: risk = ATR × lot_size (1 ATR = 1 unit risk)
/// - **Stop loss**: đặt stop cách entry 1.5–3× ATR
/// - **Trailing stop**: Chandelier Exit = HH − 3×ATR
/// - **Volatility filter**: chỉ giao dịch khi ATR > ngưỡng tối thiểu
/// - **Breakout xác nhận**: breakout đáng tin khi volume cao và ATR mở rộng
///
/// # Warmup
/// Bar đầu tiên: không có `prev_close` → TR = high - low.
/// Cần `period` bar để EMA warm up.
#[derive(Debug, Clone)]
pub struct Atr {
    _period: usize,
    ema: Ema,
    prev_close: Option<f64>,
}

/// Kết quả ATR: true range của bar hiện tại và ATR đã smoothed.
#[derive(Debug, Clone, Copy)]
pub struct AtrValue {
    /// True Range của bar hiện tại (chưa smoothed)
    pub tr: f64,
    /// ATR: EMA của TR (đã smoothed)
    pub atr: f64,
}

impl Atr {
    pub fn new(period: usize) -> Self {
        Self {
            _period: period,
            ema: Ema::new(period),
            prev_close: None,
        }
    }

    /// Standard ATR(14)
    pub fn standard() -> Self {
        Self::new(14)
    }

    /// Feed high, low, close. Returns ATR once ready.
    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<AtrValue> {
        let tr = match self.prev_close {
            Some(pc) => {
                let hl = high - low;
                let hc = (high - pc).abs();
                let lc = (low - pc).abs();
                hl.max(hc).max(lc)
            }
            None => high - low,
        };
        self.prev_close = Some(close);

        self.ema.update(tr).map(|atr| AtrValue { tr, atr })
    }

    pub fn value(&self) -> Option<f64> {
        self.ema.value()
    }

    pub fn is_ready(&self) -> bool {
        self.ema.is_ready()
    }

    pub fn reset(&mut self) {
        self.ema.reset();
        self.prev_close = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_atr_basic() {
        let mut atr = Atr::new(3);
        // Bar 1: no prev_close, TR = H - L
        assert!(atr.update(12.0, 10.0, 11.0).is_none());
        // Bar 2
        assert!(atr.update(13.0, 10.5, 12.0).is_none());
        // Bar 3 — should be ready
        let v = atr.update(14.0, 11.0, 13.0);
        assert!(v.is_some());
        assert!(v.unwrap().atr > 0.0);
    }
}
