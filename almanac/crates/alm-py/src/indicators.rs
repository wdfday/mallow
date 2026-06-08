use pyo3::prelude::*;
use pyo3::types::PyDict;
use pyo3::exceptions::{PyRuntimeError, PyValueError};
use serde_json::{json, Value};
use std::collections::HashMap;
use alm_core::Bar;
use alm_indicator::IndicatorBox;

// ── Generic Runner Helpers ───────────────────────────────────────────────────

fn run_single_indicator_helper(
    cfg: &Value,
    open: &[f64],
    high: &[f64],
    low: &[f64],
    close: &[f64],
    volume: &[f64],
) -> anyhow::Result<HashMap<String, Vec<Option<f64>>>> {
    let mut ind = IndicatorBox::from_config(cfg)?;
    let n = close.len();

    let mut raw_updates = Vec::with_capacity(n);
    for i in 0..n {
        let bar = Bar::new(
            i as i64 * 60_000,
            "TEST",
            open.get(i).copied().unwrap_or(0.0),
            high.get(i).copied().unwrap_or(0.0),
            low.get(i).copied().unwrap_or(0.0),
            close.get(i).copied().unwrap_or(0.0),
            volume.get(i).copied().unwrap_or(0.0),
        );
        raw_updates.push(ind.update(&bar));
    }

    let fields: Vec<String> = raw_updates.iter()
        .find_map(|r| r.as_ref())
        .map(|m| {
            let mut ks: Vec<String> = m.keys().cloned().collect();
            ks.sort();
            ks
        })
        .unwrap_or_else(|| vec!["value".to_string()]);

    let mut out = HashMap::new();
    for field in &fields {
        let mut vals = Vec::with_capacity(n);
        for row in &raw_updates {
            match row.as_ref().and_then(|m| m.get(field)) {
                Some(&v) => vals.push(Some(v)),
                None => vals.push(None),
            }
        }
        out.insert(field.clone(), vals);
    }

    Ok(out)
}

fn run_indicator_to_dict(
    py: Python<'_>,
    cfg: &Value,
    open: &[f64],
    high: &[f64],
    low: &[f64],
    close: &[f64],
    volume: &[f64],
) -> PyResult<PyObject> {
    let res = run_single_indicator_helper(cfg, open, high, low, close, volume)
        .map_err(|e| PyRuntimeError::new_err(format!("indicator runner error: {e}")))?;
    let out = PyDict::new_bound(py);
    for (k, v) in res {
        out.set_item(k, v)?;
    }
    Ok(out.into())
}

fn run_indicator_to_list(
    cfg: &Value,
    open: &[f64],
    high: &[f64],
    low: &[f64],
    close: &[f64],
    volume: &[f64],
) -> PyResult<Vec<Option<f64>>> {
    let res = run_single_indicator_helper(cfg, open, high, low, close, volume)
        .map_err(|e| PyRuntimeError::new_err(format!("indicator runner error: {e}")))?;
    res.get("value")
        .cloned()
        .ok_or_else(|| PyValueError::new_err("expected single-value indicator output"))
}

fn run_indicator_to_list_field(
    cfg: &Value,
    open: &[f64],
    high: &[f64],
    low: &[f64],
    close: &[f64],
    volume: &[f64],
    field: &str,
) -> PyResult<Vec<Option<f64>>> {
    let res = run_single_indicator_helper(cfg, open, high, low, close, volume)
        .map_err(|e| PyRuntimeError::new_err(format!("indicator runner error: {e}")))?;
    res.get(field)
        .cloned()
        .ok_or_else(|| PyValueError::new_err(format!("expected field '{field}' in indicator output")))
}

fn run_indicator_to_tup2(
    cfg: &Value,
    open: &[f64],
    high: &[f64],
    low: &[f64],
    close: &[f64],
    volume: &[f64],
    f1: &str,
    f2: &str,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let res = run_single_indicator_helper(cfg, open, high, low, close, volume)
        .map_err(|e| PyRuntimeError::new_err(format!("indicator runner error: {e}")))?;
    let v1 = res.get(f1).cloned().unwrap_or_default();
    let v2 = res.get(f2).cloned().unwrap_or_default();
    Ok((v1, v2))
}

