//! `alm_py` — PyO3 Python bindings for the almanac backtesting engine.
//!
//! Build with maturin:
//! ```bash
//! cd almanac/crates/alm-py
//! maturin develop          # editable install into current venv
//! maturin build --release  # build wheel
//! ```
//!
//! Usage from Python:
//! ```python
//! import alm_py as alm
//!
//! bars = {"t": [...], "o": [...], "h": [...], "l": [...], "c": [...], "v": [...]}
//!
//! result  = alm.run_backtest("BTCUSDT", "rsi_mean_rev", {"period": 14}, bars)
//! kf      = alm.kalman([100.1, 100.5, ...], q_pos=0.001, q_vel=0.001, r=1.0)
//! mc      = alm.monte_carlo(result["equity_pnl_pct"], capital=10_000, n_iter=5000)
//! names   = alm.list_strategies()
//! ```

use std::collections::HashMap;

use pyo3::exceptions::{PyKeyError, PyRuntimeError, PyValueError};
use pyo3::prelude::*;
use pyo3::types::{PyDict, PyList};

use alm_core::{Bar, exit::{ExitRules, IntraBarMode}, order::Side, portfolio::EquityPoint};
use alm_data::BarVecFeed;
use alm_engine::Engine;
use alm_indicator::{IndicatorBox, KalmanFilter};
use alm_report::{BuyHoldBenchmark, monte_carlo as mc_run, portfolio_analyze, MonteCarloConfig};
use alm_strategy::{build_strategy, catalog::STRATEGY_KEYS, AtrSizing, FixedFractional, KellySizing};
use serde_json::{json, Map, Value};

// ── Internal helpers ──────────────────────────────────────────────────────────

/// True when the symbol looks like a crypto pair.
fn is_crypto(symbol: &str) -> bool {
    let s = symbol.to_ascii_uppercase();
    s.ends_with("USDT")
        || s.ends_with("USDC")
        || s.ends_with("BTC")
        || s.ends_with("ETH")
        || s.ends_with("BUSD")
}

/// Extract a named list of `f64` from a Python dict.
fn get_f64_col<'py>(bars: &Bound<'py, PyDict>, key: &str) -> PyResult<Vec<f64>> {
    bars.get_item(key)?
        .ok_or_else(|| PyKeyError::new_err(format!("bars dict missing key '{key}'")))?
        .extract::<Vec<f64>>()
}

/// Extract a named list of `i64` from a Python dict.
fn get_i64_col<'py>(bars: &Bound<'py, PyDict>, key: &str) -> PyResult<Vec<i64>> {
    bars.get_item(key)?
        .ok_or_else(|| PyKeyError::new_err(format!("bars dict missing key '{key}'")))?
        .extract::<Vec<i64>>()
}

/// Convert `{"t":[..], "o":[..], "h":[..], "l":[..], "c":[..], "v":[..]}` → `Vec<Bar>`.
fn extract_bars(symbol: &str, bars: &Bound<PyDict>) -> PyResult<Vec<Bar>> {
    let t = get_i64_col(bars, "t")?;
    let o = get_f64_col(bars, "o")?;
    let h = get_f64_col(bars, "h")?;
    let l = get_f64_col(bars, "l")?;
    let c = get_f64_col(bars, "c")?;
    let v = get_f64_col(bars, "v")?;

    let n = t.len();
    if [o.len(), h.len(), l.len(), c.len(), v.len()]
        .iter()
        .any(|&len| len != n)
    {
        return Err(PyValueError::new_err(
            "all bar arrays must have the same length",
        ));
    }

    Ok((0..n)
        .map(|i| Bar::new(t[i], symbol, o[i], h[i], l[i], c[i], v[i]))
        .collect())
}

/// Convert a Python dict of scalar values → `serde_json::Value::Object`.
/// Accepts: bool, int, float, str (other types are stringified).
fn pydict_to_json(d: &Bound<PyDict>) -> PyResult<Value> {
    let mut map = Map::new();
    for (k, v) in d.iter() {
        let key: String = k.extract()?;
        let val = pyany_to_json(&v)?;
        map.insert(key, val);
    }
    Ok(Value::Object(map))
}

fn pyany_to_json(obj: &Bound<PyAny>) -> PyResult<Value> {
    if obj.is_none() {
        return Ok(Value::Null);
    }
    // bool before i64 — Python's bool is a subclass of int
    if let Ok(v) = obj.extract::<bool>() {
        return Ok(Value::Bool(v));
    }
    if let Ok(v) = obj.extract::<i64>() {
        return Ok(json!(v));
    }
    if let Ok(v) = obj.extract::<f64>() {
        return Ok(json!(v));
    }
    if let Ok(v) = obj.extract::<String>() {
        return Ok(Value::String(v));
    }
    // Nested dict → JSON object
    if let Ok(d) = obj.downcast::<PyDict>() {
        return pydict_to_json(d);
    }
    // List / tuple → JSON array
    if let Ok(lst) = obj.downcast::<PyList>() {
        let arr: PyResult<Vec<Value>> = lst.iter().map(|item| pyany_to_json(&item)).collect();
        return Ok(Value::Array(arr?));
    }
    // fallback
    Ok(Value::String(obj.str()?.extract()?))
}

