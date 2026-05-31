use rhai::{Array, Dynamic, Engine};

use super::binding::MEntry;

// ── Shared types / constants ──────────────────────────────────────────────────

pub(crate) const DEFAULT_BUF_DEPTH: usize = 2;
pub(crate) const BAR_FIELDS: &[&str] = &["open", "high", "low", "close", "volume"];

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Extract a float from a `Dynamic`, MEntry-aware. Returns `None` when the
/// value is neither a float nor an `MEntry` (so callers can pick a sensible
/// default — `0.0` for sums, `±∞` for highest/lowest).
fn dyn_f(d: &Dynamic) -> Option<f64> {
    d.as_float()
        .ok()
        .or_else(|| d.read_lock::<MEntry>().map(|e| e.primary_value()))
}

/// Extract a float from a Dynamic. For `MEntry` uses the semantic primary field
/// (e.g. `"macd"` for macd, `"middle"` for bbands) so `rising(macd)` tracks
/// the MACD line and `rising(bbands)` tracks the middle band.
fn get_f(v: Option<&Dynamic>) -> f64 {
    v.and_then(dyn_f).unwrap_or(0.0)
}

/// Extract a named field from each element of an indicator history array → `Array<f64>`.
fn extract_field(arr: &Array, name: &str) -> Array {
    arr.iter().map(|d| {
        let v = d.read_lock::<MEntry>()
            .map(|e| e.field(name))
            .or_else(|| {
                d.read_lock::<rhai::Map>()
                    .and_then(|m| m.get(name).and_then(|v| v.as_float().ok()))
            });
        Dynamic::from_float(v.unwrap_or(0.0))
    }).collect()
}

/// Population mean + standard deviation over the first `n` elements of `arr`
/// (newest-first). Returns `(mean, stdev, count)`; `stdev = 0` when `count < 2`.
/// Non-numeric / missing slots are skipped.
fn mean_std(arr: &Array, n: usize) -> (f64, f64, usize) {
    let vals: Vec<f64> = arr.iter().take(n).filter_map(dyn_f).collect();
    let len = vals.len();
    if len == 0 {
        return (0.0, 0.0, 0);
    }
    let mean = vals.iter().sum::<f64>() / len as f64;
    if len < 2 {
        return (mean, 0.0, len);
    }
    let var = vals.iter().map(|v| (v - mean) * (v - mean)).sum::<f64>() / len as f64;
    (mean, var.sqrt(), len)
}

// All field names emitted by multi-output indicators.
const MULTI_FIELDS: &[&str] = &[
    // macd / trix / ppo / kst / pmo
    "histogram", "signal", "macd", "trix", "ppo", "kst", "pmo",
    // adx / dmi
    "adx", "plus_di", "minus_di", "dx",
    // bbands / keltner / donchian
    "upper", "middle", "lower", "bandwidth", "percent_b",
    // stochastic / stoch_rsi / kdj
    "k", "d", "j",
    // supertrend / parabolic_sar / alligator / gmma / fractal
    "value", "bullish", "sar", "bearish",
    // aroon
    "up", "down",
    // vortex
    "plus_vi", "minus_vi",
    // rvi / smi / fisher
    "rvi", "smi", "fisher",
    // rwi
    "rwi_high", "rwi_low",
    // ichimoku
    "tenkan", "kijun", "senkou_a", "senkou_b", "chikou", "above_cloud", "below_cloud", "chikou_above", "chikou_below",
    // alligator
    "jaw", "teeth", "lips",
    // gmma  (.spread is the default; .bullish already listed above)
    "spread",
    "short_0", "short_1", "short_2", "short_3", "short_4", "short_5",
    "long_0", "long_1", "long_2", "long_3", "long_4", "long_5",
    // kalman
    "velocity",
    // bull_bear
    "bull", "bear",
    // elder_ray
    "bull_power", "bear_power",
    // chandelier_exit / chande_kroll
    "long_stop", "short_stop", "stop_long", "stop_short",
    // fractal
    "fractal_high", "fractal_low",
    // chop_zone
    "angle", "zone",
    // shared (ema inside bull_bear_power, atr inside chandelier_exit)
    "ema", "atr",
    // atr raw true range
    "tr",
    // lsma regression slope
    "slope",
];

