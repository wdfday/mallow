//! Default warm-set — indicators pre-registered on every live symbol so
//! that the common chart overlays and strategy ingredients are always
//! warm in the ledger without waiting for the first HTTP / bot request.
//!
//! Policy: err on the side of **small and popular**. Anything outside the
//! warm-set falls back to lazy `ensure_indicator` on first request (spec
//! dedup means popular-but-missing periods pin themselves automatically).
//!
//! Memory budget per (symbol, tf) with 1000-bar window, columnar storage:
//! ~500 KB (scalar fields @16 bytes/row dominate; MACD/Bollinger add
//! marginally).

use crate::spec::IndicatorSpec;
use serde_json::json;

/// The agreed-upon default warm-set (≈ 36 cells per (symbol, tf)):
///
/// | Indicator | Periods                                              | Count |
/// | --------- | ---------------------------------------------------- | ----- |
/// | EMA       | 5..=25 + {50, 100, 200, 252}                         | 25    |
/// | SMA       | {20, 50, 100, 200, 252}                              | 5     |
/// | RSI       | {6, 14, 21}                                          | 3     |
/// | ATR       | {14}                                                 | 1     |
/// | BBands    | period=20, mult=2.0                                  | 1     |
/// | MACD      | 12/26/9                                              | 1     |
///
/// Callers may extend / override per-symbol via config; see
/// `Ledger::apply_warm_set`.
pub fn default_warm_set() -> Vec<IndicatorSpec> {
    let mut specs = Vec::with_capacity(40);

    // EMA short-term (5..=25) — 21 periods
    for p in 5..=25u32 {
        specs.push(ema(p));
    }
    // EMA medium & long-term — 4 periods
    for p in [50u32, 100, 200, 252] {
        specs.push(ema(p));
    }

    // SMA canonical set
    for p in [20u32, 50, 100, 200, 252] {
        specs.push(sma(p));
    }

    // RSI standard trio
    for p in [6u32, 14, 21] {
        specs.push(rsi(p));
    }

    // ATR — Wilder default
    specs.push(IndicatorSpec::from_config(json!({"type":"atr","period":14}), None).unwrap());

    // Bollinger Bands — 20 / 2σ
    specs.push(IndicatorSpec::from_config(
        json!({"type":"bbands","period":20,"mult":2.0}),
        None,
    ).unwrap());

    // MACD — canonical 12/26/9
    specs.push(IndicatorSpec::from_config(
        json!({"type":"macd","fast":12,"slow":26,"signal":9}),
        None,
    ).unwrap());

    specs
}

#[inline]
fn ema(period: u32) -> IndicatorSpec {
    IndicatorSpec::from_config(json!({"type":"ema","period": period}), None)
        .expect("ema spec is always valid")
}

#[inline]
fn sma(period: u32) -> IndicatorSpec {
    IndicatorSpec::from_config(json!({"type":"sma","period": period}), None)
        .expect("sma spec is always valid")
}

#[inline]
fn rsi(period: u32) -> IndicatorSpec {
    IndicatorSpec::from_config(json!({"type":"rsi","period": period}), None)
        .expect("rsi spec is always valid")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_warm_set_has_expected_cardinality() {
        let specs = default_warm_set();
        // 25 EMA + 5 SMA + 3 RSI + 1 ATR + 1 BB + 1 MACD = 36
        assert_eq!(specs.len(), 36);
    }

    #[test]
    fn default_warm_set_is_unique() {
        let specs = default_warm_set();
        let mut keys: Vec<String> = specs.iter().map(|s| s.canonical_key()).collect();
        keys.sort();
        keys.dedup();
        assert_eq!(keys.len(), 36, "warm-set must be free of duplicate specs");
    }

    #[test]
    fn default_warm_set_respects_default_max_period() {
        // None of the default specs should exceed the 500-period cap that
        // `LedgerConfig::default` enforces.
        let specs = default_warm_set();
        for s in &specs {
            assert!(
                s.longest_period() <= 500,
                "spec {} violates default max_period",
                s.canonical_key()
            );
        }
    }
}