/// Build result dict from a BacktestReport + engine state (shared between run_backtest variants).
fn report_to_pydict<'py>(
    py: Python<'py>,
    report: &alm_report::BacktestReport,
    equity_curve: &[EquityPoint],
    trades: &[alm_core::trade::Trade],
) -> PyResult<Bound<'py, PyDict>> {
    let out = PyDict::new_bound(py);

    // Scalar fields
    out.set_item("strategy",                  &report.strategy)?;
    out.set_item("symbol",                    &report.symbol)?;
    out.set_item("initial_capital",           report.initial_capital)?;
    out.set_item("final_equity",              report.final_equity)?;
    out.set_item("total_return_pct",          report.total_return_pct)?;
    out.set_item("cagr_pct",                  report.cagr_pct)?;
    out.set_item("annualized_volatility_pct", report.annualized_volatility_pct)?;
    out.set_item("sharpe_ratio",              report.sharpe_ratio)?;
    out.set_item("sortino_ratio",             report.sortino_ratio)?;
    out.set_item("calmar_ratio",              report.calmar_ratio)?;
    out.set_item("max_drawdown_pct",          report.max_drawdown_pct)?;
    out.set_item("max_dd_duration_bars",      report.max_dd_duration_bars)?;
    out.set_item("avg_drawdown_pct",          report.avg_drawdown_pct)?;
    out.set_item("total_trades",              report.total_trades)?;
    out.set_item("win_rate_pct",              report.win_rate_pct)?;
    out.set_item("profit_factor",             report.profit_factor)?;
    out.set_item("expectancy",                report.expectancy)?;
    out.set_item("avg_win_pct",               report.avg_win_pct)?;
    out.set_item("avg_loss_pct",              report.avg_loss_pct)?;
    out.set_item("avg_trade_duration_hours",  report.avg_trade_duration_hours)?;
    out.set_item("max_consecutive_losses",    report.max_consecutive_losses)?;
    // Advanced risk metrics
    out.set_item("var_95",          report.var_95)?;
    out.set_item("cvar_95",         report.cvar_95)?;
    out.set_item("omega_ratio",     report.omega_ratio)?;
    out.set_item("tail_ratio",      report.tail_ratio)?;
    out.set_item("recovery_factor", report.recovery_factor)?;
    // New metrics
    out.set_item("sqn",                  report.sqn)?;
    out.set_item("psr",                  report.psr)?;
    out.set_item("skewness",             report.skewness)?;
    out.set_item("excess_kurtosis",      report.excess_kurtosis)?;
    out.set_item("max_consecutive_wins", report.max_consecutive_wins)?;
    out.set_item("largest_win_pct",      report.largest_win_pct)?;
    out.set_item("largest_loss_pct",     report.largest_loss_pct)?;
    // Long/short split
    let long_d = PyDict::new_bound(py);
    long_d.set_item("trades",        report.long_stats.count)?;
    long_d.set_item("win_rate_pct",  report.long_stats.win_rate)?;
    long_d.set_item("profit_factor", report.long_stats.profit_factor)?;
    long_d.set_item("avg_win_pct",   report.long_stats.avg_win_pct)?;
    long_d.set_item("avg_loss_pct",  report.long_stats.avg_loss_pct)?;
    long_d.set_item("expectancy",    report.long_stats.expectancy)?;
    out.set_item("long_stats", long_d)?;
    let short_d = PyDict::new_bound(py);
    short_d.set_item("trades",        report.short_stats.count)?;
    short_d.set_item("win_rate_pct",  report.short_stats.win_rate)?;
    short_d.set_item("profit_factor", report.short_stats.profit_factor)?;
    short_d.set_item("avg_win_pct",   report.short_stats.avg_win_pct)?;
    short_d.set_item("avg_loss_pct",  report.short_stats.avg_loss_pct)?;
    short_d.set_item("expectancy",    report.short_stats.expectancy)?;
    out.set_item("short_stats", short_d)?;
    // Monthly returns: [[year, month, pct], ...]
    let monthly: Vec<[f64; 3]> = report.monthly_returns.iter()
        .map(|&(y, m, r)| [y as f64, m as f64, r])
        .collect();
    out.set_item("monthly_returns", monthly)?;
    // Rolling arrays
    out.set_item("rolling_sharpe",   report.rolling_sharpe.clone())?;
    out.set_item("rolling_drawdown", report.rolling_drawdown.clone())?;
    // Timeframe
    out.set_item("timeframe", report.timeframe.to_string())?;

    // Regime summary
    if let Some(rs) = &report.regime_summary {
        out.set_item("trending_pct",  rs.trending_pct)?;
        out.set_item("ranging_pct",   rs.ranging_pct)?;
        out.set_item("neutral_pct",   rs.neutral_pct)?;
        out.set_item("high_vol_pct",  rs.high_vol_pct)?;
        out.set_item("low_vol_pct",   rs.low_vol_pct)?;
        let changes: Vec<(i64, &str)> =
            rs.changes.iter().map(|(ts, label)| (*ts, label.as_str())).collect();
        out.set_item("regime_changes", changes)?;
    } else {
        out.set_item("trending_pct",   0.0_f64)?;
        out.set_item("ranging_pct",    0.0_f64)?;
        out.set_item("neutral_pct",    0.0_f64)?;
        out.set_item("high_vol_pct",   0.0_f64)?;
        out.set_item("low_vol_pct",    0.0_f64)?;
        out.set_item("regime_changes", Vec::<(i64, String)>::new())?;
    }

    // Equity curve
    let curve = PyList::empty_bound(py);
    for pt in equity_curve {
        let d = PyDict::new_bound(py);
        d.set_item("t",      pt.timestamp)?;
        d.set_item("equity", pt.equity)?;
        curve.append(d)?;
    }
    out.set_item("equity_curve", curve)?;

    // Trades
    let trades_list = PyList::empty_bound(py);
    let mut pnl_pct_list: Vec<f64> = Vec::new();
    let mut equity_pnl_pct_list: Vec<f64> = Vec::new();
    for t in trades {
        let d = PyDict::new_bound(py);
        d.set_item("symbol",      &t.symbol)?;
        d.set_item("side",        if t.side == Side::Buy { "long" } else { "short" })?;
        d.set_item("qty",         t.qty)?;
        d.set_item("entry_price", t.entry_price)?;
        d.set_item("exit_price",  t.exit_price)?;
        d.set_item("entry_ts",    t.entry_timestamp)?;
        d.set_item("exit_ts",     t.exit_timestamp)?;
        d.set_item("pnl",         t.pnl)?;
        d.set_item("pnl_pct",     t.pnl_pct)?;
        d.set_item("mae_pct",     t.mae_pct)?;
        d.set_item("mfe_pct",     t.mfe_pct)?;
        d.set_item("bars_held",   t.bars_held)?;
        d.set_item("exit_reason", t.exit_reason.to_string())?;
        trades_list.append(d)?;
        pnl_pct_list.push(t.pnl_pct);

        // Equity-relative return: pnl / equity_at_entry — correct input for Monte Carlo
        let idx = equity_curve.partition_point(|p| p.timestamp <= t.entry_timestamp);
        let eq_at_entry = if idx > 0 { equity_curve[idx - 1].equity } else { report.initial_capital };
        if eq_at_entry > f64::EPSILON {
            equity_pnl_pct_list.push(t.pnl / eq_at_entry);
        }
    }
    out.set_item("trades",          trades_list)?;
    out.set_item("pnl_pct",         pnl_pct_list)?;
    out.set_item("equity_pnl_pct",  equity_pnl_pct_list)?;

    Ok(out)
}