fn run_indicator_to_tup3(
    cfg: &Value,
    open: &[f64],
    high: &[f64],
    low: &[f64],
    close: &[f64],
    volume: &[f64],
    f1: &str,
    f2: &str,
    f3: &str,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>, Vec<Option<f64>>)> {
    let res = run_single_indicator_helper(cfg, open, high, low, close, volume)
        .map_err(|e| PyRuntimeError::new_err(format!("indicator runner error: {e}")))?;
    let v1 = res.get(f1).cloned().unwrap_or_default();
    let v2 = res.get(f2).cloned().unwrap_or_default();
    let v3 = res.get(f3).cloned().unwrap_or_default();
    Ok((v1, v2, v3))
}

fn run_indicator_to_tup4(
    cfg: &Value,
    open: &[f64],
    high: &[f64],
    low: &[f64],
    close: &[f64],
    volume: &[f64],
    f1: &str,
    f2: &str,
    f3: &str,
    f4: &str,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>, Vec<Option<f64>>, Vec<Option<f64>>)> {
    let res = run_single_indicator_helper(cfg, open, high, low, close, volume)
        .map_err(|e| PyRuntimeError::new_err(format!("indicator runner error: {e}")))?;
    let v1 = res.get(f1).cloned().unwrap_or_default();
    let v2 = res.get(f2).cloned().unwrap_or_default();
    let v3 = res.get(f3).cloned().unwrap_or_default();
    let v4 = res.get(f4).cloned().unwrap_or_default();
    Ok((v1, v2, v3, v4))
}

fn run_indicator_to_tup5(
    cfg: &Value,
    open: &[f64],
    high: &[f64],
    low: &[f64],
    close: &[f64],
    volume: &[f64],
    f1: &str,
    f2: &str,
    f3: &str,
    f4: &str,
    f5: &str,
) -> PyResult<(
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
)> {
    let res = run_single_indicator_helper(cfg, open, high, low, close, volume)
        .map_err(|e| PyRuntimeError::new_err(format!("indicator runner error: {e}")))?;
    let v1 = res.get(f1).cloned().unwrap_or_default();
    let v2 = res.get(f2).cloned().unwrap_or_default();
    let v3 = res.get(f3).cloned().unwrap_or_default();
    let v4 = res.get(f4).cloned().unwrap_or_default();
    let v5 = res.get(f5).cloned().unwrap_or_default();
    Ok((v1, v2, v3, v4, v5))
}

// ── Macro Declarations ───────────────────────────────────────────────────────

macro_rules! define_scalar_close {
    ($name:ident, $type:expr, ($($sig:tt)*)) => {
        #[pyfunction]
        #[pyo3(signature = ($($sig)*))]
        pub fn $name(close: Vec<f64>, period: usize) -> PyResult<Vec<Option<f64>>> {
            let cfg = json!({ "type": $type, "period": period });
            run_indicator_to_list(&cfg, &close, &close, &close, &close, &close)
        }
    };
}

macro_rules! define_scalar_hlc {
    ($name:ident, $type:expr, ($($sig:tt)*)) => {
        #[pyfunction]
        #[pyo3(signature = ($($sig)*))]
        pub fn $name(high: Vec<f64>, low: Vec<f64>, close: Vec<f64>, period: usize) -> PyResult<Vec<Option<f64>>> {
            let cfg = json!({ "type": $type, "period": period });
            run_indicator_to_list(&cfg, &close, &high, &low, &close, &close)
        }
    };
}

// ── Trend / Moving Averages ──────────────────────────────────────────────────

define_scalar_close!(sma, "sma", (close, period=20));
define_scalar_close!(ema, "ema", (close, period=20));
define_scalar_close!(wma, "wma", (close, period=20));
define_scalar_close!(hma, "hma", (close, period=20));
define_scalar_close!(dema, "dema", (close, period=20));
define_scalar_close!(tema, "tema", (close, period=20));
define_scalar_close!(smma, "smma", (close, period=20));
define_scalar_close!(mcginley, "mcginley", (close, period=14));
define_scalar_close!(lsma_scalar, "lsma", (close, period=25));

