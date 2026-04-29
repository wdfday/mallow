use crate::feed::BarFeed;
use alm_core::{Bar, Timeframe};

/// In-memory bar feed for batch optimization (avoids repeated Parquet IO).
pub struct BarVecFeed {
    bars: Vec<Bar>,
    cursor: usize,
    symbol: String,
    timeframe: Timeframe,
}

impl BarVecFeed {
    pub fn new(bars: Vec<Bar>, symbol: String) -> Self {
        let timeframe = Timeframe::detect(
            &bars.iter().map(|b| b.timestamp).collect::<Vec<_>>(),
        );
        Self { bars, cursor: 0, symbol, timeframe }
    }

    /// Create with an explicit timeframe (skip auto-detection).
    pub fn with_timeframe(bars: Vec<Bar>, symbol: String, timeframe: Timeframe) -> Self {
        Self { bars, cursor: 0, symbol, timeframe }
    }
}

impl BarFeed for BarVecFeed {
    fn next(&mut self) -> Option<Bar> {
        if self.cursor < self.bars.len() {
            let bar = self.bars[self.cursor].clone();
            self.cursor += 1;
            Some(bar)
        } else {
            None
        }
    }
    fn symbol(&self)    -> &str      { &self.symbol }
    fn len(&self)       -> usize     { self.bars.len() }
    fn reset(&mut self)              { self.cursor = 0; }
    fn timeframe(&self) -> Timeframe { self.timeframe }
}
