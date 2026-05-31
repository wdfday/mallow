/// On-Balance Volume (OBV) — volume lũy kế theo hướng giá.
///
/// Được Joseph Granville phát triển năm 1963. OBV dựa trên lý thuyết:
/// volume dẫn trước giá (volume leads price). Khi OBV tăng trong khi giá đi
/// ngang hoặc giảm → tiền đang tích lũy → sắp có uptrend.
///
/// # Công thức
/// ```text
/// close > prev_close: OBV += volume   ← bar tăng, volume vào (buying pressure)
/// close < prev_close: OBV -= volume   ← bar giảm, volume ra (selling pressure)
/// close = prev_close: OBV không đổi  ← trung lập
/// ```
///
/// OBV là chỉ số tuyệt đối (không có range). Giá trị tuyệt đối không quan trọng;
/// **hướng và divergence** mới quan trọng.
///
/// # Tín hiệu giao dịch
/// - **OBV tăng + giá tăng**: uptrend được volume xác nhận → bullish
/// - **OBV giảm + giá giảm**: downtrend được xác nhận → bearish
/// - **Bullish divergence**: giá tạo lower low nhưng OBV tạo higher low → reversal lên sắp xảy ra
/// - **Bearish divergence**: giá tạo higher high nhưng OBV tạo lower high → reversal xuống
/// - **OBV breakout trước giá**: OBV phá resistance trong khi giá chưa → sắp breakout
///
/// # Hạn chế
/// - Nhạy cảm với "gap" volume — bar có gap up với volume lớn ảnh hưởng mạnh
/// - Không chuẩn hóa → khó so sánh giữa các cổ phiếu/asset khác nhau
/// - CMF và MFI bổ sung Typical Price nên phân biệt được quality của volume tốt hơn
///
/// # Warmup
/// Trả về giá trị từ bar thứ 1 (không có warmup). Bar đầu tiên: OBV = 0 (không có prev_close).
#[derive(Debug, Clone)]
pub struct Obv {
    value: f64,
    prev_close: Option<f64>,
    count: usize,
}

impl Obv {
    pub fn new() -> Self {
        Self {
            value: 0.0,
            prev_close: None,
            count: 0,
        }
    }

    pub fn description() -> &'static str {
        "On-Balance Volume — cumulative total adding or subtracting volume based on whether the close is higher or lower than the prior close. Divergence from price signals trend weakness. Outputs a single value (cumulative volume)."
    }

    pub fn update(&mut self, close: f64, volume: f64) -> f64 {
        if let Some(pc) = self.prev_close {
            if close > pc {
                self.value += volume;
            } else if close < pc {
                self.value -= volume;
            }
        }
        self.prev_close = Some(close);
        self.count += 1;
        self.value
    }

    pub fn value(&self) -> f64 {
        self.value
    }

    pub fn is_ready(&self) -> bool {
        self.count >= 2
    }

    pub fn reset(&mut self) {
        self.value = 0.0;
        self.prev_close = None;
        self.count = 0;
    }
}

impl Default for Obv {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_obv_basic() {
        let mut obv = Obv::new();
        obv.update(10.0, 100.0); // first bar, no prev
        assert_eq!(obv.value(), 0.0);

        obv.update(11.0, 200.0); // up → +200
        assert_eq!(obv.value(), 200.0);

        obv.update(10.0, 150.0); // down → -150
        assert_eq!(obv.value(), 50.0);

        obv.update(10.0, 300.0); // flat → unchanged
        assert_eq!(obv.value(), 50.0);
    }
}