#[pyfunction]
#[pyo3(signature = (close, period=9, offset=0.85, sigma=6.0))]
pub fn alma(close: Vec<f64>, period: usize, offset: f64, sigma: f64) -> PyResult<Vec<Option<f64>>> {
    let cfg = json!({ "type": "alma", "period": period, "offset": offset, "sigma": sigma });
    run_indicator_to_list(&cfg, &close, &close, &close, &close, &close)
}

#[pyfunction]
#[pyo3(signature = (close, period=25))]
pub fn lsma(close: Vec<f64>, period: usize) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "lsma", "period": period });
    run_indicator_to_tup2(&cfg, &close, &close, &close, &close, &close, "value", "slope")
}

#[pyfunction]
#[pyo3(signature = (close, volume, period=20))]
pub fn vwma(close: Vec<f64>, volume: Vec<f64>, period: usize) -> PyResult<Vec<Option<f64>>> {
    let cfg = json!({ "type": "vwma", "period": period });
    run_indicator_to_list(&cfg, &close, &close, &close, &close, &volume)
}

#[pyfunction]
#[pyo3(signature = (close, er_period=10, fast=2, slow=30))]
pub fn kama(close: Vec<f64>, er_period: usize, fast: usize, slow: usize) -> PyResult<Vec<Option<f64>>> {
    let cfg = json!({ "type": "kama", "er_period": er_period, "fast": fast, "slow": slow });
    run_indicator_to_list(&cfg, &close, &close, &close, &close, &close)
}

#[pyfunction]
#[pyo3(signature = (close, fast=12, slow=26, signal=9))]
pub fn macd(
    close: Vec<f64>,
    fast: usize,
    slow: usize,
    signal: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "macd", "fast": fast, "slow": slow, "signal": signal });
    run_indicator_to_tup3(
        &cfg,
        &close,
        &close,
        &close,
        &close,
        &close,
        "macd",
        "signal",
        "histogram",
    )
}

#[pyfunction]
#[pyo3(signature = (close, period=18, signal=9))]
pub fn trix(
    close: Vec<f64>,
    period: usize,
    signal: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "trix", "period": period, "signal": signal });
    run_indicator_to_tup3(
        &cfg,
        &close,
        &close,
        &close,
        &close,
        &close,
        "trix",
        "signal",
        "histogram",
    )
}

#[pyfunction]
#[pyo3(signature = (high, low, close, period=14))]
pub fn adx(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "adx", "period": period });
    run_indicator_to_tup3(&cfg, &close, &high, &low, &close, &close, "adx", "plus_di", "minus_di")
}

#[pyfunction]
#[pyo3(signature = (high, low, close, period=14))]
pub fn dmi(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "dmi", "period": period });
    run_indicator_to_tup2(&cfg, &close, &high, &low, &close, &close, "plus_di", "minus_di")
}

#[pyfunction]
#[pyo3(signature = (high, low, period=25))]
pub fn aroon(high: Vec<f64>, low: Vec<f64>, period: usize) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "aroon", "period": period });
    run_indicator_to_tup2(&cfg, &high, &high, &low, &high, &high, "up", "down")
}

#[pyfunction]
#[pyo3(signature = (high, low, period=25))]
pub fn aroon_osc(high: Vec<f64>, low: Vec<f64>, period: usize) -> PyResult<Vec<Option<f64>>> {
    let cfg = json!({ "type": "aroon_osc", "period": period });
    run_indicator_to_list(&cfg, &high, &high, &low, &high, &high)
}

#[pyfunction]
#[pyo3(signature = (high, low, period=14))]
pub fn vortex(high: Vec<f64>, low: Vec<f64>, period: usize) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "vortex", "period": period });
    run_indicator_to_tup2(&cfg, &high, &high, &low, &high, &high, "plus", "minus")
}

#[pyfunction]
#[pyo3(signature = (high, low, close, jaw=13, teeth=8, lips=5))]
pub fn alligator(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    jaw: usize,
    teeth: usize,
    lips: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "alligator", "jaw": jaw, "teeth": teeth, "lips": lips });
    run_indicator_to_tup3(&cfg, &close, &high, &low, &close, &close, "jaw", "teeth", "lips")
}

