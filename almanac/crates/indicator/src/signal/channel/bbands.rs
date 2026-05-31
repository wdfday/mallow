use std::collections::VecDeque;

/// Giá trị snapshot của Bollinger Bands.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct BBandsValue {
    /// Middle band: SMA(period)
    pub middle: f64,
    /// Upper band: middle + k × stddev
    pub upper: f64,
    /// Lower band: middle − k × stddev
    pub lower: f64,
    /// Bandwidth: (upper − lower) / middle — đo mức độ "nở" của dải
    pub bandwidth: f64,
    /// %B: (price − lower) / (upper − lower)
    /// 1.0 = giá ở upper band, 0.0 = giá ở lower band, 0.5 = giá ở middle
    pub percent_b: f64,
}

/// Bollinger Bands — dải biến động dựa trên SMA ± k × độ lệch chuẩn.
///
/// Được John Bollinger phát triển và phổ biến hoá qua cuốn *Bollinger on
/// Bollinger Bands* (2001). Là một trong những indicator được dùng nhiều nhất
/// để đo volatility và xác định vùng giá bất thường.
///
/// # Công thức (tham số cổ điển: period=20, k=2.0)
/// ```text
/// Middle = SMA(close, period)
/// stddev = sqrt(Σ(closeᵢ − Middle)² / period)   ← population stddev
///
/// Upper = Middle + k × stddev
/// Lower = Middle − k × stddev
/// ```
///
/// # Ý nghĩa các thành phần
/// - **Middle**: dynamic support/resistance; xu hướng khi giá ở một phía
/// - **Upper/Lower**: 2 stddev từ mean → ~95% dữ liệu nằm trong dải (với phân phối chuẩn)
/// - **Bandwidth**: đo độ "co/nở" của dải — thấp = consolidation, cao = breakout
/// - **%B**: vị trí tương đối của giá trong dải; > 1.0 hoặc < 0.0 → vượt band
///
/// # Ứng dụng thực tế
/// - Squeeze (bandwidth thấp) → breakout sắp xảy ra
/// - Giá chạm upper band trong uptrend mạnh → KHÔNG nhất thiết là sell signal
/// - Double bottom ngoài lower band → tín hiệu reversal có thể tin cậy
/// - Divergence %B vs price → momentum yếu dần
///
/// # Warmup
/// Cần `period` bar. Dùng population stddev (chia n) để khớp với TradingView mặc định.
#[derive(Debug, Clone)]
pub struct BBands {
    period: usize,
    k: f64,
    buffer: VecDeque<f64>,
    sum: f64,
}

impl BBands {
    pub fn new(period: usize, k: f64) -> Self {
        assert!(period > 1, "BBands period must be > 1");
        Self {
            period,
            k,
            buffer: VecDeque::with_capacity(period + 1),
            sum: 0.0,
        }
    }

    pub fn description() -> &'static str {
        "Bollinger Bands — upper/lower bands are N standard deviations above/below a central SMA. Contraction (squeeze) precedes breakouts; expansion signals trending moves. Outputs: `.middle` (default, SMA basis), `.upper` (upper band), `.lower` (lower band), `.bandwidth` (band width), `.percent_b` (%B position)."
    }

    /// BBands(20, 2.0) — tham số John Bollinger khuyến nghị.
    pub fn standard() -> Self {
        Self::new(20, 2.0)
    }

    pub fn update(&mut self, value: f64) -> Option<BBandsValue> {
        self.buffer.push_back(value);
        self.sum += value;

        if self.buffer.len() > self.period {
            self.sum -= self.buffer.pop_front().unwrap();
        }

        if self.buffer.len() < self.period {
            return None;
        }

        let mean = self.sum / self.period as f64;
        let variance =
            self.buffer.iter().map(|x| (x - mean).powi(2)).sum::<f64>() / self.period as f64;
        let stddev = variance.sqrt();

        let upper = mean + self.k * stddev;
        let lower = mean - self.k * stddev;
        let bandwidth = if mean.abs() > f64::EPSILON {
            (upper - lower) / mean
        } else {
            0.0
        };
        let range = upper - lower;
        let percent_b = if range.abs() > f64::EPSILON {
            (value - lower) / range
        } else {
            0.5
        };

        Some(BBandsValue {
            middle: mean,
            upper,
            lower,
            bandwidth,
            percent_b,
        })
    }

    pub fn is_ready(&self) -> bool {
        self.buffer.len() == self.period
    }

    pub fn reset(&mut self) {
        self.buffer.clear();
        self.sum = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_bbands_symmetry() {
        let mut bb = BBands::new(3, 2.0);
        bb.update(10.0);
        bb.update(10.0);
        let v = bb.update(10.0).unwrap();
        // All same values → stddev = 0 → upper = lower = middle
        assert!((v.upper - 10.0).abs() < 1e-9);
        assert!((v.lower - 10.0).abs() < 1e-9);
    }
}