/// Parse optional `exit` dict → `ExitRules`.
///
/// Supported keys:
/// - `sl`: fixed stop-loss fraction (e.g. 0.03)
/// - `tp`: fixed take-profit fraction (e.g. 0.06)
/// - `trailing_stop`: trailing stop fraction from peak (e.g. 0.05)
/// - `max_bars`: time-based exit after N bars
/// - `atr_sl`: ATR stop multiplier (e.g. 1.5)
/// - `atr_tp`: ATR target multiplier (e.g. 3.0)
/// - `atr_period`: ATR period for dynamic levels (default 14)
/// - `intra_bar_mode`: `"close_only"` (default) | `"pessimistic"` | `"ohlc_heuristic"`
fn extract_exit_rules(exit: Option<&Bound<PyDict>>) -> PyResult<ExitRules> {
    let Some(d) = exit else { return Ok(ExitRules::default()); };
    let sl            = d.get_item("sl")?.and_then(|v| v.extract::<f64>().ok());
    let tp            = d.get_item("tp")?.and_then(|v| v.extract::<f64>().ok());
    let trailing_stop = d.get_item("trailing_stop")?.and_then(|v| v.extract::<f64>().ok());
    let max_bars      = d.get_item("max_bars")?.and_then(|v| v.extract::<usize>().ok());
    let atr_sl        = d.get_item("atr_sl")?.and_then(|v| v.extract::<f64>().ok());
    let atr_tp        = d.get_item("atr_tp")?.and_then(|v| v.extract::<f64>().ok());
    let atr_period    = d.get_item("atr_period")?.and_then(|v| v.extract::<usize>().ok()).unwrap_or(14);
    let intra_bar_mode = match d.get_item("intra_bar_mode")?.and_then(|v| v.extract::<String>().ok()).as_deref() {
        Some("pessimistic")    => IntraBarMode::Pessimistic,
        Some("ohlc_heuristic") => IntraBarMode::OhlcHeuristic,
        _                      => IntraBarMode::CloseOnly,
    };
    Ok(ExitRules {
        stop_loss_pct: sl,
        take_profit_pct: tp,
        trailing_stop_pct: trailing_stop,
        max_bars_held: max_bars,
        atr_stop_multiplier: atr_sl,
        atr_target_multiplier: atr_tp,
        atr_period,
        intra_bar_mode,
        ..Default::default()
    })
}

/// Compute exposure % = sum(trade durations) / total equity-curve span.
fn compute_exposure_pct(equity_curve: &[EquityPoint], trades: &[alm_core::trade::Trade]) -> f64 {
    let total_ms = equity_curve.last()
        .and_then(|last| equity_curve.first().map(|first| last.timestamp - first.timestamp))
        .unwrap_or(0);
    let held_ms: i64 = trades.iter().map(|t| t.exit_timestamp - t.entry_timestamp).sum();
    if total_ms > 0 { held_ms as f64 / total_ms as f64 * 100.0 } else { 0.0 }
}

/// Attach `indicator_series`, `exposure_pct`, and optional `benchmark` to an existing result dict.
fn add_extra_fields<'py>(
    py: Python<'py>,
    d: &Bound<'py, PyDict>,
    indicator_series: HashMap<String, Vec<(i64, f64)>>,
    exposure_pct: f64,
    bh: Option<BuyHoldBenchmark>,
) -> PyResult<()> {
    d.set_item("exposure_pct", exposure_pct)?;

    // indicator_series → {name: {"t": [ms, ...], "v": [f64, ...]}}
    let ind_dict = PyDict::new_bound(py);
    for (name, pts) in indicator_series {
        let ts: Vec<i64> = pts.iter().map(|(t, _)| *t).collect();
        let vs: Vec<f64> = pts.iter().map(|(_, v)| *v).collect();
        let series = PyDict::new_bound(py);
        series.set_item("t", ts)?;
        series.set_item("v", vs)?;
        ind_dict.set_item(name, series)?;
    }
    d.set_item("indicator_series", ind_dict)?;

    if let Some(bh) = bh {
        let b = PyDict::new_bound(py);
        b.set_item("total_return_pct",          bh.total_return_pct)?;
        b.set_item("cagr_pct",                  bh.cagr_pct)?;
        b.set_item("annualized_volatility_pct", bh.annualized_volatility_pct)?;
        b.set_item("sharpe_ratio",              bh.sharpe_ratio)?;
        b.set_item("sortino_ratio",             bh.sortino_ratio)?;
        b.set_item("max_drawdown_pct",          bh.max_drawdown_pct)?;
        b.set_item("max_dd_duration_bars",      bh.max_dd_duration_bars)?;
        d.set_item("benchmark", b)?;
    }

    Ok(())
}

// ── Exposed Python functions ──────────────────────────────────────────────────

/// Run a full event-driven backtest.
///
/// Parameters
/// ----------
/// symbol : str
///     Ticker symbol (e.g. ``"BTCUSDT"``, ``"AAPL"``).
/// strategy : str
///     Strategy name known to the factory (see :func:`list_strategies`).
/// params : dict
///     Flat dict of strategy parameters (e.g. ``{"period": 14}``).
/// bars : dict
///     Bar data with keys ``"t"`` (i64 timestamps ms), ``"o"``, ``"h"``, ``"l"``,
///     ``"c"``, ``"v"`` (all f64 lists of equal length).
/// capital : float, optional
///     Initial capital in base currency (default 10 000).
/// commission : float, optional
///     Round-trip commission as a fraction (default 0.001 = 0.1%).
/// slippage : float, optional
///     Slippage per trade as a fraction (default 0.0005 = 0.05%).
/// lot_size : float, optional
///     Minimum tradeable lot.  ``0.0`` = fractional (crypto).  ``1.0`` = whole
///     shares (stocks).  ``-1.0`` (default) = auto-detect from symbol name.
/// sizing : str, optional
///     Position sizing method: ``"fixed"`` (default), ``"atr"``, or ``"kelly"``.
/// sizing_params : dict, optional
///     Parameters for the sizing method.
///     - ``"fixed"``: no extra params (uses ``pct=0.95``).
///     - ``"atr"``: ``{"risk_per_trade": 0.01, "atr_multiplier": 2.0}``.
///     - ``"kelly"``: ``{"win_rate": 0.5, "avg_win": 0.05, "avg_loss": 0.02, "fraction": 0.5}``.
/// next_bar : bool, optional
///     When True, signals are explicitly buffered and filled at next bar's open (default False).
/// exit : dict, optional
///     Exit overrides: ``{"sl": 0.03, "tp": 0.06, "max_bars": 100}``.
///     ``sl``/``tp`` are fractions (0.03 = 3%). Applied on top of strategy signals.
/// risk_free : float, optional
///     Annual risk-free rate for Sharpe/Sortino (default 0.04).
///
/// Returns
/// -------
/// dict
///     All ``BacktestReport`` fields plus ``"equity_curve"``, ``"trades"``,
///     ``"pnl_pct"``, ``"indicator_series"``, ``"exposure_pct"``, ``"benchmark"``.
#[pyfunction]
#[pyo3(signature = (symbol, strategy, params, bars, capital=10_000.0, commission=0.001, slippage=0.0005, lot_size=-1.0, sizing="fixed", sizing_params=None, next_bar=false, exit=None, risk_free=0.04))]
fn run_backtest<'py>(
    py: Python<'py>,
    symbol: &str,
    strategy: &str,
    params: &Bound<'py, PyDict>,
    bars: &Bound<'py, PyDict>,
    capital: f64,
    commission: f64,
    slippage: f64,
    lot_size: f64,
    sizing: &str,
    sizing_params: Option<&Bound<'py, PyDict>>,
    next_bar: bool,
    exit: Option<&Bound<'py, PyDict>>,
    risk_free: f64,
) -> PyResult<Bound<'py, PyDict>> {
    let params_json = pydict_to_json(params)?;
    let bar_vec = extract_bars(symbol, bars)?;

    let strat = build_strategy(strategy, &params_json)
        .map_err(|e| PyRuntimeError::new_err(format!("strategy build error: {e}")))?;

    let resolved_lot = if lot_size < 0.0 {
        if is_crypto(symbol) { 0.0 } else { 1.0 }
    } else {
        lot_size
    };

    let exit_rules = extract_exit_rules(exit)?;

    // Pre-extract close+ts for buy&hold benchmark (bar_vec moves into feed below)
    let bh_closes: Vec<f64>     = bar_vec.iter().map(|b| b.close).collect();
    let bh_timestamps: Vec<i64> = bar_vec.iter().map(|b| b.timestamp).collect();

    let sp: Value = match sizing_params {
        Some(d) => pydict_to_json(d)?,
        None => Value::Object(Map::new()),
    };
    let get_f = |key: &str, default: f64| -> f64 {
        sp.get(key).and_then(|v| v.as_f64()).unwrap_or(default)
    };
    let get_u = |key: &str, default: usize| -> usize {
        sp.get(key).and_then(|v| v.as_u64()).map(|v| v as usize).unwrap_or(default)
    };

    macro_rules! run_engine {
        ($risk:expr) => {{
            let mut feed = BarVecFeed::new(bar_vec, symbol.to_string());
            let mut engine =
                Engine::sync(capital, strat, $risk, commission, slippage)
                    .with_exit_rules(exit_rules)
                    .with_next_bar(next_bar);
            let report = engine.run(&mut feed, risk_free);

            let ind_series  = engine.strategy.take_indicator_series();
            let equity_curve = engine.portfolio.equity_curve.clone();
            let trades       = engine.portfolio.trades.clone();
            let exposure_pct = compute_exposure_pct(&equity_curve, &trades);
            let bh = if bh_closes.len() >= 2 {
                Some(BuyHoldBenchmark::compute(&bh_closes, &bh_timestamps, risk_free))
            } else { None };

            let d = report_to_pydict(py, &report, &equity_curve, &trades)?;
            add_extra_fields(py, &d, ind_series, exposure_pct, bh)?;
            Ok(d)
        }};
    }

    match sizing {
        "atr" => {
            let risk = AtrSizing::new(
                get_f("risk_per_trade", 0.01),
                get_f("atr_multiplier", 2.0),
                get_u("max_positions", 1),
            );
            run_engine!(risk)
        }
        "kelly" => {
            let risk = KellySizing::new(
                get_f("win_rate", 0.5),
                get_f("avg_win", 0.05),
                get_f("avg_loss", 0.02),
                get_f("fraction", 0.5),
                get_u("max_positions", 1),
            );
            run_engine!(risk)
        }
        _ => {
            // "fixed" (default)
            let risk = FixedFractional::new(0.95, 1).with_lot_size(resolved_lot);
            run_engine!(risk)
        }
    }
}