#[pyfunction]
#[pyo3(signature = (close, short=None, long=None))]
pub fn gmma(
    py: Python<'_>,
    close: Vec<f64>,
    short: Option<Vec<usize>>,
    long: Option<Vec<usize>>,
) -> PyResult<PyObject> {
    let mut map = serde_json::Map::new();
    map.insert("type".to_string(), json!("gmma"));
    if let Some(s) = short { map.insert("short".to_string(), json!(s)); }
    if let Some(l) = long { map.insert("long".to_string(), json!(l)); }
    let cfg = Value::Object(map);
    run_indicator_to_dict(py, &cfg, &close, &close, &close, &close, &close)
}

#[pyfunction]
#[pyo3(signature = (high, low, close, period=9, k_period=3, d_period=3))]
pub fn kdj(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
    k_period: usize,
    d_period: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "kdj", "period": period, "k_period": k_period, "d_period": d_period });
    run_indicator_to_tup3(&cfg, &close, &high, &low, &close, &close, "k", "d", "j")
}

#[pyfunction]
#[pyo3(signature = (close, q_pos=0.001, q_vel=0.001, r=1.0))]
pub fn kalman(close: Vec<f64>, q_pos: f64, q_vel: f64, r: f64) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "kalman", "q_pos": q_pos, "q_vel": q_vel, "r": r });
    run_indicator_to_tup2(&cfg, &close, &close, &close, &close, &close, "pos", "vel")
}

// ── Momentum / Oscillators ───────────────────────────────────────────────────

define_scalar_close!(rsi, "rsi", (close, period=14));
define_scalar_close!(roc, "roc", (close, period=10));
define_scalar_close!(mom, "mom", (close, period=10));
define_scalar_close!(cmo, "cmo", (close, period=14));
define_scalar_close!(dpo, "dpo", (close, period=20));
define_scalar_close!(rci, "rci", (close, period=9));
define_scalar_hlc!(cci, "cci", (high, low, close, period=20));
define_scalar_hlc!(williams_r, "williams_r", (high, low, close, period=14));

#[pyfunction]
#[pyo3(signature = (high, low, close, volume, period=14))]
pub fn mfi(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    volume: Vec<f64>,
    period: usize,
) -> PyResult<Vec<Option<f64>>> {
    let cfg = json!({ "type": "mfi", "period": period });
    run_indicator_to_list(&cfg, &close, &high, &low, &close, &volume)
}

#[pyfunction]
pub fn bop(open: Vec<f64>, high: Vec<f64>, low: Vec<f64>, close: Vec<f64>) -> PyResult<Vec<Option<f64>>> {
    let cfg = json!({ "type": "bop" });
    run_indicator_to_list(&cfg, &open, &high, &low, &close, &open)
}

#[pyfunction]
#[pyo3(signature = (high, low, close, k_period=14, smooth_k=1, d_period=3))]
pub fn stochastic(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    k_period: usize,
    smooth_k: usize,
    d_period: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "stochastic", "k_period": k_period, "smooth_k": smooth_k, "d_period": d_period });
    run_indicator_to_tup2(&cfg, &close, &high, &low, &close, &close, "k", "d")
}

#[pyfunction]
#[pyo3(signature = (close, rsi_period=14, smooth_d=3))]
pub fn stoch_rsi(close: Vec<f64>, rsi_period: usize, smooth_d: usize) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "stoch_rsi", "rsi_period": rsi_period, "smooth_d": smooth_d });
    run_indicator_to_tup2(&cfg, &close, &close, &close, &close, &close, "k", "d")
}

#[pyfunction]
#[pyo3(signature = (close, first=25, second=13))]
pub fn tsi(
    close: Vec<f64>,
    first: usize,
    second: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "tsi", "first": first, "second": second });
    run_indicator_to_tup3(&cfg, &close, &close, &close, &close, &close, "tsi", "signal", "histogram")
}

#[pyfunction]
#[pyo3(signature = (high, low, close, period=13))]
pub fn bull_bear(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "bull_bear", "period": period });
    run_indicator_to_tup2(&cfg, &close, &high, &low, &close, &close, "bull", "bear")
}

