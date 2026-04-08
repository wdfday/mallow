use alm_core::Bar;
use alm_indicator::HeikenAshi;

/// Loại nến dùng làm input cho indicators.
///
/// Một số strategy (supertrend, alligator, trend-follow) hoạt động tốt hơn
/// trên Heiken Ashi vì noise ít hơn. `CandleType` cho phép chọn transform
/// mà không cần viết logic riêng trong từng strategy.
///
/// **Lưu ý lookahead bias:** khi dùng HA, indicators tính trên HA close
/// nhưng execution vẫn phải dùng giá raw. Backtest có thể overstate performance
/// so với live trading nếu không cẩn thận.
#[derive(Debug, Clone, Default)]
pub enum CandleType {
    /// Nến thô — không transform, mặc định.
    #[default]
    Raw,
    /// Heiken Ashi tiêu chuẩn (smooth = 1).
    HeikenAshi,
    /// Heiken Ashi có EMA smoothing trước khi tính HA.
    /// `smooth` = period EMA áp dụng cho từng OHLC trước khi tính HA.
    SmoothHeikenAshi(usize),
}

impl CandleType {
    /// Parse từ string config: `"raw"`, `"heiken_ashi"`, `"smooth_ha"`.
    /// `smooth_period` chỉ dùng cho `"smooth_ha"`.
    pub fn from_str(s: &str, smooth_period: usize) -> Self {
        match s {
            "heiken_ashi" | "ha" => Self::HeikenAshi,
            "smooth_ha" | "smooth_heiken_ashi" => Self::SmoothHeikenAshi(smooth_period.max(2)),
            _ => Self::Raw,
        }
    }
}

/// Stateful transformer — giữ HeikenAshi state giữa các bar.
#[derive(Debug, Clone)]
pub struct CandleTransform {
    pub candle_type: CandleType,
    ha: HeikenAshi,
}

impl CandleTransform {
    pub fn new(candle_type: CandleType) -> Self {
        let smooth = match &candle_type {
            CandleType::SmoothHeikenAshi(n) => *n,
            _ => 1,
        };
        Self {
            candle_type,
            ha: HeikenAshi::new(smooth),
        }
    }

    /// Transform một bar theo `candle_type`.
    ///
    /// - `Raw`: trả về clone của bar gốc.
    /// - `HeikenAshi`: trả về bar HA ngay từ bar đầu tiên (không cần warmup).
    /// - `SmoothHeikenAshi(N)`: trả về `None` trong N-1 bar đầu (EMA warmup).
    ///
    /// Timestamp, symbol và volume được giữ nguyên từ bar gốc.
    pub fn apply(&mut self, bar: &Bar) -> Option<Bar> {
        match self.candle_type {
            CandleType::Raw => Some(bar.clone()),
            CandleType::HeikenAshi | CandleType::SmoothHeikenAshi(_) => {
                let ha = self.ha.update(bar.open, bar.high, bar.low, bar.close)?;
                Some(Bar::new(
                    bar.timestamp,
                    bar.symbol.as_str(),
                    ha.open,
                    ha.high,
                    ha.low,
                    ha.close,
                    bar.volume,
                ))
            }
        }
    }

    pub fn reset(&mut self) {
        self.ha.reset();
    }
}

impl Default for CandleTransform {
    fn default() -> Self {
        Self::new(CandleType::Raw)
    }
}
