use alm_core::bar::Bar;

/// Accumulates M1 bars into HTF bars. Emits a completed bar whenever the bucket
/// boundary changes (i.e. when the next M1 bar belongs to a new TF bucket).
pub(super) struct HtfAggregator {
    tf_ms:   i64,
    bucket:  Option<i64>, // floor timestamp of current open bucket
    last_ts: i64,
    o: f64, h: f64, l: f64, c: f64, v: f64,
}

impl HtfAggregator {
    pub(super) fn new(tf_ms: i64) -> Self {
        Self { tf_ms, bucket: None, last_ts: 0, o: 0.0, h: 0.0, l: 0.0, c: 0.0, v: 0.0 }
    }

    /// Feed one M1 bar. Returns `Some(htf_bar)` when the previous bucket closes.
    pub(super) fn update(&mut self, bar: &Bar) -> Option<Bar> {
        let new_bucket = bar.timestamp / self.tf_ms * self.tf_ms;

        if let Some(cur) = self.bucket {
            if new_bucket > cur {
                let completed = Bar::new(
                    self.last_ts, &bar.symbol,
                    self.o, self.h, self.l, self.c, self.v,
                );
                self.bucket  = Some(new_bucket);
                self.o       = bar.open;
                self.h       = bar.high;
                self.l       = bar.low;
                self.c       = bar.close;
                self.v       = bar.volume;
                self.last_ts = bar.timestamp;
                return Some(completed);
            }
            self.h        = self.h.max(bar.high);
            self.l        = self.l.min(bar.low);
            self.c        = bar.close;
            self.v       += bar.volume;
            self.last_ts  = bar.timestamp;
        } else {
            self.bucket  = Some(new_bucket);
            self.o       = bar.open;
            self.h       = bar.high;
            self.l       = bar.low;
            self.c       = bar.close;
            self.v       = bar.volume;
            self.last_ts = bar.timestamp;
        }
        None
    }

    /// Return the current forming bar without advancing state.
    pub(super) fn peek(&self) -> Option<Bar> {
        self.bucket.map(|_| Bar::new(
            self.last_ts, "",
            self.o, self.h, self.l, self.c, self.v,
        ))
    }

    pub(super) fn reset(&mut self) {
        self.bucket  = None;
        self.last_ts = 0;
        self.o = 0.0; self.h = 0.0; self.l = 0.0; self.c = 0.0; self.v = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::Timeframe;

    #[test]
    fn htf_aggregator_emits_on_bucket_cross() {
        let mut agg = HtfAggregator::new(Timeframe::H1.duration_ms());
        let sym = "TEST";
        let h1_ms = 3_600_000i64;

        for i in 0..60usize {
            let ts = i as i64 * 60_000;
            let b = Bar::new(ts, sym, 100.0, 101.0, 99.0, 100.0 + i as f64 * 0.1, 1000.0);
            assert!(agg.update(&b).is_none(), "no emit within same bucket");
        }
        let next = Bar::new(h1_ms, sym, 106.0, 107.0, 105.0, 106.0, 1000.0);
        let completed = agg.update(&next).expect("should emit on bucket cross");
        assert_eq!(completed.open, 100.0, "HTF bar open = first M1 open");
        assert!((completed.close - 105.9).abs() < 0.01, "HTF bar close = last M1 close");
    }
}