#[pyfunction]
#[pyo3(signature = (high, low, close, period=9))]
pub fn fisher(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "fisher", "period": period });
    run_indicator_to_tup2(&cfg, &close, &high, &low, &close, &close, "fisher", "signal")
}

#[pyfunction]
#[pyo3(signature = (close, roc_periods=None, roc_smas=None, signal=None))]
pub fn kst(
    py: Python<'_>,
    close: Vec<f64>,
    roc_periods: Option<Vec<usize>>,
    roc_smas: Option<Vec<usize>>,
    signal: Option<usize>,
) -> PyResult<PyObject> {
    let mut map = serde_json::Map::new();
    map.insert("type".to_string(), json!("kst"));
    if let Some(p) = roc_periods { map.insert("roc_periods".to_string(), json!(p)); }
    if let Some(s) = roc_smas { map.insert("roc_smas".to_string(), json!(s)); }
    if let Some(sig) = signal { map.insert("signal".to_string(), json!(sig)); }
    let cfg = Value::Object(map);
    run_indicator_to_dict(py, &cfg, &close, &close, &close, &close, &close)
}

// ── Volume ───────────────────────────────────────────────────────────────────

#[pyfunction]
pub fn vwap(high: Vec<f64>, low: Vec<f64>, close: Vec<f64>, volume: Vec<f64>) -> PyResult<Vec<Option<f64>>> {
    let cfg = json!({ "type": "vwap" });
    run_indicator_to_list(&cfg, &close, &high, &low, &close, &volume)
}

#[pyfunction]
pub fn obv(close: Vec<f64>, volume: Vec<f64>) -> PyResult<Vec<Option<f64>>> {
    let cfg = json!({ "type": "obv" });
    run_indicator_to_list(&cfg, &close, &close, &close, &close, &volume)
}

#[pyfunction]
#[pyo3(signature = (high, low, close, volume, period=20))]
pub fn cmf(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    volume: Vec<f64>,
    period: usize,
) -> PyResult<Vec<Option<f64>>> {
    let cfg = json!({ "type": "cmf", "period": period });
    run_indicator_to_list(&cfg, &close, &high, &low, &close, &volume)
}

// ── Channels ─────────────────────────────────────────────────────────────────

#[pyfunction]
#[pyo3(signature = (close, period=20, k=2.0))]
pub fn bbands(
    close: Vec<f64>,
    period: usize,
    k: f64,
) -> PyResult<(
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
)> {
    let cfg = json!({ "type": "bbands", "period": period, "k": k });
    run_indicator_to_tup5(
        &cfg,
        &close,
        &close,
        &close,
        &close,
        &close,
        "middle",
        "upper",
        "lower",
        "percent_b",
        "bandwidth",
    )
}

#[pyfunction]
#[pyo3(signature = (close, period=20, k=2.0))]
pub fn bollinger_bands(
    close: Vec<f64>,
    period: usize,
    k: f64,
) -> PyResult<(
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
)> {
    bbands(close, period, k)
}

#[pyfunction]
#[pyo3(signature = (high, low, close, period=20, multiplier=2.0, atr_period=10))]
pub fn keltner(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
    multiplier: f64,
    atr_period: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({
        "type": "keltner",
        "period": period,
        "multiplier": multiplier,
        "atr_period": atr_period
    });
    run_indicator_to_tup3(&cfg, &close, &high, &low, &close, &close, "middle", "upper", "lower")
}

#[pyfunction]
#[pyo3(signature = (high, low, close, period=20))]
pub fn donchian(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "donchian", "period": period });
    run_indicator_to_tup3(&cfg, &close, &high, &low, &close, &close, "upper", "lower", "middle")
}

// ── Pattern / Regime / Risk ──────────────────────────────────────────────────

#[pyfunction]
#[pyo3(signature = (high, low, close, period=14))]
pub fn rwi(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "rwi", "period": period });
    run_indicator_to_tup2(&cfg, &close, &high, &low, &close, &close, "rwi_high", "rwi_low")
}

#[pyfunction]
#[pyo3(signature = (high, low, close, period=13))]
pub fn elder_ray(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "elder_ray", "period": period });
    run_indicator_to_tup2(&cfg, &close, &high, &low, &close, &close, "bull", "bear")
}

