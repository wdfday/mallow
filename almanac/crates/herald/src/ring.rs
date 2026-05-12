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

}