/// Run a CEL (expression-based) backtest with ergonomic entry/exit expressions.
///
/// Parameters
/// ----------
/// symbol : str
///     Ticker symbol.
/// entry_expr : str
///     CEL entry expression, e.g. ``"rsi(14) < 30.0 && close > ema(200)"``.
/// exit_expr : str
///     CEL exit expression, e.g. ``"rsi(14) > 70.0 || close < ema(50)"``.
///     Use ``"false"`` to rely solely on the ``exit`` config.
/// bars : dict
///     Bar data — same format as :func:`run_backtest`.
/// params : dict, optional
///     Extra strategy parameters (e.g. indicator period overrides).
/// capital, commission, slippage, lot_size, sizing, sizing_params, next_bar, risk_free
///     Same meaning as :func:`run_backtest`.
/// exit : dict, optional
///     Engine-level exits: ``{"sl": 0.03, "tp": 0.06, "max_bars": 100}``.
/// candle_type : str, optional
///     ``"plain"`` (default), ``"heiken_ashi"``, or ``"smooth_heiken_ashi"``.
///
/// Returns
/// -------
/// dict
///     Same as :func:`run_backtest`, including ``indicator_series``.
#[pyfunction]
#[pyo3(signature = (symbol, entry_expr, exit_expr, bars, params=None, capital=10_000.0, commission=0.001, slippage=0.0005, lot_size=-1.0, sizing="fixed", sizing_params=None, next_bar=false, exit=None, candle_type="plain", risk_free=0.04))]
#[allow(clippy::too_many_arguments)]
fn run_cel_backtest<'py>(
    py: Python<'py>,
    symbol: &str,
    entry_expr: &str,
    exit_expr: &str,
    bars: &Bound<'py, PyDict>,
    params: Option<&Bound<'py, PyDict>>,
    capital: f64,
    commission: f64,
    slippage: f64,
    lot_size: f64,
    sizing: &str,
    sizing_params: Option<&Bound<'py, PyDict>>,
    next_bar: bool,
    exit: Option<&Bound<'py, PyDict>>,
    candle_type: &str,
    risk_free: f64,
) -> PyResult<Bound<'py, PyDict>> {
    // Build CEL params map: user params + entry/exit/candle_type
    let mut cel_map: Map<String, Value> = match params {
        Some(d) => match pydict_to_json(d)? {
            Value::Object(m) => m,
            _ => Map::new(),
        },
        None => Map::new(),
    };
    cel_map.insert("entry".into(),       json!(entry_expr));
    cel_map.insert("exit".into(),        json!(exit_expr));
    cel_map.insert("candle_type".into(), json!(candle_type));
    let params_json = Value::Object(cel_map);

    let bar_vec = extract_bars(symbol, bars)?;
    let strat = build_strategy("cel", &params_json)
        .map_err(|e| PyRuntimeError::new_err(format!("CEL strategy build error: {e}")))?;

    let resolved_lot = if lot_size < 0.0 {
        if is_crypto(symbol) { 0.0 } else { 1.0 }
    } else {
        lot_size
    };

    let exit_rules = extract_exit_rules(exit)?;
    let bh_closes: Vec<f64>     = bar_vec.iter().map(|b| b.close).collect();
    let bh_timestamps: Vec<i64> = bar_vec.iter().map(|b| b.timestamp).collect();

    let sp: Value = match sizing_params {
        Some(d) => pydict_to_json(d)?,
        None => Value::Object(Map::new()),
    };
    let get_f = |key: &str, default: f64| -> f64 {
        sp.get(key).and_then(|v| v.as_f64()).unwrap_or(default)
    };
    let get_u = |key: &str, default: usize| -> usize {
        sp.get(key).and_then(|v| v.as_u64()).map(|v| v as usize).unwrap_or(default)
    };

    macro_rules! run_engine {
        ($risk:expr) => {{
            let mut feed = BarVecFeed::new(bar_vec, symbol.to_string());
            let mut engine =
                Engine::sync(capital, strat, $risk, commission, slippage)
                    .with_exit_rules(exit_rules)
                    .with_next_bar(next_bar);
            let report = engine.run(&mut feed, risk_free);

            let ind_series   = engine.strategy.take_indicator_series();
            let equity_curve = engine.portfolio.equity_curve.clone();
            let trades       = engine.portfolio.trades.clone();
            let exposure_pct = compute_exposure_pct(&equity_curve, &trades);
            let bh = if bh_closes.len() >= 2 {
                Some(BuyHoldBenchmark::compute(&bh_closes, &bh_timestamps, risk_free))
            } else { None };

            let d = report_to_pydict(py, &report, &equity_curve, &trades)?;
            add_extra_fields(py, &d, ind_series, exposure_pct, bh)?;
            Ok(d)
        }};
    }

    match sizing {
        "atr" => run_engine!(AtrSizing::new(
            get_f("risk_per_trade", 0.01),
            get_f("atr_multiplier", 2.0),
            get_u("max_positions", 1),
        )),
        "kelly" => run_engine!(KellySizing::new(
            get_f("win_rate", 0.5),
            get_f("avg_win", 0.05),
            get_f("avg_loss", 0.02),
            get_f("fraction", 0.5),
            get_u("max_positions", 1),
        )),
        _ => run_engine!(FixedFractional::new(0.95, 1).with_lot_size(resolved_lot)),
    }
}

