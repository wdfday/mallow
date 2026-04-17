"""Tests for alm_py.run_backtest.

Run after:  maturin develop  (or maturin develop --release)
            pytest tests/ -v
"""

import math
import pytest
import alm_py as alm


# ── helpers ───────────────────────────────────────────────────────────────────

REQUIRED_KEYS = {
    "strategy", "symbol",
    "initial_capital", "final_equity",
    "total_return_pct", "cagr_pct", "annualized_volatility_pct",
    "sharpe_ratio", "sortino_ratio", "calmar_ratio",
    "max_drawdown_pct", "max_dd_duration_bars", "avg_drawdown_pct",
    "total_trades", "win_rate_pct", "profit_factor", "expectancy",
    "avg_win_pct", "avg_loss_pct", "avg_trade_duration_hours",
    "max_consecutive_losses",
    "var_95", "cvar_95", "omega_ratio", "tail_ratio", "recovery_factor",
    "rolling_sharpe", "rolling_drawdown",
    "timeframe",
    "equity_curve", "trades", "pnl_pct",
    "trending_pct", "ranging_pct", "neutral_pct",
    "high_vol_pct", "low_vol_pct", "regime_changes",
}


def _run(strategy, bars, params=None, **kwargs):
    return alm.run_backtest("BTCUSDT", strategy, params or {}, bars, **kwargs)


# ── schema ────────────────────────────────────────────────────────────────────

def test_result_has_all_keys(flat_bars):
    r = _run("rsi_mean_rev", flat_bars)
    missing = REQUIRED_KEYS - set(r.keys())
    assert not missing, f"Missing keys: {missing}"


def test_timeframe_detected(flat_bars):
    """conftest generates 1-hour bars → should detect H1."""
    r = _run("rsi_mean_rev", flat_bars)
    assert r["timeframe"] == "H1"


def test_equity_curve_length(flat_bars):
    r = _run("rsi_mean_rev", flat_bars)
    # one equity point per bar
    assert len(r["equity_curve"]) == len(flat_bars["t"])


def test_equity_curve_keys(flat_bars):
    r = _run("rsi_mean_rev", flat_bars)
    pt = r["equity_curve"][0]
    assert "t" in pt and "equity" in pt


# ── no-trade sanity ───────────────────────────────────────────────────────────

def test_no_trades_ratios_are_zero(flat_bars):
    """A strategy that never fires should return 0 for Sharpe/Sortino, not a
    noisy value driven by the risk-free rate on a flat equity curve."""
    # rsi_mean_rev on 'flat_bars' typically generates 0 trades
    r = _run("rsi_mean_rev", flat_bars)
    if r["total_trades"] == 0:
        assert r["sharpe_ratio"] == 0.0
        assert r["sortino_ratio"] == 0.0
        assert r["calmar_ratio"] == 0.0


def test_no_trades_equity_unchanged(flat_bars):
    r = _run("rsi_mean_rev", flat_bars, capital=5_000.0)
    if r["total_trades"] == 0:
        assert r["final_equity"] == pytest.approx(5_000.0, rel=1e-6)
        assert r["total_return_pct"] == pytest.approx(0.0, abs=1e-6)


# ── trending strategy ─────────────────────────────────────────────────────────

def test_trending_strategy_fires_trades(trending_bars):
    r = _run("ma_crossover", trending_bars, {"fast": 10, "slow": 30})
    assert r["total_trades"] >= 1


def test_win_rate_in_range(trending_bars):
    r = _run("ma_crossover", trending_bars, {"fast": 10, "slow": 30})
    if r["total_trades"] > 0:
        assert 0.0 <= r["win_rate_pct"] <= 100.0


def test_profit_factor_non_negative(trending_bars):
    r = _run("ma_crossover", trending_bars, {"fast": 10, "slow": 30})
    assert r["profit_factor"] >= 0.0


def test_max_drawdown_non_negative(trending_bars):
    r = _run("ma_crossover", trending_bars, {"fast": 10, "slow": 30})
    assert r["max_drawdown_pct"] >= 0.0


def test_sharpe_finite_when_trades(trending_bars):
    r = _run("ma_crossover", trending_bars, {"fast": 10, "slow": 30})
    if r["total_trades"] > 0:
        assert math.isfinite(r["sharpe_ratio"])
        assert math.isfinite(r["sortino_ratio"]) or math.isinf(r["sortino_ratio"])


