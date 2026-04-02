use crate::feed::BarFeed;
use alm_core::Bar;

/// In-memory bar feed for batch optimization (avoids repeated Parquet IO).
pub struct InMemoryFeed {
    bars: Vec<Bar>,
    cursor: usize,
    symbol: String,
}

impl InMemoryFeed {
    pub fn new(bars: Vec<Bar>, symbol: String) -> Self {
        Self {
            bars,
            cursor: 0,
            symbol,
        }
    }
}

impl BarFeed for InMemoryFeed {
    fn next(&mut self) -> Option<Bar> {
        if self.cursor < self.bars.len() {
            let bar = self.bars[self.cursor].clone();
            self.cursor += 1;
            Some(bar)
        } else {
            None
        }
    }
    fn symbol(&self) -> &str {
        &self.symbol
    }
    fn len(&self) -> usize {
        self.bars.len()
    }
    fn reset(&mut self) {
        self.cursor = 0;
    }
}
