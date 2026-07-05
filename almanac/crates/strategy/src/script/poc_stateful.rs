use rhai::{Map, Dynamic, Engine, Scope};
use std::collections::VecDeque;
use std::time::Instant;

// ── Mock EMA (No History Buffer needed) ──────────────────────────────────────

#[derive(Clone, Debug)]
pub struct MockEma {
    val: f64,
    alpha: f64,
}

impl MockEma {
    pub fn new(period: usize) -> Self {
        Self {
            val: 0.0,
            alpha: 2.0 / (period as f64 + 1.0),
        }
    }

    pub fn update(&mut self, next: f64) -> f64 {
        if self.val == 0.0 {
            self.val = next;
        } else {
            self.val = next * self.alpha + self.val * (1.0 - self.alpha);
        }
        self.val
    }
}

// ── Mock SMA (Requires Deque History Buffer) ──────────────────────────────────

#[derive(Clone, Debug)]
pub struct MockSma {
    period: usize,
    buffer: VecDeque<f64>,
    sum: f64,
}

impl MockSma {
    pub fn new(period: usize) -> Self {
        Self {
            period,
            buffer: VecDeque::with_capacity(period),
            sum: 0.0,
        }
    }

    pub fn update(&mut self, next: f64) -> Option<f64> {
        self.buffer.push_back(next);
        self.sum += next;
        if self.buffer.len() > self.period {
            let old = self.buffer.pop_front().unwrap();
            self.sum -= old;
        }
        if self.buffer.len() == self.period {
            Some(self.sum / self.period as f64)
        } else {
            None
        }
    }
}

// ── Stateful Extension Methods on Rhai Map ────────────────────────────────────

pub fn ema_stateful(state: &mut Map, period: i64, value: f64) -> f64 {
    let key = format!("ema_{}", period);

    if !state.contains_key(key.as_str()) {
        let mut ind = MockEma::new(period as usize);
        let res = ind.update(value);
        state.insert(key.into(), Dynamic::from(ind));
        res
    } else {
        let ind_dyn = state.get_mut(key.as_str()).unwrap();
        let mut ind = ind_dyn.write_lock::<MockEma>().unwrap();
        ind.update(value)
    }
}

pub fn sma_stateful(state: &mut Map, period: i64, value: f64) -> Dynamic {
    let key = format!("sma_{}", period);

    if !state.contains_key(key.as_str()) {
        let mut ind = MockSma::new(period as usize);
        let res = ind.update(value);
        state.insert(key.into(), Dynamic::from(ind));
        match res {
            Some(v) => Dynamic::from(v),
            None => Dynamic::UNIT,
        }
    } else {
        let ind_dyn = state.get_mut(key.as_str()).unwrap();
        let mut ind = ind_dyn.write_lock::<MockSma>().unwrap();
        match ind.update(value) {
            Some(v) => Dynamic::from(v),
            None => Dynamic::UNIT,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_stateful_rhai_indicator() {
        let mut engine = Engine::new();
        engine.register_type::<MockEma>();
        engine.register_fn("ema", ema_stateful);

        let mut scope = Scope::new();
        let incr = Map::new();
        scope.push("incr", incr);

        let res1: f64 = engine
            .eval_with_scope(&mut scope, "incr.ema(10, 100.0)")
            .unwrap();
        assert_eq!(res1, 100.0);

        let res2: f64 = engine
            .eval_with_scope(&mut scope, "incr.ema(10, 110.0)")
            .unwrap();
        
        assert!((res2 - 101.81818).abs() < 1e-4);
    }

    #[test]
    fn test_ema_parity_comparison() {
        let prices = vec![100.0, 105.0, 110.0, 115.0, 112.0, 108.0, 114.0];
        let period = 10;

        let mut rust_ema = MockEma::new(period);
        let mut rust_results = Vec::new();
        for &p in &prices {
            rust_results.push(rust_ema.update(p));
        }

        let mut engine = Engine::new();
        engine.register_type::<MockEma>();
        engine.register_fn("ema", ema_stateful);

        let mut scope = Scope::new();
        let incr = Map::new();
        scope.push("incr", incr);

        let mut rhai_results = Vec::new();
        for &p in &prices {
            let expr = format!("incr.ema({}, {:.4})", period, p);
            let res: f64 = engine.eval_with_scope(&mut scope, &expr).unwrap();
            rhai_results.push(res);
        }

        assert_eq!(rust_results.len(), rhai_results.len());
        for i in 0..rust_results.len() {
            assert!((rust_results[i] - rhai_results[i]).abs() < 1e-9);
        }
    }

    #[test]
    fn test_real_data_indicators_comparison() {
        // Load real BTCUSDT M1 bars
        let bars = match crate::test_utils::load_real_bars() {
            Some(b) => b,
            None => {
                println!("testdata not found, skipping real data comparison");
                return;
            }
        };

        // We only benchmark first 2000 bars for test speed
        let prices: Vec<f64> = bars.iter().map(|b| b.close).take(2000).collect();
        let period = 20;

        // 1. Rust Native Path (EMA & SMA)
        let t_rust_start = Instant::now();
        let mut rust_ema = MockEma::new(period);
        let mut rust_sma = MockSma::new(period);
        let mut rust_ema_res = Vec::new();
        let mut rust_sma_res = Vec::new();

        for &p in &prices {
            rust_ema_res.push(rust_ema.update(p));
            rust_sma_res.push(rust_sma.update(p));
        }
        let d_rust = t_rust_start.elapsed();

        // 2. Rhai Stateful Incr Path (EMA & SMA)
        let mut engine = Engine::new();
        engine.register_type::<MockEma>();
        engine.register_type::<MockSma>();
        engine.register_fn("ema", ema_stateful);
        engine.register_fn("sma", sma_stateful);

        let mut scope = Scope::new();
        let incr = Map::new();
        scope.push("incr", incr);

        let t_rhai_start = Instant::now();
        let mut rhai_ema_res = Vec::new();
        let mut rhai_sma_res = Vec::new();

        for &p in &prices {
            let expr = format!(
                "let e = incr.ema({}, {:.4}); let s = incr.sma({}, {:.4}); [e, s]",
                period, p, period, p
            );
            let res: rhai::Array = engine.eval_with_scope(&mut scope, &expr).unwrap();
            let e: f64 = res[0].as_float().unwrap();
            let s: Option<f64> = res[1].as_float().ok();
            
            rhai_ema_res.push(e);
            rhai_sma_res.push(s);
        }
        let d_rhai = t_rhai_start.elapsed();

        println!(
            "\n[Benchmark] Processed {} bars:\n- Rust Native (EMA + SMA): {:?}\n- Rhai Stateful Incr (EMA + SMA): {:?}",
            prices.len(),
            d_rust,
            d_rhai
        );

        // Verify Parity
        assert_eq!(rust_ema_res.len(), rhai_ema_res.len());
        assert_eq!(rust_sma_res.len(), rhai_sma_res.len());

        for i in 0..prices.len() {
            assert!((rust_ema_res[i] - rhai_ema_res[i]).abs() < 1e-9);
            
            match (rust_sma_res[i], rhai_sma_res[i]) {
                (Some(r), Some(rh)) => assert!((r - rh).abs() < 1e-9),
                (None, None) => {}
                _ => panic!("SMA result mismatch at index {}", i),
            }
        }
    }
}