// ── run_rhai_backtest ────────────────────────────────────────────────────────

/// Run a backtest using a **Rhai script** strategy.
///
/// The script declares indicators with ``indicator(type, period)`` and assigns
/// boolean variables ``entry`` / ``exit`` (and optionally ``tp`` / ``sl`` for
/// target/stop prices). Any ``plot("name", value)`` calls accumulate into the
/// ``indicator_series`` key of the returned dict.
///
/// Parameters
/// ----------
/// symbol : str
///     Asset symbol, e.g. ``"BTCUSDT"``.
/// script : str
///     Rhai script. Example:
///
/// ```text
/// let ema9  = indicator("ema", 9);
/// let ema21 = indicator("ema", 21);
///
/// let entry = cross_above(ema9, ema21);
/// let exit  = cross_below(ema9, ema21);
///
/// plot("ema9",  ema9[0]);
/// plot("ema21", ema21[0]);
/// ```
///
/// bars : dict
///     Bar data — same format as :func:`run_backtest`.
/// capital, commission, slippage, lot_size, sizing, sizing_params, next_bar, risk_free
///     Same meaning as :func:`run_backtest`.
/// exit : dict, optional
///     Engine-level exits: ``{"sl": 0.03, "tp": 0.06, "max_bars": 100}``.
///
/// Returns
/// -------
/// dict
///     Same as :func:`run_backtest`. ``indicator_series`` contains one entry
///     per ``plot()`` call: ``{"name": [{"t": ts_ms, "v": float}, ...]}``.
#[pyfunction]
#[pyo3(signature = (symbol, script, bars, capital=10_000.0, commission=0.001, slippage=0.0005, lot_size=-1.0, sizing="fixed", sizing_params=None, next_bar=false, exit=None, risk_free=0.04))]
#[allow(clippy::too_many_arguments)]
fn run_rhai_backtest<'py>(
    py: Python<'py>,
    symbol: &str,
    script: &str,
    bars: &Bound<'py, PyDict>,
    capital: f64,
    commission: f64,
    slippage: f64,
    lot_size: f64,
    sizing: &str,
    sizing_params: Option<&Bound<'py, PyDict>>,
    next_bar: bool,
    exit: Option<&Bound<'py, PyDict>>,
    risk_free: f64,
) -> PyResult<Bound<'py, PyDict>> {
    let params_json = json!({ "script": script });
    let bar_vec = extract_bars(symbol, bars)?;
    let strat = build_strategy("rhai", &params_json)
        .map_err(|e| PyRuntimeError::new_err(format!("Rhai strategy build error: {e}")))?;

    let resolved_lot = if lot_size < 0.0 {
        if is_crypto(symbol) { 0.0 } else { 1.0 }
    } else {
        lot_size
    };

    let exit_rules = extract_exit_rules(exit)?;
    let bh_closes: Vec<f64>     = bar_vec.iter().map(|b| b.close).collect();
    let bh_timestamps: Vec<i64> = bar_vec.iter().map(|b| b.timestamp).collect();

    let sp: Value = match sizing_params {
        Some(d) => pydict_to_json(d)?,
        None => Value::Object(Map::new()),
    };
    let get_f = |key: &str, default: f64| -> f64 {
        sp.get(key).and_then(|v| v.as_f64()).unwrap_or(default)
    };
    let get_u = |key: &str, default: usize| -> usize {
        sp.get(key).and_then(|v| v.as_u64()).map(|v| v as usize).unwrap_or(default)
    };

    macro_rules! run_engine {
        ($risk:expr) => {{
            let mut feed = BarVecFeed::new(bar_vec, symbol.to_string());
            let mut engine =
                Engine::sync(capital, strat, $risk, commission, slippage)
                    .with_exit_rules(exit_rules)
                    .with_next_bar(next_bar);
            let report = engine.run(&mut feed, risk_free);

            let ind_series   = engine.strategy.take_indicator_series();
            let equity_curve = engine.portfolio.equity_curve.clone();
            let trades       = engine.portfolio.trades.clone();
            let exposure_pct = compute_exposure_pct(&equity_curve, &trades);
            let bh = if bh_closes.len() >= 2 {
                Some(BuyHoldBenchmark::compute(&bh_closes, &bh_timestamps, risk_free))
            } else { None };

            let d = report_to_pydict(py, &report, &equity_curve, &trades)?;
            add_extra_fields(py, &d, ind_series, exposure_pct, bh)?;
            Ok(d)
        }};
    }

    match sizing {
        "atr" => run_engine!(AtrSizing::new(
            get_f("risk_per_trade", 0.01),
            get_f("atr_multiplier", 2.0),
            get_u("max_positions", 1),
        )),
        "kelly" => run_engine!(KellySizing::new(
            get_f("win_rate", 0.5),
            get_f("avg_win", 0.05),
            get_f("avg_loss", 0.02),
            get_f("fraction", 0.5),
            get_u("max_positions", 1),
        )),
        _ => run_engine!(FixedFractional::new(0.95, 1).with_lot_size(resolved_lot)),
    }
}

