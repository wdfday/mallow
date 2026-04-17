use alm_core::{Bar, Timeframe};

/// Iterator-like data feed — yields bars in chronological order.
pub trait BarFeed: Send {
    fn next(&mut self) -> Option<Bar>;
    fn symbol(&self) -> &str;
    /// Total number of bars (for progress reporting)
    fn len(&self) -> usize;
    fn is_empty(&self) -> bool {
        self.len() == 0
    }
    fn reset(&mut self);
    /// Base timeframe of this feed.  Implementations should detect or store
    /// the actual bar period; the default falls back to `M1`.
    fn timeframe(&self) -> Timeframe {
        Timeframe::M1
    }
}