# ── trade list ────────────────────────────────────────────────────────────────

def test_trade_keys(trending_bars):
    r = _run("ma_crossover", trending_bars, {"fast": 5, "slow": 20})
    if r["trades"]:
        t = r["trades"][0]
        for key in ("symbol", "side", "qty", "entry_price", "exit_price",
                    "entry_ts", "exit_ts", "pnl", "pnl_pct"):
            assert key in t, f"trade missing key '{key}'"


def test_pnl_pct_matches_trades(trending_bars):
    r = _run("ma_crossover", trending_bars, {"fast": 5, "slow": 20})
    pnl_from_trades = [t["pnl_pct"] for t in r["trades"]]
    assert r["pnl_pct"] == pnl_from_trades


def test_trade_side_values(trending_bars):
    r = _run("ma_crossover", trending_bars, {"fast": 5, "slow": 20})
    for t in r["trades"]:
        assert t["side"] in ("long", "short")


# ── position sizing ───────────────────────────────────────────────────────────

def test_capital_respected(trending_bars):
    r = _run("ma_crossover", trending_bars, {"fast": 5, "slow": 20}, capital=1_000.0)
    assert r["initial_capital"] == pytest.approx(1_000.0)


def test_final_equity_positive(trending_bars):
    r = _run("ma_crossover", trending_bars, {"fast": 5, "slow": 20})
    assert r["final_equity"] > 0.0


# ── crypto bars ──────────────────────────────────────────────────────────────

def test_crypto_bars_work(crypto_bars):
    r = _run("rsi_mean_rev", crypto_bars, {"period": 14}, capital=50_000.0)
    assert r["initial_capital"] == pytest.approx(50_000.0)
    assert len(r["equity_curve"]) == len(crypto_bars["t"])


# ── list_strategies ──────────────────────────────────────────────────────────

def test_list_strategies_non_empty():
    names = alm.list_strategies()
    assert len(names) > 50
    assert "ma_crossover" in names
    assert "rsi_mean_rev" in names
    assert "cel" in names


# ── kalman ───────────────────────────────────────────────────────────────────

def test_kalman_output_shape(flat_bars):
    prices = flat_bars["c"]
    result = alm.kalman(prices)
    assert "value" in result and "velocity" in result
    assert len(result["value"]) == len(prices)
    assert len(result["velocity"]) == len(prices)


def test_kalman_smoothing(flat_bars):
    """Kalman output should be smoother than raw prices (lower std)."""
    import statistics
    prices = flat_bars["c"]
    kf = alm.kalman(prices)
    assert statistics.stdev(kf["value"]) <= statistics.stdev(prices) * 1.05


# ── monte_carlo ───────────────────────────────────────────────────────────────

def test_monte_carlo_requires_trades():
    with pytest.raises(Exception):
        alm.monte_carlo([], capital=10_000)


def test_monte_carlo_output_keys():
    pnl = [0.01, -0.005, 0.02, -0.01, 0.015] * 10
    mc = alm.monte_carlo(pnl, capital=10_000, n_iter=100)
    assert "ruin_probability" in mc
    assert "final" in mc
    assert "curves" in mc
    assert 0.0 <= mc["ruin_probability"] <= 1.0


def test_monte_carlo_percentile_ordering():
    pnl = [0.01, -0.005, 0.02] * 20
    mc = alm.monte_carlo(pnl, capital=10_000, n_iter=500, seed=42)
    f = mc["final"]
    assert f["p5"] <= f["p25"] <= f["p50"] <= f["p75"] <= f["p95"]


# ── run_indicators ────────────────────────────────────────────────────────────

def test_run_indicators_ema(trending_bars):
    result = alm.run_indicators("BTCUSDT", trending_bars, {"ema20": {"type": "ema", "period": 20}})
    assert "ema20" in result
    series = result["ema20"]["value"]
    assert len(series) == len(trending_bars["t"])
    # First 19 bars are None (warm-up), rest should be floats
    assert all(v is None for v in series[:19])
    assert all(isinstance(v, float) for v in series[19:])


def test_run_indicators_rsi(trending_bars):
    result = alm.run_indicators("BTCUSDT", trending_bars, {"rsi14": {"type": "rsi", "period": 14}})
    rsi = result["rsi14"]["value"]
    non_none = [v for v in rsi if v is not None]
    assert all(0.0 <= v <= 100.0 for v in non_none)