/// Apply a 2-state Kalman filter to a price series.
///
/// Parameters
/// ----------
/// prices : list[float]
///     Raw price series (e.g. close prices).
/// q_pos : float, optional
///     Process noise — position component (default 0.001).
/// q_vel : float, optional
///     Process noise — velocity component (default 0.001).
/// r : float, optional
///     Measurement noise (default 1.0).
///
/// Returns
/// -------
/// dict
///     ``{"value": list[float], "velocity": list[float]}``
///     Both arrays have the same length as ``prices``.
///     ``value`` = filtered price; ``velocity`` = slope per bar (>0 = uptrend).
#[pyfunction]
#[pyo3(signature = (prices, q_pos=0.001, q_vel=0.001, r=1.0))]
fn kalman<'py>(
    py: Python<'py>,
    prices: Vec<f64>,
    q_pos: f64,
    q_vel: f64,
    r: f64,
) -> PyResult<Bound<'py, PyDict>> {
    let mut kf = KalmanFilter::new(q_pos, q_vel, r);
    let mut values: Vec<f64> = Vec::with_capacity(prices.len());
    let mut velocities: Vec<f64> = Vec::with_capacity(prices.len());

    for p in &prices {
        let v = kf.update(*p);
        values.push(v.value);
        velocities.push(v.velocity);
    }

    let out = PyDict::new_bound(py);
    out.set_item("value",    values)?;
    out.set_item("velocity", velocities)?;
    Ok(out)
}

/// Bootstrap Monte Carlo simulation over a list of trade returns.
///
/// Resamples the trade PnL list with replacement across ``n_iter`` paths.
/// Each path compounds equity from ``capital`` and is checked for ruin.
///
/// Parameters
/// ----------
/// pnl_pct : list[float]
///     Trade returns as equity-relative fractions (e.g. 0.05 = +5% of equity at entry).
///     Use ``result["equity_pnl_pct"]`` from :func:`run_backtest` for correct compounding.
///     ``result["pnl_pct"]`` is position-level and will slightly over-estimate results
///     when ``position_size_pct < 1.0``.
/// capital : float, optional
///     Starting capital for each simulation path (default 10 000).
/// n_iter : int, optional
///     Number of bootstrap iterations (default 1 000).
/// ruin_threshold : float, optional
///     Fraction of initial capital below which a path is "ruined" (default 0.5 = 50% loss).
/// seed : int, optional
///     RNG seed for reproducibility (default None = time-based).
///
/// Returns
/// -------
/// dict
///     ``{
///       "ruin_probability": float,
///       "final": {"p5": float, "p10": float, ..., "p95": float},
///       "curves": {"p5": list[float], ..., "p95": list[float]}
///     }``
///     ``curves`` arrays have length ``n_trades + 1`` (including the starting equity).
#[pyfunction]
#[pyo3(signature = (pnl_pct, capital=10_000.0, n_iter=1000, ruin_threshold=0.5, seed=None))]
fn monte_carlo<'py>(
    py: Python<'py>,
    pnl_pct: Vec<f64>,
    capital: f64,
    n_iter: usize,
    ruin_threshold: f64,
    seed: Option<u64>,
) -> PyResult<Bound<'py, PyDict>> {
    let cfg = MonteCarloConfig { n_iter, ruin_threshold, seed };

    let result = mc_run(&pnl_pct, capital, &cfg)
        .ok_or_else(|| PyValueError::new_err("monte_carlo requires at least 1 trade"))?;

    let out = PyDict::new_bound(py);
    out.set_item("ruin_probability", result.ruin_probability)?;

    // Final-equity percentiles
    let finals = PyDict::new_bound(py);
    finals.set_item("p5",  result.final_p5)?;
    finals.set_item("p10", result.final_p10)?;
    finals.set_item("p25", result.final_p25)?;
    finals.set_item("p50", result.final_p50)?;
    finals.set_item("p75", result.final_p75)?;
    finals.set_item("p90", result.final_p90)?;
    finals.set_item("p95", result.final_p95)?;
    out.set_item("final", finals)?;

    // Percentile equity curves
    let curves = PyDict::new_bound(py);
    curves.set_item("p5",  result.curve_p5)?;
    curves.set_item("p10", result.curve_p10)?;
    curves.set_item("p25", result.curve_p25)?;
    curves.set_item("p50", result.curve_p50)?;
    curves.set_item("p75", result.curve_p75)?;
    curves.set_item("p90", result.curve_p90)?;
    curves.set_item("p95", result.curve_p95)?;
    out.set_item("curves", curves)?;

    Ok(out)
}

/// Compute Buy-and-Hold benchmark statistics from a close-price series.
///
/// Parameters
/// ----------
/// closes : list[float]
///     Close prices in chronological order.
/// timestamps : list[int]
///     Unix millisecond timestamps, same length as ``closes``.
/// risk_free : float, optional
///     Annual risk-free rate (default 0.04 = 4%).
///
/// Returns
/// -------
/// dict
///     ``{total_return_pct, cagr_pct, annualized_volatility_pct,
///        sharpe_ratio, sortino_ratio, max_drawdown_pct, max_dd_duration_bars}``
#[pyfunction]
#[pyo3(signature = (closes, timestamps, risk_free=0.04))]
fn buy_hold_benchmark<'py>(
    py: Python<'py>,
    closes: Vec<f64>,
    timestamps: Vec<i64>,
    risk_free: f64,
) -> PyResult<Bound<'py, PyDict>> {
    if closes.len() != timestamps.len() {
        return Err(PyValueError::new_err("closes and timestamps must have the same length"));
    }
    let bh = BuyHoldBenchmark::compute(&closes, &timestamps, risk_free);
    let d = PyDict::new_bound(py);
    d.set_item("total_return_pct",          bh.total_return_pct)?;
    d.set_item("cagr_pct",                  bh.cagr_pct)?;
    d.set_item("annualized_volatility_pct", bh.annualized_volatility_pct)?;
    d.set_item("sharpe_ratio",              bh.sharpe_ratio)?;
    d.set_item("sortino_ratio",             bh.sortino_ratio)?;
    d.set_item("max_drawdown_pct",          bh.max_drawdown_pct)?;
    d.set_item("max_dd_duration_bars",      bh.max_dd_duration_bars)?;
    Ok(d)
}

