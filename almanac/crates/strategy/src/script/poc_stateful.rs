use rhai::{Map, Dynamic, Engine, Scope};

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

/// Stateful extension method on Rhai Map
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_stateful_rhai_indicator() {
        let mut engine = Engine::new();
        // Register custom type and method
        engine.register_type::<MockEma>();
        engine.register_fn("ema", ema_stateful);

        let mut scope = Scope::new();
        // Create the stateful map and push it into the scope
        let incr = Map::new();
        scope.push("incr", incr);

        // Run step 1
        let res1: f64 = engine
            .eval_with_scope(&mut scope, "incr.ema(10, 100.0)")
            .unwrap();
        assert_eq!(res1, 100.0);

        // Run step 2
        let res2: f64 = engine
            .eval_with_scope(&mut scope, "incr.ema(10, 110.0)")
            .unwrap();
        
        // alpha for period 10 is 2 / (10 + 1) = 2/11 ≈ 0.181818
        // expected = 110 * (2/11) + 100 * (9/11) = 20 + 81.818 = 101.818
        assert!((res2 - 101.81818).abs() < 1e-4);
    }

    #[test]
    fn test_ema_parity_comparison() {
        let prices = vec![100.0, 105.0, 110.0, 115.0, 112.0, 108.0, 114.0];
        let period = 10;

        // 1. "Script Ind" (Traditional pre-computed loop in Rust)
        let mut rust_ema = MockEma::new(period);
        let mut rust_results = Vec::new();
        for &p in &prices {
            rust_results.push(rust_ema.update(p));
        }

        // 2. "Incr" (Incremental Stateful Map method in Rhai)
        let mut engine = Engine::new();
        engine.register_type::<MockEma>();
        engine.register_fn("ema", ema_stateful);

        let mut scope = Scope::new();
        let incr = Map::new();
        scope.push("incr", incr);

        let mut rhai_results = Vec::new();
        for &p in &prices {
            // Evaluate step-by-step
            let expr = format!("incr.ema({}, {:.4})", period, p);
            let res: f64 = engine.eval_with_scope(&mut scope, &expr).unwrap();
            rhai_results.push(res);
        }

        // Verify that both approaches yield identical results step-by-step
        assert_eq!(rust_results.len(), rhai_results.len());
        for i in 0..rust_results.len() {
            assert!((rust_results[i] - rhai_results[i]).abs() < 1e-9);
        }
    }
}