#[pyfunction]
#[pyo3(signature = (high, low, close, tenkan=9, kijun=26, senkou=52, chikou=26))]
pub fn ichimoku(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    tenkan: usize,
    kijun: usize,
    senkou: usize,
    chikou: usize,
) -> PyResult<(
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
)> {
    let cfg = json!({
        "type": "ichimoku",
        "tenkan": tenkan,
        "kijun": kijun,
        "senkou": senkou,
        "chikou": chikou
    });
    run_indicator_to_tup5(
        &cfg,
        &close,
        &high,
        &low,
        &close,
        &close,
        "tenkan",
        "kijun",
        "senkou_a",
        "senkou_b",
        "chikou",
    )
}

#[pyfunction]
pub fn heiken_ashi(
    open: Vec<f64>,
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
) -> PyResult<(
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
)> {
    let cfg = json!({ "type": "heiken_ashi" });
    run_indicator_to_tup4(&cfg, &open, &high, &low, &close, &open, "open", "high", "low", "close")
}

define_scalar_hlc!(chop, "chop", (high, low, close, period=14));
define_scalar_hlc!(volatility_ratio, "volatility_ratio", (high, low, close, period=14));

#[pyfunction]
#[pyo3(signature = (high, low, close, period=14))]
pub fn atr(high: Vec<f64>, low: Vec<f64>, close: Vec<f64>, period: usize) -> PyResult<Vec<Option<f64>>> {
    let cfg = json!({ "type": "atr", "period": period });
    run_indicator_to_list_field(&cfg, &close, &high, &low, &close, &close, "atr")
}

#[pyfunction]
#[pyo3(signature = (high, low, close, period=10, multiplier=3.0))]
pub fn supertrend(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
    multiplier: f64,
) -> PyResult<(
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
    Vec<Option<f64>>,
)> {
    let cfg = json!({ "type": "supertrend", "period": period, "multiplier": multiplier });
    run_indicator_to_tup4(&cfg, &close, &high, &low, &close, &close, "trend", "direction", "long", "short")
}

#[pyfunction]
#[pyo3(signature = (high, low, close, period=22, k=3.0))]
pub fn chandelier(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
    k: f64,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "chandelier", "period": period, "k": k });
    run_indicator_to_tup2(&cfg, &close, &high, &low, &close, &close, "long", "short")
}

#[pyfunction]
#[pyo3(signature = (high, low, close, period=10, max_min_period=20, k=2.0))]
pub fn chande_kroll(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    period: usize,
    max_min_period: usize,
    k: f64,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({
        "type": "chande_kroll",
        "period": period,
        "max_min_period": max_min_period,
        "k": k
    });
    run_indicator_to_tup2(&cfg, &close, &high, &low, &close, &close, "long", "short")
}

#[pyfunction]
#[pyo3(signature = (high, low, close, step=0.02, max_step=0.2))]
pub fn psar(
    high: Vec<f64>,
    low: Vec<f64>,
    close: Vec<f64>,
    step: f64,
    max_step: f64,
) -> PyResult<(Vec<Option<f64>>, Vec<Option<f64>>)> {
    let cfg = json!({ "type": "psar", "step": step, "max_step": max_step });
    run_indicator_to_tup2(&cfg, &close, &high, &low, &close, &close, "sar", "trend")
}

// ── Module registration ───────────────────────────────────────────────────────