/// Run a backtest for multiple symbols and compute portfolio-level analytics.
///
/// Parameters
/// ----------
/// symbol_bar_pairs : list of (str, dict)
///     List of ``(symbol, bars_dict)`` where ``bars_dict`` has the same format
///     as ``run_backtest``'s ``bars`` parameter.
/// strategy : str
///     Same strategy name applied to all symbols.
/// params : dict
///     Strategy parameters (same for all symbols).
/// capital : float, optional
///     Initial capital per symbol (default 10 000).
/// commission : float, optional
///     Commission fraction (default 0.001).
/// slippage : float, optional
///     Slippage fraction (default 0.0005).
/// benchmark_equity : list of float, optional
///     Reference equity curve for beta/alpha computation.
///
/// Returns
/// -------
/// dict
///     ``{
///       "results": {symbol: backtest_result_dict, ...},
///       "analytics": {
///         "symbols": [...],
///         "correlation_matrix": [[...], ...],
///         "beta": [...],
///         "alpha_annualized": [...],
///         "turnover_pct": [...],
///         "diversification_ratio": float,
///       }
///     }``
#[pyfunction]
#[pyo3(signature = (symbol_bar_pairs, strategy, params, capital=10_000.0, commission=0.001, slippage=0.0005, benchmark_equity=None))]
fn run_portfolio_backtest<'py>(
    py: Python<'py>,
    symbol_bar_pairs: Vec<(String, Bound<'py, PyDict>)>,
    strategy: &str,
    params: &Bound<'py, PyDict>,
    capital: f64,
    commission: f64,
    slippage: f64,
    benchmark_equity: Option<Vec<f64>>,
) -> PyResult<Bound<'py, PyDict>> {
    let params_json = pydict_to_json(params)?;

    let mut symbol_reports: Vec<(String, alm_report::BacktestReport, Vec<EquityPoint>)> = Vec::new();
    let results_dict = PyDict::new_bound(py);

    for (symbol, bars_dict) in &symbol_bar_pairs {
        let bar_vec = extract_bars(symbol, bars_dict)?;
        let strat = build_strategy(strategy, &params_json)
            .map_err(|e| PyRuntimeError::new_err(format!("strategy build error: {e}")))?;

        let resolved_lot = if is_crypto(symbol) { 0.0 } else { 1.0 };
        let risk = FixedFractional::new(0.95, 1).with_lot_size(resolved_lot);

        let mut feed = BarVecFeed::new(bar_vec, symbol.clone());
        let mut engine = Engine::sync(capital, strat, risk, commission, slippage);
        let report = engine.run(&mut feed, 0.04);

        let equity_curve = engine.portfolio.equity_curve.clone();
        let trades = engine.portfolio.trades.clone();

        let result_dict = report_to_pydict(py, &report, &equity_curve, &trades)?;
        results_dict.set_item(symbol, result_dict)?;

        symbol_reports.push((symbol.clone(), report, equity_curve));
    }

    // Compute portfolio analytics.
    let bench = benchmark_equity.unwrap_or_default();
    let analytics = portfolio_analyze(&symbol_reports, &bench);

    let analytics_dict = PyDict::new_bound(py);
    analytics_dict.set_item("symbols", analytics.symbols)?;

    // Correlation matrix → list of lists
    let corr_list = PyList::empty_bound(py);
    for row in &analytics.correlation_matrix {
        corr_list.append(row.clone())?;
    }
    analytics_dict.set_item("correlation_matrix", corr_list)?;
    analytics_dict.set_item("beta",                  analytics.beta)?;
    analytics_dict.set_item("alpha_annualized",      analytics.alpha_annualized)?;
    analytics_dict.set_item("turnover_pct",          analytics.turnover_pct)?;
    analytics_dict.set_item("diversification_ratio", analytics.diversification_ratio)?;

    let out = PyDict::new_bound(py);
    out.set_item("results",   results_dict)?;
    out.set_item("analytics", analytics_dict)?;
    Ok(out)
}

/// Run one or more indicators bar-by-bar and return their full time series.
///
/// Useful for cross-validating indicator values against Python reference
/// implementations without running a full backtest.
///
/// Parameters
/// ----------
/// symbol : str
///     Ticker symbol (only used to label bars).
/// bars : dict
///     Same format as :func:`run_backtest` — ``"t"``, ``"o"``, ``"h"``, ``"l"``,
///     ``"c"``, ``"v"`` lists.
/// indicators : dict
///     Mapping of ``name → config_dict`` using the same config format as
///     ``DynamicStrategy``.  Examples:
///
/// ```text
/// {
///   "ema_20":  {"type": "ema",    "period": 20},
///   "rsi_14":  {"type": "rsi",    "period": 14},
///   "atr_14":  {"type": "atr",    "period": 14},
///   "macd":    {"type": "macd",   "fast": 12, "slow": 26, "signal": 9},
///   "bb_20":   {"type": "bbands", "period": 20, "std_dev": 2.0},
///   "st_10":   {"type": "supertrend", "period": 10, "multiplier": 3.0},
/// }
/// ```
///
/// Returns
/// -------
/// dict
///     ``{name: {field: list[float | None], ...}, ...}``
///     Warmup bars (indicator not yet ready) are represented as ``None``.
///     Single-value indicators (EMA, RSI, ATR, …) have one field ``"value"``.
///     Multi-value indicators (MACD, BBands, SuperTrend, …) have one key per output field.
#[pyfunction]
fn run_indicators<'py>(
    py: Python<'py>,
    symbol: &str,
    bars: &Bound<'py, PyDict>,
    indicators: &Bound<'py, PyDict>,
) -> PyResult<Bound<'py, PyDict>> {
    let bar_vec = extract_bars(symbol, bars)?;
    let n = bar_vec.len();

    // Build (name, IndicatorBox) pairs preserving insertion order.
    let mut boxes: Vec<(String, IndicatorBox)> = Vec::new();
    for (k, v) in indicators.iter() {
        let name: String = k.extract()?;
        let cfg = pyany_to_json(&v)?;
        let ind = IndicatorBox::from_config(&cfg)
            .map_err(|e| PyRuntimeError::new_err(format!("indicator '{name}': {e}")))?;
        boxes.push((name, ind));
    }

    // First pass: collect raw per-bar results as Option<HashMap<String,f64>>.
    // Index: boxes_idx → Vec<Option<HashMap>>
    let mut raw: Vec<Vec<Option<std::collections::HashMap<String, f64>>>> =
        boxes.iter().map(|_| Vec::with_capacity(n)).collect();

    for bar in &bar_vec {
        for (i, (_, ind)) in boxes.iter_mut().enumerate() {
            raw[i].push(ind.update(bar));
        }
    }

    // Second pass: pivot into name → field → Vec<Option<f64>>.
    // Discover field names from first non-None result.
    let out = PyDict::new_bound(py);
    for (i, (name, _)) in boxes.iter().enumerate() {
        let rows = &raw[i];

        // Discover all field names.
        let fields: Vec<String> = rows.iter()
            .find_map(|r| r.as_ref())
            .map(|m| { let mut ks: Vec<_> = m.keys().cloned().collect(); ks.sort(); ks })
            .unwrap_or_default();

        let ind_dict = PyDict::new_bound(py);
        for field in &fields {
            let py_list = PyList::empty_bound(py);
            for row in rows {
                match row.as_ref().and_then(|m| m.get(field)) {
                    Some(v) => py_list.append(v)?,
                    None    => py_list.append(py.None())?,
                }
            }
            ind_dict.set_item(field, py_list)?;
        }
        out.set_item(name, ind_dict)?;
    }
    Ok(out)
}

