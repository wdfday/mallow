use std::collections::HashMap;
use std::collections::VecDeque;
use std::sync::Arc;

use alm_core::Bar;
use parking_lot::RwLock;

/// Maximum M1 bars kept per symbol (24 hours).
const RING_CAPACITY: usize = 24 * 60;

/// Thread-safe 24h rolling bar buffer.
/// Each symbol keeps up to `RING_CAPACITY` most-recent M1 bars.
#[derive(Default, Clone)]
pub struct BarRing(Arc<RwLock<HashMap<String, VecDeque<Bar>>>>);

impl BarRing {
    pub fn new() -> Self {
        Self::default()
    }

    /// Push a bar. Drops the oldest entry when at capacity.
    pub fn push(&self, bar: Bar) {
        let mut map = self.0.write();
        let deq = map.entry(bar.symbol.clone()).or_default();
        if deq.len() == RING_CAPACITY {
            deq.pop_front();
        }
        deq.push_back(bar);
    }

    /// Return the last `n` bars for `symbol` (oldest first).
    /// Returns fewer than `n` if not enough history is available.
    pub fn last_n(&self, symbol: &str, n: usize) -> Vec<Bar> {
        let map = self.0.read();
        let Some(deq) = map.get(symbol) else {
            return vec![];
        };
        let skip = deq.len().saturating_sub(n);
        deq.iter().skip(skip).cloned().collect()
    }

    /// Symbols currently tracked.
    pub fn symbols(&self) -> Vec<String> {
        self.0.read().keys().cloned().collect()
    }

    /// Bar count for `symbol`.
    pub fn len(&self, symbol: &str) -> usize {
        self.0.read().get(symbol).map_or(0, |d| d.len())
    }
}
