use crate::Smma;

/// Average True Range (ATR) — đo volatility của thị trường.
///
/// Được Welles Wilder phát triển năm 1978. ATR không cho biết hướng giá —
/// chỉ đo mức độ biến động. Là nền tảng của nhiều indicator khác (SuperTrend,
/// Keltner, Chandelier Exit, Chande Kroll Stop, v.v.).
///
/// # Công thức (Wilder gốc — khớp với TradingView/MT4)
/// ```text
/// TR = max(
///   high - low,              ← biên độ trong bar
///   |high - prev_close|,     ← gap lên + high
///   |low  - prev_close|      ← gap xuống + low
/// )
///
/// ATR = SMMA(TR, period)     ← alpha = 1/n (Wilder smoothing)
/// ```
/// Note: codebase này dùng SMMA (RMA), khớp với ATR gốc của Wilder và
/// implementation của TradingView / MT4 / TA-Lib. EMA(2/(n+1)) sẽ cho ATR
/// khoảng ~2× nhỏ hơn (alpha bigger ⇒ reacts faster ⇒ smaller smoothed value).
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
/// Cần `period` bar để SMMA warm up.
#[derive(Debug, Clone)]
pub struct Atr {
    _period: usize,
    smma: Smma,
    prev_close: Option<f64>,
}

/// Kết quả ATR: true range của bar hiện tại và ATR đã smoothed.
///
/// `atr` là **giá trị tuyệt đối theo đơn vị giá** (e.g. ATR=3000 nghĩa là
/// trung bình daily range = $3000 — không phải 3000% hay 3.0). Để hiển thị
/// theo phần trăm trên chart, chia `atr` cho close của bar hiện tại.
#[derive(Debug, Clone, Copy)]
pub struct AtrValue {
    /// True Range của bar hiện tại (chưa smoothed). Đơn vị: giá.
    pub tr: f64,
    /// ATR: Wilder-smoothed (SMMA) của TR. Đơn vị: giá. Khớp TradingView/MT4.
    pub atr: f64,
}

impl Atr {
    pub fn new(period: usize) -> Self {
        Self {
            _period: period,
            smma: Smma::new(period),
            prev_close: None,
        }
    }

    pub fn description() -> &'static str {
        "Average True Range — Wilder-smoothed average of the true range (high-low, high-prev_close, low-prev_close). Measures volatility; used for stop sizing and position management."
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