/// Walk-forward validation — **disabled, FUTURE[wf-v2]**.
///
/// Walk-forward requires a parameter optimisation layer to be meaningful.
/// That layer does not exist yet. Calling this function returns immediately
/// with ``{"note": "disabled — FUTURE[wf-v2]"}``.
///
/// The implementation is preserved in source and will be re-enabled together
/// with the optimisation layer (grid search / Optuna) in wf-v2.
#[pyfunction]
#[pyo3(signature = (symbol, strategy, params, bars, is_bars, oos_bars, step_bars=0, capital=10_000.0, commission=0.001, slippage=0.0005, risk_free=0.04, mode="rolling", embargo_bars=0, min_oos_trades=None))]
#[allow(clippy::too_many_arguments, unused_variables)]
fn run_walk_forward<'py>(
    py: Python<'py>,
    symbol: &str,
    strategy: &str,
    params: &Bound<'py, PyDict>,
    bars: &Bound<'py, PyDict>,
    is_bars: usize,
    oos_bars: usize,
    step_bars: usize,
    capital: f64,
    commission: f64,
    slippage: f64,
    risk_free: f64,
    mode: &str,
    embargo_bars: usize,
    min_oos_trades: Option<usize>,
) -> PyResult<Bound<'py, PyDict>> {
    let out = PyDict::new_bound(py);
    out.set_item("note", "disabled — FUTURE[wf-v2]")?;
    Ok(out)

    /* --- FUTURE[wf-v2]: re-enable together with optimisation layer ---
    use alm_engine::{walk_forward_sync, WalkForwardConfig, WalkForwardMode};

    let params_json = pydict_to_json(params)?;
    let bar_vec = extract_bars(symbol, bars)?;
    let strat = build_strategy(strategy, &params_json)
        .map_err(|e| PyRuntimeError::new_err(format!("strategy build error: {e}")))?;

    let resolved_lot = if is_crypto(symbol) { 0.0 } else { 1.0 };
    let risk = FixedFractional::new(0.95, 1).with_lot_size(resolved_lot);

    let step = if step_bars == 0 { oos_bars } else { step_bars };
    let mut cfg = WalkForwardConfig::new(is_bars, oos_bars, capital);
    cfg.step_bars = step;
    cfg.risk_free_annual = risk_free;
    cfg.mode = if mode == "anchored" { WalkForwardMode::Anchored } else { WalkForwardMode::Rolling };
    cfg.embargo_bars = embargo_bars;
    cfg.min_oos_trades = min_oos_trades;

    let result = walk_forward_sync(capital, strat, risk, commission, slippage, &bar_vec, symbol, cfg);

    let windows_list = PyList::empty_bound(py);
    for w in &result.windows {
        let d = PyDict::new_bound(py);
        d.set_item("window",    w.window)?;
        d.set_item("is_range",  vec![w.is_range.0,  w.is_range.1])?;
        d.set_item("oos_range", vec![w.oos_range.0, w.oos_range.1])?;
        d.set_item("is_sharpe",            w.in_sample.sharpe_ratio)?;
        d.set_item("is_return_pct",        w.in_sample.total_return_pct)?;
        d.set_item("is_trades",            w.in_sample.total_trades)?;
        d.set_item("is_win_rate_pct",      w.in_sample.win_rate_pct)?;
        d.set_item("is_max_drawdown_pct",  w.in_sample.max_drawdown_pct)?;
        d.set_item("is_profit_factor",     w.in_sample.profit_factor)?;
        d.set_item("is_sqn",               w.in_sample.sqn)?;
        d.set_item("oos_sharpe",           w.out_of_sample.sharpe_ratio)?;
        d.set_item("oos_return_pct",       w.out_of_sample.total_return_pct)?;
        d.set_item("oos_trades",           w.out_of_sample.total_trades)?;
        d.set_item("oos_win_rate_pct",     w.out_of_sample.win_rate_pct)?;
        d.set_item("oos_max_drawdown_pct", w.out_of_sample.max_drawdown_pct)?;
        d.set_item("oos_profit_factor",    w.out_of_sample.profit_factor)?;
        d.set_item("oos_sortino",          w.out_of_sample.sortino_ratio)?;
        d.set_item("oos_calmar",           w.out_of_sample.calmar_ratio)?;
        d.set_item("oos_sqn",              w.out_of_sample.sqn)?;
        d.set_item("oos_psr",              w.out_of_sample.psr)?;
        d.set_item("oos_expectancy",       w.out_of_sample.expectancy)?;
        let curve = PyList::empty_bound(py);
        for pt in &w.oos_equity_curve {
            let p = PyDict::new_bound(py);
            p.set_item("t",      pt.timestamp)?;
            p.set_item("equity", pt.equity)?;
            curve.append(p)?;
        }
        d.set_item("oos_equity_curve", curve)?;
        windows_list.append(d)?;
    }

    let combined_curve = PyList::empty_bound(py);
    for pt in &result.oos_equity_curve {
        let p = PyDict::new_bound(py);
        p.set_item("t",      pt.timestamp)?;
        p.set_item("equity", pt.equity)?;
        combined_curve.append(p)?;
    }

    let efficiency_label = match result.efficiency_ratio {
        r if r >= 0.7 => "robust",
        r if r >= 0.5 => "marginal",
        r if r >  0.0 => "overfit",   // TODO[wf-v2]: misleading without optimisation context
        _             => "no_data",
    };

    let out = PyDict::new_bound(py);
    out.set_item("windows",                windows_list)?;
    out.set_item("avg_oos_sharpe",         result.avg_oos_sharpe)?;
    out.set_item("avg_oos_win_rate",       result.avg_oos_win_rate)?;
    out.set_item("avg_oos_return_pct",     result.avg_oos_return_pct)?;
    out.set_item("total_oos_trades",       result.total_oos_trades)?;
    out.set_item("pct_profitable_windows", result.pct_profitable_windows)?;
    out.set_item("efficiency_ratio",       result.efficiency_ratio)?;
    out.set_item("efficiency_label",       efficiency_label)?;
    out.set_item("oos_equity_curve",       combined_curve)?;
    Ok(out)
    --- end FUTURE[wf-v2] --- */
}

/// Return the names of all built-in strategies available in the factory.
#[pyfunction]
fn list_strategies() -> Vec<&'static str> {
    STRATEGY_KEYS.to_vec()
}

// ── Module registration ───────────────────────────────────────────────────────

/// almanac Python bindings — backtesting engine, Kalman filter, Monte Carlo.
#[pymodule]
fn alm_py(m: &Bound<PyModule>) -> PyResult<()> {
    m.add_function(wrap_pyfunction!(run_backtest, m)?)?;
    m.add_function(wrap_pyfunction!(run_cel_backtest, m)?)?;
    m.add_function(wrap_pyfunction!(run_rhai_backtest, m)?)?;
    m.add_function(wrap_pyfunction!(run_portfolio_backtest, m)?)?;
    m.add_function(wrap_pyfunction!(run_walk_forward, m)?)?;
    m.add_function(wrap_pyfunction!(run_indicators, m)?)?;
    m.add_function(wrap_pyfunction!(buy_hold_benchmark, m)?)?;
    m.add_function(wrap_pyfunction!(kalman, m)?)?;
    m.add_function(wrap_pyfunction!(monte_carlo, m)?)?;
    m.add_function(wrap_pyfunction!(list_strategies, m)?)?;
    Ok(())
}