pub fn register_submodule(m: &Bound<PyModule>) -> PyResult<()> {
    let py = m.py();
    let sub = PyModule::new_bound(py, "indicators")?;

    // Trend
    sub.add_function(wrap_pyfunction!(sma, &sub)?)?;
    sub.add_function(wrap_pyfunction!(ema, &sub)?)?;
    sub.add_function(wrap_pyfunction!(wma, &sub)?)?;
    sub.add_function(wrap_pyfunction!(hma, &sub)?)?;
    sub.add_function(wrap_pyfunction!(dema, &sub)?)?;
    sub.add_function(wrap_pyfunction!(tema, &sub)?)?;
    sub.add_function(wrap_pyfunction!(smma, &sub)?)?;
    sub.add_function(wrap_pyfunction!(alma, &sub)?)?;
    sub.add_function(wrap_pyfunction!(mcginley, &sub)?)?;
    sub.add_function(wrap_pyfunction!(lsma, &sub)?)?;
    sub.add_function(wrap_pyfunction!(lsma_scalar, &sub)?)?;
    sub.add_function(wrap_pyfunction!(vwma, &sub)?)?;
    sub.add_function(wrap_pyfunction!(kama, &sub)?)?;
    sub.add_function(wrap_pyfunction!(macd, &sub)?)?;
    sub.add_function(wrap_pyfunction!(trix, &sub)?)?;
    sub.add_function(wrap_pyfunction!(adx, &sub)?)?;
    sub.add_function(wrap_pyfunction!(dmi, &sub)?)?;
    sub.add_function(wrap_pyfunction!(aroon, &sub)?)?;
    sub.add_function(wrap_pyfunction!(aroon_osc, &sub)?)?;
    sub.add_function(wrap_pyfunction!(vortex, &sub)?)?;
    sub.add_function(wrap_pyfunction!(alligator, &sub)?)?;
    sub.add_function(wrap_pyfunction!(gmma, &sub)?)?;
    sub.add_function(wrap_pyfunction!(kdj, &sub)?)?;
    sub.add_function(wrap_pyfunction!(kalman, &sub)?)?;

    // Momentum
    sub.add_function(wrap_pyfunction!(rsi, &sub)?)?;
    sub.add_function(wrap_pyfunction!(cci, &sub)?)?;
    sub.add_function(wrap_pyfunction!(roc, &sub)?)?;
    sub.add_function(wrap_pyfunction!(mom, &sub)?)?;
    sub.add_function(wrap_pyfunction!(cmo, &sub)?)?;
    sub.add_function(wrap_pyfunction!(dpo, &sub)?)?;
    sub.add_function(wrap_pyfunction!(mfi, &sub)?)?;
    sub.add_function(wrap_pyfunction!(bop, &sub)?)?;
    sub.add_function(wrap_pyfunction!(williams_r, &sub)?)?;
    sub.add_function(wrap_pyfunction!(stochastic, &sub)?)?;
    sub.add_function(wrap_pyfunction!(stoch_rsi, &sub)?)?;
    sub.add_function(wrap_pyfunction!(tsi, &sub)?)?;
    sub.add_function(wrap_pyfunction!(rci, &sub)?)?;
    sub.add_function(wrap_pyfunction!(bull_bear, &sub)?)?;
    sub.add_function(wrap_pyfunction!(fisher, &sub)?)?;
    sub.add_function(wrap_pyfunction!(kst, &sub)?)?;

    // Volume
    sub.add_function(wrap_pyfunction!(vwap, &sub)?)?;
    sub.add_function(wrap_pyfunction!(obv, &sub)?)?;
    sub.add_function(wrap_pyfunction!(cmf, &sub)?)?;

    // Channels
    sub.add_function(wrap_pyfunction!(bbands, &sub)?)?;
    sub.add_function(wrap_pyfunction!(bollinger_bands, &sub)?)?;
    sub.add_function(wrap_pyfunction!(keltner, &sub)?)?;
    sub.add_function(wrap_pyfunction!(donchian, &sub)?)?;

    // Pattern / Regime / Risk
    sub.add_function(wrap_pyfunction!(rwi, &sub)?)?;
    sub.add_function(wrap_pyfunction!(elder_ray, &sub)?)?;
    sub.add_function(wrap_pyfunction!(ichimoku, &sub)?)?;
    sub.add_function(wrap_pyfunction!(heiken_ashi, &sub)?)?;
    sub.add_function(wrap_pyfunction!(chop, &sub)?)?;
    sub.add_function(wrap_pyfunction!(volatility_ratio, &sub)?)?;
    sub.add_function(wrap_pyfunction!(atr, &sub)?)?;
    sub.add_function(wrap_pyfunction!(supertrend, &sub)?)?;
    sub.add_function(wrap_pyfunction!(chandelier, &sub)?)?;
    sub.add_function(wrap_pyfunction!(chande_kroll, &sub)?)?;
    sub.add_function(wrap_pyfunction!(psar, &sub)?)?;

    m.add_submodule(&sub)?;
    Ok(())
}