        self.smma.update(tr).map(|atr| AtrValue { tr, atr })
    }

    pub fn value(&self) -> Option<f64> {
        self.smma.value()
    }

    pub fn is_ready(&self) -> bool {
        self.smma.is_ready()
    }

    pub fn reset(&mut self) {
        self.smma.reset();
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

    /// Sanity: when every bar has identical OHLC (so TR = high-low = const),
    /// ATR after the seed equals exactly that constant. Smoothing of a
    /// constant series is the constant.
    #[test]
    fn atr_of_constant_tr_equals_that_constant() {
        let mut atr = Atr::new(14);
        let mut last = None;
        for _ in 0..30 {
            // h=110, l=100, c=105 → TR = max(10, |110-105|, |100-105|) = 10
            last = atr.update(110.0, 100.0, 105.0);
        }
        let v = last.unwrap();
        assert!((v.atr - 10.0).abs() < 1e-9, "atr={}", v.atr);
        assert!((v.tr  - 10.0).abs() < 1e-9);
    }

    /// Verify the exact Wilder/SMMA smoothing formula:
    ///   ATR_t = ATR_{t-1} + (TR_t - ATR_{t-1}) / n
    /// 14 constant TR=10 bars → ATR=10. Then one TR=100 spike → 16.428571…
    /// Then another TR=10 → 10 + (10 - 16.428571)/14 = 15.969388…
    #[test]
    fn atr_wilder_smoothing_after_spike() {
        let mut atr = Atr::new(14);
        // 14 bars TR = 10. Seed = SMA(10*14) = 10.
        for _ in 0..14 {
            atr.update(110.0, 100.0, 105.0);
        }
        // Bar 15: spike. high=200, low=100, close=150.
        // prev_close = 105 → TR = max(100, |200-105|=95, |100-105|=5) = 100.
        let after_spike = atr.update(200.0, 100.0, 150.0).unwrap();
        let expected_spike = 10.0 + (100.0 - 10.0) / 14.0; // 16.428571…
        assert!((after_spike.atr - expected_spike).abs() < 1e-9,
            "spike atr = {}, expected {}", after_spike.atr, expected_spike);
        // Bar 16: TR back to 10. high=160, low=150, close=155. prev_close=150.
        // TR = max(10, |160-150|, |150-150|) = 10.
        let after_calm = atr.update(160.0, 150.0, 155.0).unwrap();
        let expected_calm = expected_spike + (10.0 - expected_spike) / 14.0;
        assert!((after_calm.atr - expected_calm).abs() < 1e-9,
            "calm atr = {}, expected {}", after_calm.atr, expected_calm);
    }

    /// ATR is reported in **price units**, not percentage. For an asset
    /// priced ~$60,000 with typical daily moves of ~5% (i.e. ~$3,000), an
    /// ATR(14) in the $3,000 range is the correct, expected magnitude.
    /// This test reproduces that scale on synthetic BTC-shaped data and
    /// asserts the values land where Wilder's formula says they should.
    #[test]
    fn atr_on_btc_scale_data_reports_price_units_not_percent() {
        let mut atr = Atr::new(14);
        // 20 bars at ~$65k with $3,000 daily range, close near midpoint.
        // h=66_500, l=63_500, c=65_000 → range = 3_000.
        // For bar 2+: prev_close = 65_000, so TR = max(3000, |66500-65000|=1500,
        // |63500-65000|=1500) = 3000.
        let mut last = None;
        for _ in 0..20 {
            last = atr.update(66_500.0, 63_500.0, 65_000.0);
        }
        let v = last.unwrap();
        // ATR converges to TR = 3_000 once seeded.
        assert!((v.atr - 3_000.0).abs() < 1e-6,
            "ATR should equal 3000 for constant TR=3000, got {}", v.atr);
        // For chart display: percentage = atr / close.
        let atr_pct = v.atr / 65_000.0;
        assert!((atr_pct - 3.0 / 65.0).abs() < 1e-9);
        // ≈ 4.6% — matches the expected daily volatility band for BTC.
    }

    /// Cross-check against a precomputed Wilder reference: feed 17 bars of
    /// pre-built (H, L, C) and assert the running ATR matches values
    /// computed offline (this same formula, hand-rolled — defends against
    /// any future regression in either `Atr` or `Smma`).
    #[test]
    fn atr_matches_offline_reference() {
        // (h, l, c)
        let bars: [(f64, f64, f64); 17] = [
            (10.0,  9.0,  9.5),   // bar 1: TR = 1.0
            (10.5,  9.4,  9.8),   // 2: max(1.1, |10.5-9.5|=1.0, |9.4-9.5|=0.1)=1.1
            (10.8,  9.7,  10.2),  // 3: max(1.1, |10.8-9.8|=1.0, |9.7-9.8|=0.1)=1.1
            (10.3,  9.9,  10.0),  // 4: max(0.4, |10.3-10.2|=0.1, |9.9-10.2|=0.3)=0.4
            (10.6,  9.8,  10.4),  // 5: max(0.8, 0.6, 0.2)=0.8
            (10.9, 10.2,  10.7),  // 6: max(0.7, 0.5, 0.2)=0.7
            (11.1, 10.4,  11.0),  // 7: max(0.7, 0.4, 0.3)=0.7
            (11.4, 10.7,  11.2),  // 8: max(0.7, 0.4, 0.3)=0.7
            (11.7, 11.0,  11.5),  // 9: max(0.7, 0.5, 0.2)=0.7
            (11.9, 11.2,  11.7),  // 10: max(0.7, 0.4, 0.3)=0.7
            (12.2, 11.5,  12.0),  // 11: max(0.7, 0.5, 0.2)=0.7
            (12.4, 11.7,  12.2),  // 12: max(0.7, 0.4, 0.3)=0.7
            (12.7, 12.0,  12.5),  // 13: max(0.7, 0.5, 0.2)=0.7
            (12.9, 12.2,  12.7),  // 14: max(0.7, 0.4, 0.3)=0.7
            (13.2, 12.5,  13.0),  // 15: max(0.7, 0.5, 0.2)=0.7
            (13.4, 12.7,  13.2),  // 16: max(0.7, 0.4, 0.3)=0.7
            (13.7, 13.0,  13.5),  // 17: max(0.7, 0.5, 0.2)=0.7
        ];
        let mut atr = Atr::new(14);
        let mut last_atr = 0.0;
        for (i, (h, l, c)) in bars.iter().enumerate() {
            if let Some(v) = atr.update(*h, *l, *c) {
                last_atr = v.atr;
                if i == 13 {
                    // Bar 14: ATR = SMA of TR_1..TR_14 (sum = 10.7).
                    let trs = [1.0, 1.1, 1.1, 0.4, 0.8, 0.7, 0.7, 0.7, 0.7, 0.7, 0.7, 0.7, 0.7, 0.7];
                    let expected = trs.iter().sum::<f64>() / 14.0;
                    assert!((v.atr - expected).abs() < 1e-9,
                        "seed bar 14: atr={}, expected={}", v.atr, expected);
                    assert!((expected - 10.7 / 14.0).abs() < 1e-9);
                }
            }
        }
        // Bars 15, 16, 17 all have TR=0.7. Each step:
        //   ATR_new = ATR_old + (0.7 - ATR_old) / 14
        // Seed (bar 14) = sum(TR_1..TR_14) / 14
        //               = (1.0 + 1.1 + 1.1 + 0.4 + 0.8 + 9*0.7) / 14
        //               = (4.4 + 6.3) / 14 = 10.7 / 14 = 0.7642857…
        let seed = 10.7 / 14.0;
        let after15 = seed + (0.7 - seed) / 14.0;
        let after16 = after15 + (0.7 - after15) / 14.0;
        let after17 = after16 + (0.7 - after16) / 14.0;
        assert!((last_atr - after17).abs() < 1e-9,
            "after bar 17: atr={}, expected={}", last_atr, after17);
    }
}
