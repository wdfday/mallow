use std::collections::VecDeque;

use crate::Smma;

#[derive(Debug, Clone)]
pub struct AlligatorValue {
    /// Jaw (blue): SMMA(13) of median price — shifted 8 bars forward.
    pub jaw: f64,
    /// Teeth (red): SMMA(8) of median price — shifted 5 bars forward.
    pub teeth: f64,
    /// Lips (green): SMMA(5) of median price — shifted 3 bars forward.
    pub lips: f64,
    /// `true` when Lips > Teeth > Jaw (bullish spread / alligator eating upward).
    pub bullish: bool,
    /// `true` when Lips < Teeth < Jaw (bearish spread / alligator eating downward).
    /// `bullish == false && bearish == false` ⇒ lines intertwined = alligator sleeping.
    pub bearish: bool,
}

/// Williams Alligator — ba đường SMMA mô phỏng hành vi của cá sấu.
///
/// Được Bill Williams phát triển. Ba đường đặt tên theo "cơ quan" của cá sấu:
/// - **Jaw** (hàm): SMMA(13) của median price, shift 8 bar về tương lai
/// - **Teeth** (răng): SMMA(8), shift 5 bar
/// - **Lips** (môi): SMMA(5), shift 3 bar
///
/// Metaphor: "Cá sấu ăn" khi ba đường mở rộng ra và theo thứ tự (Lips > Teeth > Jaw
/// trong uptrend). "Cá sấu ngủ" khi ba đường đan chéo nhau — không trade.
///
/// # Forward shift
/// Mỗi đường được **dịch về tương lai** một số bar khác nhau (jaw 8, teeth 5, lips 3).
/// Trong indicator streaming, "dịch tới `n` bar" được hiện thực bằng cách **trễ output
/// lại `n` bar**: giá trị SMMA tính ở bar `t` chỉ được phát ra tại bar `t + n`. Vì vậy
/// tại bar hiện tại ta so sánh Lips(t−3) vs Teeth(t−5) vs Jaw(t−8) — đúng như những gì
/// hiển thị trên chart. Toàn bộ là dữ liệu quá khứ → **không** look-ahead bias.
///
/// # Tín hiệu
/// - **Lips > Teeth > Jaw** (`bullish = true`): alligator đang ăn lên → long
/// - **Lips < Teeth < Jaw** (`bearish = true`): alligator đang ăn xuống → short
/// - **Ba đường đan nhau** (`bullish == bearish == false`): cá sấu ngủ → không trade
///
/// # Warmup
/// Mỗi đường cần `period + shift` bar (SMMA seed `period` bar, cộng `shift` bar trễ).
/// Default(13,8,5): max(13+8, 8+5, 5+3) = 21 bar trước giá trị đầu tiên.
#[derive(Clone)]
pub struct Alligator {
    jaw: Smma,
    teeth: Smma,
    lips: Smma,
    jaw_period: usize,
    teeth_period: usize,
    lips_period: usize,
    jaw_shift: usize,
    teeth_shift: usize,
    lips_shift: usize,
    jaw_buf: VecDeque<f64>,
    teeth_buf: VecDeque<f64>,
    lips_buf: VecDeque<f64>,
}

/// Standard Williams forward shifts (bars): jaw=8, teeth=5, lips=3.
const JAW_SHIFT: usize = 8;
const TEETH_SHIFT: usize = 5;
const LIPS_SHIFT: usize = 3;

/// Push a fresh SMMA value into its delay buffer and return the value from
/// `shift` emissions ago (i.e. the value forward-shifted onto the current bar).
/// Returns `None` until the buffer has accumulated `shift + 1` values.
fn shifted(buf: &mut VecDeque<f64>, value: Option<f64>, shift: usize) -> Option<f64> {
    if let Some(v) = value {
        buf.push_back(v);
        if buf.len() > shift + 1 {
            buf.pop_front();
        }
    }
    if buf.len() == shift + 1 {
        buf.front().copied()
    } else {
        None
    }
}

impl Alligator {
    /// Standard Williams periods: jaw=13, teeth=8, lips=5 (shifts 8/5/3).
    pub fn new(jaw_period: usize, teeth_period: usize, lips_period: usize) -> Self {
        Self {
            jaw: Smma::new(jaw_period),
            teeth: Smma::new(teeth_period),
            lips: Smma::new(lips_period),
            jaw_period,
            teeth_period,
            lips_period,
            jaw_shift: JAW_SHIFT,
            teeth_shift: TEETH_SHIFT,
            lips_shift: LIPS_SHIFT,
            jaw_buf: VecDeque::with_capacity(JAW_SHIFT + 1),
            teeth_buf: VecDeque::with_capacity(TEETH_SHIFT + 1),
            lips_buf: VecDeque::with_capacity(LIPS_SHIFT + 1),
        }
    }