// ── Script engine with built-in functions ────────────────────────────────────

pub(crate) fn build_engine() -> Engine {
    let mut engine = Engine::new();

    // ── MEntry — multi-output indicator element ──────────────────────────────
    // Allows `supertrend[0] > close` (uses "value" field) AND `supertrend[0].value`.
    engine.register_type_with_name::<MEntry>("IndicatorEntry");

    // Comparison: MEntry op f64  and  f64 op MEntry  (fall back to "value" field)
    engine.register_fn(">",  |a: MEntry, b: f64| a.primary_value() > b);
    engine.register_fn(">",  |a: f64, b: MEntry| a > b.primary_value());
    engine.register_fn(">=", |a: MEntry, b: f64| a.primary_value() >= b);
    engine.register_fn(">=", |a: f64, b: MEntry| a >= b.primary_value());
    engine.register_fn("<",  |a: MEntry, b: f64| a.primary_value() < b);
    engine.register_fn("<",  |a: f64, b: MEntry| a < b.primary_value());
    engine.register_fn("<=", |a: MEntry, b: f64| a.primary_value() <= b);
    engine.register_fn("<=", |a: f64, b: MEntry| a <= b.primary_value());
    engine.register_fn("==", |a: MEntry, b: f64| a.primary_value() == b);
    engine.register_fn("==", |a: f64, b: MEntry| a == b.primary_value());
    engine.register_fn("!=", |a: MEntry, b: f64| a.primary_value() != b);
    engine.register_fn("!=", |a: f64, b: MEntry| a != b.primary_value());

    // Arithmetic: MEntry op f64  and  f64 op MEntry  → f64
    engine.register_fn("-", |a: MEntry, b: f64| -> f64 { a.primary_value() - b });
    engine.register_fn("-", |a: f64, b: MEntry| -> f64 { a - b.primary_value() });
    engine.register_fn("+", |a: MEntry, b: f64| -> f64 { a.primary_value() + b });
    engine.register_fn("+", |a: f64, b: MEntry| -> f64 { a + b.primary_value() });
    engine.register_fn("*", |a: MEntry, b: f64| -> f64 { a.primary_value() * b });
    engine.register_fn("*", |a: f64, b: MEntry| -> f64 { a * b.primary_value() });
    engine.register_fn("/", |a: MEntry, b: f64| -> f64 {
        let b = b; if b == 0.0 { 0.0 } else { a.primary_value() / b }
    });
    engine.register_fn("/", |a: f64, b: MEntry| -> f64 {
        let b = b.primary_value(); if b == 0.0 { 0.0 } else { a / b }
    });

    // Comparison / arithmetic: MEntry op MEntry (both use their primary field).
    // Enables `macd[0] > macd[1]`, `atr[0] - atr[1]`, `tenkan[0] - kijun[0]`, etc.
    engine.register_fn(">",  |a: MEntry, b: MEntry| a.primary_value() >  b.primary_value());
    engine.register_fn(">=", |a: MEntry, b: MEntry| a.primary_value() >= b.primary_value());
    engine.register_fn("<",  |a: MEntry, b: MEntry| a.primary_value() <  b.primary_value());
    engine.register_fn("<=", |a: MEntry, b: MEntry| a.primary_value() <= b.primary_value());
    engine.register_fn("==", |a: MEntry, b: MEntry| a.primary_value() == b.primary_value());
    engine.register_fn("!=", |a: MEntry, b: MEntry| a.primary_value() != b.primary_value());
    engine.register_fn("-", |a: MEntry, b: MEntry| -> f64 { a.primary_value() - b.primary_value() });
    engine.register_fn("+", |a: MEntry, b: MEntry| -> f64 { a.primary_value() + b.primary_value() });
    engine.register_fn("*", |a: MEntry, b: MEntry| -> f64 { a.primary_value() * b.primary_value() });
    engine.register_fn("/", |a: MEntry, b: MEntry| -> f64 {
        let b = b.primary_value(); if b == 0.0 { 0.0 } else { a.primary_value() / b }
    });

    // Property getters: `entry[0].field_name` → f64
    // Each field name from MULTI_FIELDS is registered as a property on MEntry.
    for &field in MULTI_FIELDS {
        engine.register_get(field, move |e: &mut MEntry| -> f64 { e.field(field) });
    }

    // ── Crossover / direction ────────────────────────────────────────────────
    engine.register_fn("cross_above", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        a1 <= b1 && a0 > b0
    });
    engine.register_fn("crossover", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        a1 <= b1 && a0 > b0
    });
    engine.register_fn("cross_below", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        a1 >= b1 && a0 < b0
    });
    engine.register_fn("crossunder", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        a1 >= b1 && a0 < b0
    });
    engine.register_fn("rising",   |a: Array| -> bool { get_f(a.get(0)) > get_f(a.get(1)) });
    engine.register_fn("falling",  |a: Array| -> bool { get_f(a.get(0)) < get_f(a.get(1)) });
    // crossed — either-direction cross (cross_above OR cross_below) this bar.
    engine.register_fn("crossed", |a: Array, b: Array| -> bool {
        let a1 = get_f(a.get(1)); let a0 = get_f(a.get(0));
        let b1 = get_f(b.get(1)); let b0 = get_f(b.get(0));
        (a1 <= b1 && a0 > b0) || (a1 >= b1 && a0 < b0)
    });
    engine.register_fn("above",    |a: Array, b: Array| -> bool { get_f(a.get(0)) > get_f(b.get(0)) });
    engine.register_fn("below",    |a: Array, b: Array| -> bool { get_f(a.get(0)) < get_f(b.get(0)) });
    engine.register_fn("in_range", |v: f64, lo: f64, hi: f64| -> bool { v >= lo && v <= hi });
    // within — tolerant float equality: |a - b| <= tol. Safer than `==` on f64.
    engine.register_fn("within", |a: f64, b: f64, tol: f64| -> bool { (a - b).abs() <= tol });

    // flag — coerce a bool-semantic indicator field (encoded as 0.0/1.0) into a
    // real `bool`, so `!`, `&&`, `||`, and `if` work naturally. Indicator output
    // is uniformly f64 (see `IndicatorBox::update`); fields like `bullish`,
    // `above_cloud`, … only ever carry 0.0/1.0. Use `flag(st[0].bullish)` instead
    // of `st[0].bullish > 0.5`, and `!flag(st[0].bullish)` instead of `< 0.5`.
    engine.register_fn("flag", |v: f64| -> bool { v > 0.5 });

    // rising_n / falling_n — monotonically rising/falling for last n bars.
    engine.register_fn("rising_n", |a: Array, n: i64| -> bool {
        let n = n as usize;
        if a.len() < n + 1 { return false; }
        (0..n).all(|i| get_f(a.get(i)) > get_f(a.get(i + 1)))
    });
    engine.register_fn("falling_n", |a: Array, n: i64| -> bool {
        let n = n as usize;
        if a.len() < n + 1 { return false; }
        (0..n).all(|i| get_f(a.get(i)) < get_f(a.get(i + 1)))
    });

    // slope — (arr[0] - arr[n-1]) / (n-1). Positive = rising.
    engine.register_fn("slope", |a: Array| -> f64 {
        let n = a.len();
        if n < 2 { return 0.0; }
        (get_f(a.get(0)) - get_f(a.get(n - 1))) / (n - 1) as f64
    });

    // momentum — absolute change vs N bars ago: arr[0] - arr[n].
    engine.register_fn("momentum", |a: Array, n: i64| -> f64 {
        get_f(a.get(0)) - get_f(a.get(n as usize))
    });

    // ── Lookback ─────────────────────────────────────────────────────────────
    engine.register_fn("highest", |arr: Array, n: i64| -> f64 {
        arr.iter().take(n as usize).map(|d| dyn_f(d).unwrap_or(f64::NEG_INFINITY)).fold(f64::NEG_INFINITY, f64::max)
    });
    engine.register_fn("lowest", |arr: Array, n: i64| -> f64 {
        arr.iter().take(n as usize).map(|d| dyn_f(d).unwrap_or(f64::INFINITY)).fold(f64::INFINITY, f64::min)
    });

    // ── Array aggregation ────────────────────────────────────────────────────
    engine.register_fn("avg", |arr: Array| -> f64 {
        if arr.is_empty() { return 0.0; }
        let s: f64 = arr.iter().map(|d| dyn_f(d).unwrap_or(0.0)).sum();
        s / arr.len() as f64
    });
    engine.register_fn("sum", |arr: Array| -> f64 {
        arr.iter().map(|d| dyn_f(d).unwrap_or(0.0)).sum()
    });

    // ── Volatility / statistics ──────────────────────────────────────────────
    // stdev — population standard deviation over the whole buffer or first `n` bars.
    engine.register_fn("stdev", |arr: Array| -> f64 { mean_std(&arr, arr.len()).1 });
    engine.register_fn("stdev", |arr: Array, n: i64| -> f64 {
        mean_std(&arr, n.max(0) as usize).1
    });
    // pct_change — percentage change vs `n` bars ago: (a[0] - a[n]) / a[n].
    engine.register_fn("pct_change", |arr: Array, n: i64| -> f64 {
        let cur  = get_f(arr.get(0));
        let past = get_f(arr.get(n.max(0) as usize));
        if past == 0.0 { 0.0 } else { (cur - past) / past }
    });
    // zscore — (a[0] - mean) / stdev over the whole buffer or first `n` bars.
    // Returns 0.0 when stdev is 0 (flat series) to avoid div-by-zero.
    engine.register_fn("zscore", |arr: Array| -> f64 {
        let (mean, sd, _) = mean_std(&arr, arr.len());
        if sd == 0.0 { 0.0 } else { (get_f(arr.get(0)) - mean) / sd }
    });
    engine.register_fn("zscore", |arr: Array, n: i64| -> f64 {
        let (mean, sd, _) = mean_std(&arr, n.max(0) as usize);
        if sd == 0.0 { 0.0 } else { (get_f(arr.get(0)) - mean) / sd }
    });

    // ── Scalar math ──────────────────────────────────────────────────────────
    engine.register_fn("abs",   |v: f64| -> f64 { v.abs() });
    engine.register_fn("sqrt",  |v: f64| -> f64 { v.sqrt() });
    engine.register_fn("pow",   |v: f64, e: f64| -> f64 { v.powf(e) });
    engine.register_fn("round", |v: f64| -> f64 { v.round() });
    engine.register_fn("floor", |v: f64| -> f64 { v.floor() });
    engine.register_fn("ceil",  |v: f64| -> f64 { v.ceil() });
    engine.register_fn("min",   |a: f64, b: f64| -> f64 { a.min(b) });
    engine.register_fn("max",   |a: f64, b: f64| -> f64 { a.max(b) });
    engine.register_fn("clamp", |v: f64, lo: f64, hi: f64| -> f64 { v.clamp(lo, hi) });
    engine.register_fn("sign",  |v: f64| -> f64 {
        if v > 0.0 { 1.0 } else if v < 0.0 { -1.0 } else { 0.0 }
    });

    // ── Debug ────────────────────────────────────────────────────────────────
    engine.register_fn("log", |msg: String| {
        tracing::debug!(rhai = %msg);
    });

    // ── Plot ─────────────────────────────────────────────────────────────────
    // `plot("name", value)` is a no-op hint: declared indicators are already
    // auto-collected into `take_indicator_series()` without needing plot calls.
    engine.register_fn("plot", |_name: String, _value: f64|  {});
    engine.register_fn("plot", |_name: String, _value: bool| {});

    // ── Multi-output field extractors ────────────────────────────────────────
    // Register as BOTH a free function AND an Array property getter so that
    // `arr.field()` (method), `arr.field` (property), and `field(arr)` all work.
    // Enables: `rising(macd.histogram)`, `cross_above(adx14.adx, ema50)`,
    //          `rising_n(adx14.adx, 3)` — all without parentheses on the field accessor.
    for &field in MULTI_FIELDS {
        engine.register_fn(field, move |arr: Array| -> Array {
            extract_field(&arr, field)
        });
        // Property getter on Array: `array.field` (without parentheses).
        engine.register_get(field, move |arr: &mut Array| -> Array {
            extract_field(arr, field)
        });
    }

    engine
}

