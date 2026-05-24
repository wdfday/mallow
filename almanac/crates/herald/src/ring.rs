use std::collections::HashMap;
use std::collections::VecDeque;
use std::sync::Arc;

use alm_core::Bar;
use parking_lot::RwLock;
use tracing::warn;

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
        let symbol = bar.symbol.clone();
        let size = {
            let mut map = self.0.write();
            let deq = map.entry(symbol.clone()).or_default();
            if let Some(last) = deq.back() {
                let gap_ms = bar.timestamp - last.timestamp;
                if gap_ms > 2 * 60_000 {
                    warn!(
                        symbol = %symbol,
                        last_ts = last.timestamp,
                        bar_ts = bar.timestamp,
                        gap_ms,
                        "BarRing M1 gap detected",
                    );
                }
            }
            if deq.len() == RING_CAPACITY {
                deq.pop_front();
            }
            deq.push_back(bar);
            deq.len()
        };
        metrics::gauge!("herald_bar_ring_size", "symbol" => symbol).set(size as f64);
    }

}