    pub fn description() -> &'static str {
        "Williams Alligator — three smoothed MAs (Jaw 13, Teeth 8, Lips 5) offset into the future. Lips > Teeth > Jaw = alligator eating upward (bullish); Lips < Teeth < Jaw = eating downward (bearish); lines intertwined = sleeping. Outputs: `.teeth` (default, SMMA 8), `.jaw` (SMMA 13), `.lips` (SMMA 5), `.bullish` (1.0 = eating upward), `.bearish` (1.0 = eating downward)."
    }

    pub fn update(&mut self, high: f64, low: f64) -> Option<AlligatorValue> {
        let median = (high + low) / 2.0;
        // Feed all three SMMAs independently — avoids cascade short-circuit where lips/teeth
        // would miss early bars while jaw is still warming up. Each output is then forward-shifted.
        let jaw_v = shifted(&mut self.jaw_buf, self.jaw.update(median), self.jaw_shift);
        let teeth_v = shifted(&mut self.teeth_buf, self.teeth.update(median), self.teeth_shift);
        let lips_v = shifted(&mut self.lips_buf, self.lips.update(median), self.lips_shift);
        let (Some(jaw), Some(teeth), Some(lips)) = (jaw_v, teeth_v, lips_v) else {
            return None;
        };
        let bullish = lips > teeth && teeth > jaw;
        let bearish = lips < teeth && teeth < jaw;
        Some(AlligatorValue { jaw, teeth, lips, bullish, bearish })
    }

    pub fn reset(&mut self) {
        self.jaw = Smma::new(self.jaw_period);
        self.teeth = Smma::new(self.teeth_period);
        self.lips = Smma::new(self.lips_period);
        self.jaw_buf.clear();
        self.teeth_buf.clear();
        self.lips_buf.clear();
    }
}

impl Default for Alligator {
    fn default() -> Self {
        Self::new(13, 8, 5)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_alligator_ready_eventually() {
        // Alligator(5,4,3) with shifts 8/5/3: jaw needs 5+8=13, teeth 4+5=9, lips 3+3=6.
        // First value gated on the slowest (jaw) → bar 13.
        let mut al = Alligator::new(5, 4, 3);
        let mut got = false;
        for i in 0..20 {
            let p = 100.0 + i as f64;
            if al.update(p + 1.0, p - 1.0).is_some() {
                got = true;
            }
        }
        assert!(got, "Alligator(5,4,3) should produce a value within 20 bars");
    }

    #[test]
    fn test_alligator_bullish_uptrend() {
        // Default Alligator(13,8,5) with shifts: max(13+8, 8+5, 5+3) = 21 bars.
        let mut al = Alligator::default();
        let mut last = None;
        for i in 0..80 {
            let p = 100.0 + i as f64 * 2.0;
            last = al.update(p + 1.0, p - 1.0);
        }
        let v = last.unwrap();
        assert!(v.bullish, "Alligator should be bullish in strong uptrend");
        assert!(!v.bearish, "bullish and bearish are mutually exclusive");
    }

    #[test]
    fn test_alligator_lips_gt_teeth_gt_jaw_in_uptrend() {
        let mut al = Alligator::default();
        let mut last = None;
        for i in 0..80 {
            let p = 100.0 + i as f64 * 2.0;
            last = al.update(p + 1.0, p - 1.0);
        }
        let v = last.unwrap();
        assert!(v.lips > v.teeth && v.teeth > v.jaw);
    }

    #[test]
    fn test_alligator_bearish_downtrend() {
        // Strong downtrend → shifted Jaw (8 bars stale, higher price) > Teeth > Lips.
        let mut al = Alligator::default();
        let mut last = None;
        for i in 0..80 {
            let p = 300.0 - i as f64 * 2.0;
            last = al.update(p + 1.0, p - 1.0);
        }
        let v = last.unwrap();
        assert!(v.bearish, "Alligator should be bearish in strong downtrend");
        assert!(!v.bullish, "bullish and bearish are mutually exclusive");
        assert!(v.lips < v.teeth && v.teeth < v.jaw);
    }
}