// ── Lookback scanner ─────────────────────────────────────────────────────────

/// Scan the cleaned script for lookback functions and return the maximum N
/// so `bar_buf` is sized correctly at build time.
pub(crate) fn extract_max_lookback(script: &str) -> usize {
    const SECOND_ARG_FNS: &[(&str, usize)] = &[
        ("highest(",    0),
        ("lowest(",     0),
        ("momentum(",   1),
        ("pct_change(", 1), // reads a[n] → needs n+1 bars
        ("rising_n(",   1),
        ("falling_n(",  1),
        ("stdev(",      0), // windowed form stdev(arr, n) → needs n bars
        ("zscore(",     0), // windowed form zscore(arr, n) → needs n bars
    ];

    let mut max_n = 0usize;
    for (prefix, extra) in SECOND_ARG_FNS {
        let mut search = script;
        while let Some(idx) = search.find(prefix) {
            search = &search[idx + prefix.len()..];
            if let Some(comma) = search.find(',') {
                let after = search[comma + 1..].trim_start();
                let n: usize = after
                    .chars()
                    .take_while(|c| c.is_ascii_digit())
                    .collect::<String>()
                    .parse()
                    .unwrap_or(0);
                let needed = n + extra;
                if needed > max_n { max_n = needed; }
            }
        }
    }
    max_n
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;
    use alm_indicator::IndicatorBox;
    use crate::script::lint::KNOWN_INDICATOR_TYPES;
    use crate::script::v1::{indicator_json_config, map_indicator_type, IndicatorKind};

    /// Parity guard for the user's vision: anything a Rust named-strategy reads
    /// from an indicator must be reachable from script.
    ///
    /// Every output field of every known indicator MUST be registered in
    /// `MULTI_FIELDS` (otherwise `entry[0].field` errors at runtime — and
    /// `on_bar` swallows that error, so the gap is invisible without this test).
    /// Also asserts each Multi indicator's declared primary is a real field.
    #[test]
    fn multi_fields_cover_every_indicator_output_field() {
        let registered: std::collections::HashSet<&str> =
            MULTI_FIELDS.iter().copied().collect();
        let empty = HashMap::new();

        let mut missing_fields = Vec::new();
        let mut bad_primaries  = Vec::new();

        // field_names() is period-independent, but some indicators reject
        // certain periods (e.g. coppock needs short < long; ema needs > 0) and
        // do so via panic, not Err. Try a few candidate periods under a
        // silenced panic hook and use the first that constructs.
        let candidate_periods = [11usize, 14, 20, 26, 34, 52];
        let prev_hook = std::panic::take_hook();
        std::panic::set_hook(Box::new(|_| {}));

        for &ty in KNOWN_INDICATOR_TYPES {
            let bx = candidate_periods.iter().find_map(|&p| {
                std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                    IndicatorBox::from_config(&indicator_json_config(ty, p, &empty)).ok()
                })).ok().flatten()
            });
            let bx = match bx {
                Some(b) => b,
                None => { std::panic::set_hook(prev_hook); panic!("no candidate period built `{ty}`"); }
            };
            let fields = bx.field_names();

            for &f in fields {
                if !registered.contains(f) {
                    missing_fields.push(format!("{ty}.{f}"));
                }
            }

            if let (_, IndicatorKind::Multi(primary)) = map_indicator_type(ty) {
                if !fields.contains(&primary.as_str()) {
                    bad_primaries.push(format!("{ty} → primary `{primary}` not in {fields:?}"));
                }
            }
        }

        std::panic::set_hook(prev_hook);

        assert!(missing_fields.is_empty(),
            "indicator output fields missing from MULTI_FIELDS (script can't read them): {missing_fields:?}");
        assert!(bad_primaries.is_empty(),
            "Multi indicators whose primary field is not produced: {bad_primaries:?}");
    }

    /// `flag(x)` coerces a 0.0/1.0 bool-semantic field into a real `bool`, so
    /// `!`, `&&`, `||`, and `if` all work — the ergonomic fix for the
    /// `Function not found: ! (f64)` error on `!st.bullish`.
    #[test]
    fn flag_coerces_f64_to_bool() {
        let engine = build_engine();

        // truthiness: 1.0 → true, 0.0 → false (threshold 0.5).
        assert!(engine.eval::<bool>("flag(1.0)").unwrap());
        assert!(!engine.eval::<bool>("flag(0.0)").unwrap());

        // negation now works (the original pain point).
        assert!(engine.eval::<bool>("!flag(0.0)").unwrap());
        assert!(!engine.eval::<bool>("!flag(1.0)").unwrap());

        // composes with && / || and `if`.
        assert!(engine.eval::<bool>("flag(1.0) && !flag(0.0)").unwrap());
        assert_eq!(
            engine.eval::<i64>("if flag(1.0) { 7 } else { 9 }").unwrap(),
            7
        );

        // cross-up idiom: was-not-bull && now-bull.
        assert!(engine.eval::<bool>("!flag(0.0) && flag(1.0)").unwrap());
    }

    /// Tier-1 statistics built-ins: stdev / pct_change / zscore.
    #[test]
    fn statistics_builtins() {
        let engine = build_engine();

        // stdev of [2,4,4,4,5,5,7,9] (population) = 2.0.
        let sd = engine.eval::<f64>("stdev([2.0,4.0,4.0,4.0,5.0,5.0,7.0,9.0])").unwrap();
        assert!((sd - 2.0).abs() < 1e-9, "stdev = {sd}");

        // stdev with n limits the window: first 2 of [10,0,...] → mean 5, sd 5.
        let sd_n = engine.eval::<f64>("stdev([10.0, 0.0, 99.0, 99.0], 2)").unwrap();
        assert!((sd_n - 5.0).abs() < 1e-9, "stdev(n=2) = {sd_n}");

        // flat series → stdev 0, zscore 0 (no div-by-zero).
        assert_eq!(engine.eval::<f64>("stdev([3.0,3.0,3.0])").unwrap(), 0.0);
        assert_eq!(engine.eval::<f64>("zscore([3.0,3.0,3.0])").unwrap(), 0.0);

        // pct_change: newest-first, (a[0]-a[n])/a[n]. [110,100] over 1 bar = +10%.
        let pc = engine.eval::<f64>("pct_change([110.0, 100.0], 1)").unwrap();
        assert!((pc - 0.10).abs() < 1e-9, "pct_change = {pc}");
        // div-by-zero guard.
        assert_eq!(engine.eval::<f64>("pct_change([5.0, 0.0], 1)").unwrap(), 0.0);

        // zscore: current = mean + 2σ → z = 2.
        // [9,1,1,1,1] → mean 2.6, sd 3.2; z = (9-2.6)/3.2 = 2.0.
        let z = engine.eval::<f64>("zscore([9.0,1.0,1.0,1.0,1.0])").unwrap();
        assert!((z - 2.0).abs() < 1e-9, "zscore = {z}");
    }

    /// Tier-2 convenience built-ins: sign / crossed / within.
    #[test]
    fn convenience_builtins() {
        let engine = build_engine();

        assert_eq!(engine.eval::<f64>("sign(3.5)").unwrap(), 1.0);
        assert_eq!(engine.eval::<f64>("sign(-3.5)").unwrap(), -1.0);
        assert_eq!(engine.eval::<f64>("sign(0.0)").unwrap(), 0.0);

        // within: tolerant float equality.
        assert!(engine.eval::<bool>("within(1.0, 1.05, 0.1)").unwrap());
        assert!(!engine.eval::<bool>("within(1.0, 1.5, 0.1)").unwrap());

        // crossed: either direction. [1,-1] vs [0,0]: was below(−1<0), now above(1>0) → cross up.
        assert!(engine.eval::<bool>("crossed([1.0, -1.0], [0.0, 0.0])").unwrap());
        // [-1,1] vs [0,0]: was above, now below → cross down (still crossed).
        assert!(engine.eval::<bool>("crossed([-1.0, 1.0], [0.0, 0.0])").unwrap());
        // no cross: both bars above.
        assert!(!engine.eval::<bool>("crossed([2.0, 3.0], [0.0, 0.0])").unwrap());
    }

    /// Windowed statistics functions must extend the bar buffer to fit `n`,
    /// otherwise `stdev(close, 50)` would silently see a too-short slice.
    #[test]
    fn lookback_scanner_covers_windowed_stats() {
        assert_eq!(extract_max_lookback("x = stdev(close, 50);"), 50);
        assert_eq!(extract_max_lookback("z = zscore(close, 30);"), 30);
        // pct_change reads a[n] → needs n+1 bars.
        assert_eq!(extract_max_lookback("p = pct_change(close, 9);"), 10);
        // whole-buffer form (no comma) contributes nothing.
        assert_eq!(extract_max_lookback("s = stdev(close);"), 0);
    }
}
