/// Chaikin Money Flow (CMF) — đo áp lực mua/bán tích hợp volume.
///
/// Được Marc Chaikin phát triển dựa trên Accumulation/Distribution của Larry Williams.
/// CMF đo liệu volume đang chảy vào (accumulation) hay ra khỏi (distribution) thị trường
/// bằng cách chuẩn hóa vị trí đóng cửa trong range của bar rồi nhân với volume.
///
/// # Công thức
/// ```text
/// MFM = ((Close − Low) − (High − Close)) / (High − Low)
///      = (2×Close − High − Low) / (High − Low)
///
/// MFM = +1: close = High (toàn bộ range là buying pressure)
/// MFM = -1: close = Low  (toàn bộ range là selling pressure)
/// MFM =  0: close = (H+L)/2 (trung tính)
///
/// MFV = MFM × Volume      ← Money Flow Volume có dấu
///
/// CMF = Σ MFV(n) / Σ Volume(n)   ← range: −1 đến +1
/// ```
///
/// # Cách đọc tín hiệu
/// - **CMF > 0**: buying pressure; càng gần +1 càng mạnh
/// - **CMF < 0**: selling pressure; càng gần −1 càng mạnh
/// - **CMF cắt 0 từ dưới lên**: accumulation bắt đầu → buy signal
/// - **CMF cắt 0 từ trên xuống**: distribution → sell signal
/// - **CMF > +0.25**: buying pressure mạnh (institutional accumulation)
/// - **CMF < −0.25**: selling pressure mạnh
///
/// # Ưu điểm vs OBV
/// - CMF chuẩn hóa theo vị trí close trong range → phân biệt được "close near high"
///   và "close near low" trong cùng một bar có volume lớn
/// - OBV chỉ biết bar tăng hay giảm; CMF biết *mức độ* áp lực mua/bán
///
/// # Warmup
/// Cần đúng `period` bar.

use std::collections::VecDeque;

#[derive(Clone)]
pub struct Cmf {
    period: usize,
    mfv_buf: VecDeque<f64>,
    vol_buf: VecDeque<f64>,
    mfv_sum: f64,
    vol_sum: f64,
}

impl Cmf {
    pub fn new(period: usize) -> Self {
        Self {
            period,
            mfv_buf: VecDeque::with_capacity(period),
            vol_buf: VecDeque::with_capacity(period),
            mfv_sum: 0.0,
            vol_sum: 0.0,
        }
    }

    pub fn description() -> &'static str {
        "Chaikin Money Flow — sum of money flow volume over N bars divided by total volume. Positive = buying pressure; negative = selling pressure. Outputs a single value (−1 to +1)."
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64, volume: f64) -> Option<f64> {
        let range = high - low;
        let mfv = if range > f64::EPSILON {
            let mfm = ((close - low) - (high - close)) / range;
            mfm * volume
        } else {
            0.0
        };

        self.mfv_buf.push_back(mfv);
        self.vol_buf.push_back(volume);
        self.mfv_sum += mfv;
        self.vol_sum += volume;

        if self.mfv_buf.len() > self.period {
            self.mfv_sum -= self.mfv_buf.pop_front().unwrap();
            self.vol_sum -= self.vol_buf.pop_front().unwrap();
        }

        if self.mfv_buf.len() == self.period && self.vol_sum > f64::EPSILON {
            Some(self.mfv_sum / self.vol_sum)
        } else {
            None
        }
    }

    pub fn reset(&mut self) {
        self.mfv_buf.clear();
        self.vol_buf.clear();
        self.mfv_sum = 0.0;
        self.vol_sum = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_warmup() {
        let mut cmf = Cmf::new(20);
        for i in 0..20 {
            let v = cmf.update(110.0, 90.0, 100.0, 1000.0);
            if i < 19 {
                assert!(v.is_none());
            } else {
                assert!(v.is_some());
            }
        }
    }

    #[test]
    fn test_neutral_close() {
        // Close at midpoint → MFM = 0 → CMF = 0
        let mut cmf = Cmf::new(5);
        let mut last = None;
        for _ in 0..10 {
            last = cmf.update(110.0, 90.0, 100.0, 1000.0);
        }
        assert!(last.unwrap().abs() < 0.001);
    }

    #[test]
    fn test_bullish() {
        // Close near high → positive CMF
        let mut cmf = Cmf::new(5);
        let mut last = None;
        for _ in 0..10 {
            last = cmf.update(110.0, 90.0, 108.0, 1000.0);
        }
        assert!(last.unwrap() > 0.5);
    }

    #[test]
    fn test_reset() {
        let mut cmf = Cmf::new(5);
        for _ in 0..10 { cmf.update(110.0, 90.0, 100.0, 1000.0); }
        cmf.reset();
        assert!(cmf.update(110.0, 90.0, 100.0, 1000.0).is_none());
    }
}
