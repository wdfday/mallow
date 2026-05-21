"""
Hàm dùng chung cho 10 notebook so sánh `alm_py` vs `backtrader`.

- `make_bars(...)`           : sinh OHLCV tổng hợp (dict cho alm_py + DataFrame cho backtrader)
- `bt_run(strategy_cls, ...)`: chạy backtrader với cerebro tiêu chuẩn, trả về dict metric
- `compare_metrics(...)`     : in bảng so sánh + return DataFrame
- `plot_equity(...)`         : vẽ 2 đường equity curve trên cùng 1 trục
"""
from __future__ import annotations

import datetime as dt
import glob
import pathlib
from typing import Iterable

import numpy as np
import pandas as pd
import backtrader as bt


def _skew(a: np.ndarray) -> float:
    n = len(a)
    if n < 3: return float("nan")
    m = a.mean(); s = a.std()
    return float(((a - m) ** 3).mean() / s ** 3) if s > 0 else float("nan")

def _kurtosis(a: np.ndarray) -> float:
    n = len(a)
    if n < 4: return float("nan")
    m = a.mean(); s = a.std()
    return float(((a - m) ** 4).mean() / s ** 4) if s > 0 else float("nan")

# Repo root: /Users/.../mallow/. _shared.py lives in
# almanac/crates/alm-py/notebooks/comparison/ → parents[5] is the repo root.
REPO_ROOT = pathlib.Path(__file__).resolve().parents[5]
DATA_ROOT = REPO_ROOT / "data"


# ─────────────────────────────────────────────────────────────────────────────
# Data generation
# ─────────────────────────────────────────────────────────────────────────────

def make_bars(
    n: int = 500,
    base: float = 100.0,
    trend: float = 0.0005,
    noise: float = 0.005,
    period: float = 0.0,
    seed: int = 42,
    start_ms: int = 1_700_000_000_000,
    bar_ms: int = 3_600_000,            # 1h bars by default
):
    """
    Sinh OHLCV tổng hợp.  Trả về tuple `(alm_bars_dict, bt_dataframe)` để feed
    đồng thời vào hai engine.

    `bt_dataframe` có index là `datetime` (UTC) — backtrader yêu cầu vậy.
    """
    rng = np.random.default_rng(seed)
    closes = np.empty(n)
    p = base
    for i in range(n):
        drift = p * trend
        wiggle = p * noise * rng.standard_normal()
        cycle  = p * 0.01 * np.sin(2 * np.pi * i / period) if period > 0 else 0.0
        p = max(0.5, p + drift + wiggle + cycle)
        closes[i] = p

    # Bar OHLC từ close (open ≈ prev close, high/low ±0.3% noise)
    opens = np.concatenate(([base], closes[:-1]))
    highs = np.maximum(opens, closes) * (1 + np.abs(rng.normal(0.002, 0.001, n)))
    lows  = np.minimum(opens, closes) * (1 - np.abs(rng.normal(0.002, 0.001, n)))
    vols  = rng.uniform(50.0, 200.0, n)

    timestamps_ms = np.arange(n, dtype=np.int64) * bar_ms + start_ms

    alm_bars = {
        "t": timestamps_ms.tolist(),
        "o": opens.tolist(),
        "h": highs.tolist(),
        "l": lows.tolist(),
        "c": closes.tolist(),
        "v": vols.tolist(),
    }

    df = pd.DataFrame({
        "open":   opens,
        "high":   highs,
        "low":    lows,
        "close":  closes,
        "volume": vols,
    }, index=pd.to_datetime(timestamps_ms, unit="ms", utc=True))
    df.index.name = "datetime"
    return alm_bars, df


# ─────────────────────────────────────────────────────────────────────────────
# Real data loader (parquet)
# ─────────────────────────────────────────────────────────────────────────────

def load_parquet(
    symbol: str = "BTCUSDT",
    tf: str = "H1",
    source: str = "Binance",
    n: int | None = None,
    start: str | None = None,
    end: str | None = None,
):
    """
    Load OHLCV thật từ `data/{source}/{tf}/{symbol}/*.parquet`.

    Files đã được hist-data crawler tạo sẵn (xem `mallow/hist-data/`).
    Schema: `t (Unix ms), o, h, l, c, v` (+ `vw, n` được drop).

    Args:
        symbol : 'BTCUSDT', 'ETHUSDT', ... (xem `data/Binance/H1/`)
        tf     : 'M1' | 'M5' | 'M15' | 'M30' | 'H1' | 'H4' | 'D1'
        source : 'Binance' | 'Polygon' | 'VCI'
        n      : nếu set, chỉ lấy `n` bar cuối
        start  : ISO date 'YYYY-MM-DD' lọc theo timestamp
        end    : ISO date 'YYYY-MM-DD' lọc theo timestamp

    Returns:
        (alm_bars: dict, df: pandas.DataFrame) — cùng tuple như `make_bars`.
    """
    pattern = str(DATA_ROOT / source / tf / symbol / f"{symbol}_{tf}_*.parquet")
    files = sorted(glob.glob(pattern))
    if not files:
        raise FileNotFoundError(
            f"No parquet found for {source}/{tf}/{symbol}.\n  pattern: {pattern}\n"
            f"  available symbols: {sorted(p.name for p in (DATA_ROOT/source/tf).iterdir()) if (DATA_ROOT/source/tf).exists() else 'NONE'}"
        )

    df_raw = pd.concat([pd.read_parquet(f) for f in files], ignore_index=True)
    df_raw = df_raw.drop_duplicates(subset="t").sort_values("t").reset_index(drop=True)
    # Drop extra columns (vw, n) if present — alm_py only needs t, o, h, l, c, v.
    keep = ["t", "o", "h", "l", "c", "v"]
    df_raw = df_raw[[c for c in keep if c in df_raw.columns]].copy()

    if start:
        df_raw = df_raw[df_raw["t"] >= int(pd.Timestamp(start, tz="UTC").timestamp() * 1000)]
    if end:
        df_raw = df_raw[df_raw["t"] <  int(pd.Timestamp(end,   tz="UTC").timestamp() * 1000)]
    if n is not None:
        df_raw = df_raw.tail(n).reset_index(drop=True)

    alm_bars = {col: df_raw[col].astype(float).tolist() if col != "t" else df_raw["t"].astype("int64").tolist() for col in keep}

    bt_df = pd.DataFrame({
        "open":   df_raw["o"].astype(float).values,
        "high":   df_raw["h"].astype(float).values,
        "low":    df_raw["l"].astype(float).values,
        "close":  df_raw["c"].astype(float).values,
        "volume": df_raw["v"].astype(float).values,
    }, index=pd.to_datetime(df_raw["t"].values, unit="ms", utc=True))
    bt_df.index.name = "datetime"
    return alm_bars, bt_df


# ─────────────────────────────────────────────────────────────────────────────
# backtrader runner
# ─────────────────────────────────────────────────────────────────────────────

def bt_run(
    strategy_cls,
    df: pd.DataFrame,
    capital: float = 10_000.0,
    commission: float = 0.001,
    slippage: float = 0.0005,
    sizer_pct: float = 95.0,
    strategy_kwargs: dict | None = None,
    timeframe=bt.TimeFrame.Minutes,
    compression: int = 60,
    extra_data: list | None = None,   # list of (df, name) for MTF
):
    """
    Chạy backtrader với cấu hình giống alm_py:
    - capital, commission, slippage tương đương
    - PercentSizer 95% mặc định (tương đương sizing="fixed")
    - Trả về dict gồm: final_equity, total_return_pct, sharpe_ratio,
      max_drawdown_pct, total_trades, win_rate_pct, equity_curve
    """
    cerebro = bt.Cerebro()
    cerebro.broker.setcash(capital)
    cerebro.broker.setcommission(commission=commission)
    cerebro.broker.set_slippage_perc(perc=slippage)
    cerebro.addsizer(bt.sizers.PercentSizer, percents=sizer_pct)

    data = bt.feeds.PandasData(dataname=df, timeframe=timeframe, compression=compression)
    cerebro.adddata(data, name="primary")
    if extra_data:
        for d_df, name in extra_data:
            d = bt.feeds.PandasData(dataname=d_df, timeframe=timeframe, compression=compression)
            cerebro.adddata(d, name=name)

    cerebro.addstrategy(strategy_cls, **(strategy_kwargs or {}))
    cerebro.addanalyzer(bt.analyzers.SharpeRatio, _name="sharpe", riskfreerate=0.0,
                        timeframe=bt.TimeFrame.Days, factor=252, annualize=True)
    cerebro.addanalyzer(bt.analyzers.DrawDown,    _name="dd")
    cerebro.addanalyzer(bt.analyzers.TradeAnalyzer, _name="trades")
    cerebro.addanalyzer(bt.analyzers.TimeReturn,  _name="time_ret",
                        timeframe=bt.TimeFrame.Days)
    cerebro.addanalyzer(bt.analyzers.SQN,         _name="sqn")
    try:
        cerebro.addanalyzer(bt.analyzers.Calmar,  _name="calmar")
        _has_calmar = True
    except Exception:
        _has_calmar = False

    # Record individual trade entry/exit timestamps for signal comparison chart.
    class _TradeRec(bt.Analyzer):
        def start(self):
            self.recs = []
        def notify_trade(self, trade):
            if trade.isclosed:
                self.recs.append({
                    "entry_dt": pd.Timestamp(bt.num2date(trade.dtopen)).tz_localize("UTC"),
                    "exit_dt":  pd.Timestamp(bt.num2date(trade.dtclose)).tz_localize("UTC"),
                    "pnl":      float(trade.pnl),
                })
    cerebro.addanalyzer(_TradeRec, _name="trade_rec")

    # Track equity curve
    class EquityObserver(bt.Observer):
        lines = ("equity",)
        plotinfo = dict(plot=False)
        def next(self):
            self.lines.equity[0] = self._owner.broker.getvalue()
    cerebro.addobserver(EquityObserver)

    results = cerebro.run()
    strat = results[0]
    final = cerebro.broker.getvalue()
    sharpe   = strat.analyzers.sharpe.get_analysis().get("sharperatio") or 0.0
    dd       = strat.analyzers.dd.get_analysis()
    trades   = strat.analyzers.trades.get_analysis()
    total_trades = trades.get("total", {}).get("closed", 0) or 0
    won      = trades.get("won",  {}).get("total", 0) or 0
    win_rate = (won / total_trades * 100.0) if total_trades else 0.0

    # Pull equity curve from observer
    obs = strat.observers[-1]
    eq_line = list(obs.lines.equity.array)
    eq = [v if (v == v and v != 0) else capital for v in eq_line][-len(df):]

    # ── Per-trade stats from TradeAnalyzer ───────────────────────────────────
    won_pnl  = trades.get("won",  {}).get("pnl", {})
    lost_pnl = trades.get("lost", {}).get("pnl", {})
    avg_win_pnl  = float(won_pnl.get("average", 0.0)  or 0.0)
    avg_loss_pnl = float(lost_pnl.get("average", 0.0) or 0.0)
    max_win_pnl  = float(won_pnl.get("max", 0.0)  or 0.0)
    max_loss_pnl = float(lost_pnl.get("max", 0.0) or 0.0)   # least-negative
    total_won_pnl    = float(won_pnl.get("total",  0.0) or 0.0)
    total_lost_pnl   = abs(float(lost_pnl.get("total", 0.0) or 0.0))
    profit_factor    = total_won_pnl / total_lost_pnl if total_lost_pnl > 0 else float("inf")
    expectancy       = ((win_rate / 100.0) * avg_win_pnl
                        + (1 - win_rate / 100.0) * avg_loss_pnl) if total_trades else float("nan")
    max_consec_wins  = trades.get("streak", {}).get("won",  {}).get("longest", 0) or 0
    max_consec_losses= trades.get("streak", {}).get("lost", {}).get("longest", 0) or 0

    # ── Drawdown extras ───────────────────────────────────────────────────────
    max_dd_pct      = float(dd.get("max", {}).get("drawdown", 0.0))
    max_dd_dur_bars = int(dd.get("max", {}).get("len", 0) or 0)

    # ── Daily returns from TimeReturn ─────────────────────────────────────────
    time_ret_raw = strat.analyzers.time_ret.get_analysis()
    day_rets = np.array(list(time_ret_raw.values()), float) if time_ret_raw else np.array([])
    neg_day  = day_rets[day_rets < 0]
    pos_day  = day_rets[day_rets > 0]

    sortino  = (float(day_rets.mean() / neg_day.std() * np.sqrt(252))
                if len(neg_day) > 0 else float("nan"))
    ann_vol  = float(day_rets.std() * np.sqrt(252) * 100) if len(day_rets) > 1 else float("nan")
    skewness = float(_skew(day_rets))         if len(day_rets) > 3 else float("nan")
    ex_kurt  = float(_kurtosis(day_rets) - 3) if len(day_rets) > 3 else float("nan")
    omega    = (float(pos_day.sum() / abs(neg_day.sum()))
                if len(neg_day) > 0 and neg_day.sum() != 0 else float("inf"))
    p95      = float(np.percentile(day_rets, 95))  if len(day_rets) else float("nan")
    p05      = float(np.percentile(day_rets, 5))   if len(day_rets) else float("nan")
    tail_ratio = float(abs(p95) / abs(p05)) if (len(day_rets) and p05 != 0) else float("nan")
    var_95   = p05
    cvar_95  = float(day_rets[day_rets <= p05].mean()) if len(day_rets[day_rets <= p05]) else float("nan")

    # ── CAGR ─────────────────────────────────────────────────────────────────
    years = (df.index[-1] - df.index[0]).days / 365.25
    cagr  = ((final / capital) ** (1.0 / years) - 1.0) * 100.0 if years > 0 else float("nan")

    # ── Recovery factor ───────────────────────────────────────────────────────
    total_ret_pct = (final - capital) / capital * 100.0
    recovery_factor = total_ret_pct / max_dd_pct if max_dd_pct > 0 else float("inf")

    # ── SQN ──────────────────────────────────────────────────────────────────
    sqn_raw = strat.analyzers.sqn.get_analysis()
    sqn     = float(sqn_raw.get("sqn", float("nan")) or float("nan"))

    # ── Calmar ────────────────────────────────────────────────────────────────
    calmar = float("nan")
    if _has_calmar:
        try:
            cal_dict = strat.analyzers.calmar.get_analysis()
            calmar = float(list(cal_dict.values())[-1]) if cal_dict else float("nan")
        except Exception:
            pass
    if (isinstance(calmar, float) and (np.isnan(calmar) or np.isinf(calmar))) and max_dd_pct > 0:
        calmar = cagr / max_dd_pct  # fallback

    # ── avg trade duration ───────────────────────────────────────────────────
    len_won  = trades.get("won",  {}).get("len", {}).get("average", float("nan")) or float("nan")
    len_lost = trades.get("lost", {}).get("len", {}).get("average", float("nan")) or float("nan")
    if not np.isnan(len_won) and not np.isnan(len_lost) and total_trades:
        avg_len_bars = (len_won * won + len_lost * (total_trades - won)) / total_trades
    elif not np.isnan(len_won):
        avg_len_bars = len_won
    else:
        avg_len_bars = float("nan")
    # Convert bars to hours using compression (minutes per bar → hours)
    mins_per_bar = compression
    avg_duration_h = avg_len_bars * mins_per_bar / 60.0 if not np.isnan(avg_len_bars) else float("nan")

    # ── per-trade pnl from recs (for $ avg and chart) ────────────────────────
    recs     = strat.analyzers.trade_rec.recs
    pnl_arr  = np.array([t["pnl"] for t in recs], float)
    wins_arr = pnl_arr[pnl_arr > 0]; loss_arr = pnl_arr[pnl_arr <= 0]

    return {
        "final_equity":             float(final),
        "total_return_pct":         total_ret_pct,
        "cagr_pct":                 cagr,
        "annualized_volatility_pct": ann_vol,
        "sharpe_ratio":             float(sharpe),
        "sortino_ratio":            sortino,
        "calmar_ratio":             calmar,
        "omega_ratio":              omega,
        "tail_ratio":               tail_ratio,
        "recovery_factor":          recovery_factor,
        "max_drawdown_pct":         max_dd_pct,
        "avg_drawdown_pct":         float("nan"),   # not available from bt
        "max_dd_duration_bars":     max_dd_dur_bars,
        "total_trades":             int(total_trades),
        "win_rate_pct":             float(win_rate),
        "profit_factor":            profit_factor,
        "expectancy":               expectancy,
        "avg_win_pnl":              avg_win_pnl,
        "avg_loss_pnl":             avg_loss_pnl,
        "avg_win":                  float(wins_arr.mean()) if len(wins_arr) else float("nan"),
        "avg_loss":                 float(loss_arr.mean()) if len(loss_arr) else float("nan"),
        "largest_win_pnl":          max_win_pnl,
        "largest_loss_pnl":         max_loss_pnl,
        "avg_trade_duration_hours": avg_duration_h,
        "max_consecutive_wins":     int(max_consec_wins),
        "max_consecutive_losses":   int(max_consec_losses),
        "var_95":                   var_95,
        "cvar_95":                  cvar_95,
        "skewness":                 skewness,
        "excess_kurtosis":          ex_kurt,
        "sqn":                      sqn,
        "equity_curve":             eq,
        "trades":                   recs,
    }


# ─────────────────────────────────────────────────────────────────────────────
# Comparison helpers
# ─────────────────────────────────────────────────────────────────────────────

ALM_KEYS = [
    "final_equity", "total_return_pct", "sharpe_ratio",
    "max_drawdown_pct", "total_trades", "win_rate_pct",
]

def compare_metrics(alm_result: dict, bt_result: dict) -> pd.DataFrame:
    """Bảng so sánh side-by-side các metric chính."""
    rows = []
    for k in ALM_KEYS:
        a = alm_result.get(k, float("nan"))
        b = bt_result.get(k,  float("nan"))
        diff = a - b if isinstance(a, (int, float)) and isinstance(b, (int, float)) else None
        rows.append({"metric": k, "alm_py": a, "backtrader": b, "diff (alm − bt)": diff})
    return pd.DataFrame(rows).set_index("metric")


def _extract_equity_values(equity_curve) -> list:
    """Normalise equity_curve to a plain list of floats.
    Accepts both alm_py format (list of {"t":…,"equity":…} dicts)
    and backtrader format (plain list of floats)."""
    if not equity_curve:
        return []
    if isinstance(equity_curve[0], dict):
        return [pt["equity"] for pt in equity_curve]
    return list(equity_curve)


def plot_equity(result_a: dict, result_b: dict, df: pd.DataFrame,
                title: str = "Equity curve",
                label_a: str = "alm_py (named)",
                label_b: str = "alm_py (rhai) / backtrader"):
    """Vẽ 2 đường equity trên cùng trục.
    Chấp nhận cả alm_py format {"equity_curve": [{"t":…,"equity":…}]}
    lẫn backtrader format {"equity_curve": [float, …]}."""
    import matplotlib.pyplot as plt
    fig, ax = plt.subplots(figsize=(11, 4))

    eq_a = _extract_equity_values(result_a.get("equity_curve", []))
    if eq_a:
        eq_a_df = pd.DataFrame(result_a["equity_curve"]) if isinstance(result_a["equity_curve"][0], dict) else None
        if eq_a_df is not None:
            eq_a_df["dt"] = pd.to_datetime(eq_a_df["t"], unit="ms", utc=True)
            ax.plot(eq_a_df["dt"], eq_a_df["equity"], label=label_a, linewidth=1.5)
        else:
            ax.plot(df.index[-len(eq_a):], eq_a, label=label_a, linewidth=1.5)

    eq_b = _extract_equity_values(result_b.get("equity_curve", []))
    if eq_b:
        eq_b_src = result_b.get("equity_curve", [])
        if eq_b_src and isinstance(eq_b_src[0], dict):
            eq_b_df = pd.DataFrame(eq_b_src)
            eq_b_df["dt"] = pd.to_datetime(eq_b_df["t"], unit="ms", utc=True)
            ax.plot(eq_b_df["dt"], eq_b_df["equity"], label=label_b, linewidth=1.5, alpha=0.8)
        else:
            ax.plot(df.index[-len(eq_b):], eq_b, label=label_b, linewidth=1.5, alpha=0.8)

    ax.set_title(title)
    ax.set_ylabel("Equity")
    ax.legend()
    ax.grid(alpha=0.3)
    fig.tight_layout()
    return fig


# ─────────────────────────────────────────────────────────────────────────────
# vectorbt runner
# ─────────────────────────────────────────────────────────────────────────────

def vbt_run(
    entries,        # boolean list/array, same length as closes
    exits,          # boolean list/array
    closes,         # list of close prices
    capital: float = 10_000.0,
    commission: float = 0.001,
    slippage: float = 0.0005,
    freq: str = "1h",
):
    """
    Run a vectorbt portfolio from pre-computed boolean entry/exit signals.
    Returns the same key dict format as bt_run for easy compare_metrics() use.
    """
    import numpy as np
    import vectorbt as vbt

    c = np.ascontiguousarray(closes, dtype=float)
    e = np.ascontiguousarray(entries, dtype=bool)
    x = np.ascontiguousarray(exits,   dtype=bool)

    pf = vbt.Portfolio.from_signals(
        c, entries=e, exits=x,
        init_cash=capital,
        fees=commission,
        freq=freq,
        direction="longonly",
    )

    def _try(fn, default=float("nan")):
        try:
            v = fn()
            return float(v) if v == v else default
        except Exception:
            return default

    tr      = pf.trades
    total   = int(tr.count())
    pnl_arr = tr.pnl.values if total > 0 else np.array([], dtype=float)
    won_arr  = pnl_arr[pnl_arr > 0]
    loss_arr = pnl_arr[pnl_arr <= 0]

    # per-trade returns (%) for win/loss % stats
    tr_rets = tr.returns.values if total > 0 else np.array([], dtype=float)
    tr_rets_w = tr_rets[tr_rets > 0]
    tr_rets_l = tr_rets[tr_rets <= 0]

    sum_wins     = float(won_arr.sum())       if len(won_arr)  else 0.0
    sum_loss_abs = float(abs(loss_arr.sum())) if len(loss_arr) else 0.0
    profit_factor = sum_wins / sum_loss_abs if sum_loss_abs > 0 else float("inf")

    win_rate = float(len(won_arr) / total * 100) if total else 0.0
    avg_win_pnl  = float(won_arr.mean())  if len(won_arr)  else float("nan")
    avg_loss_pnl = float(loss_arr.mean()) if len(loss_arr) else float("nan")
    avg_win_pct  = float(tr_rets_w.mean() * 100) if len(tr_rets_w) else float("nan")
    avg_loss_pct = float(tr_rets_l.mean() * 100) if len(tr_rets_l) else float("nan")
    largest_win_pct  = float(tr_rets.max() * 100) if len(tr_rets) else float("nan")
    largest_loss_pct = float(tr_rets.min() * 100) if len(tr_rets) else float("nan")

    # expectancy in $
    expectancy = (win_rate / 100 * avg_win_pnl + (1 - win_rate / 100) * avg_loss_pnl
                  ) if total else float("nan")

    # SQN
    sqn = (float(np.sqrt(total) * pnl_arr.mean() / pnl_arr.std())
           if total > 1 and pnl_arr.std() > 0 else float("nan"))

    # max consecutive wins/losses (from streak arrays)
    max_consec_wins   = int(tr.winning_streak.values.max()) if total > 0 else 0
    max_consec_losses = int(tr.losing_streak.values.max())  if total > 0 else 0

    # avg trade duration (bars → hours)
    freq_hours = _try(lambda: float(pd.tseries.frequencies.to_offset(freq).nanos) / 3_600_000_000_000)
    avg_dur_bars = _try(lambda: float(tr.duration.mean()))
    avg_dur_h = avg_dur_bars * freq_hours if not (avg_dur_bars != avg_dur_bars) else float("nan")

    # bar-level returns for distribution stats
    eq_vals = pf.value().values
    bar_rets = np.diff(eq_vals) / np.where(eq_vals[:-1] != 0, eq_vals[:-1], np.nan)
    bar_rets = bar_rets[~np.isnan(bar_rets)]

    var_95  = float(np.percentile(bar_rets, 5))  if len(bar_rets) else float("nan")
    cvar_95 = float(bar_rets[bar_rets <= var_95].mean()) if len(bar_rets[bar_rets <= var_95]) else float("nan")
    skewness    = _skew(bar_rets)
    ex_kurtosis = _kurtosis(bar_rets) - 3.0 if not (isinstance(_kurtosis(bar_rets), float) and
                                                      _kurtosis(bar_rets) != _kurtosis(bar_rets)) else float("nan")

    # max drawdown duration
    try:
        dd_dur = pf.drawdowns
        max_dd_dur_bars = int(dd_dur.duration.max()) if dd_dur.count() > 0 else 0
    except Exception:
        max_dd_dur_bars = 0

    # avg drawdown
    try:
        avg_dd_pct = abs(_try(lambda: float(pf.drawdowns.drawdown.mean()) * 100))
    except Exception:
        avg_dd_pct = float("nan")

    total_ret_pct = _try(lambda: pf.total_return() * 100)
    max_dd_pct    = abs(_try(lambda: pf.max_drawdown() * 100))
    recovery      = total_ret_pct / max_dd_pct if max_dd_pct > 0 else float("inf")
    cagr_pct      = _try(lambda: pf.annualized_return() * 100)
    ann_vol_pct   = _try(lambda: pf.annualized_volatility() * 100)
    calmar        = _try(lambda: pf.calmar_ratio())
    omega         = _try(lambda: pf.omega_ratio())
    tail_ratio    = _try(lambda: pf.tail_ratio())
    sortino       = _try(lambda: pf.sortino_ratio())

    return {
        "final_equity":             _try(pf.final_value),
        "total_return_pct":         total_ret_pct,
        "cagr_pct":                 cagr_pct,
        "annualized_volatility_pct": ann_vol_pct,
        "sharpe_ratio":             _try(pf.sharpe_ratio),
        "sortino_ratio":            sortino,
        "calmar_ratio":             calmar,
        "omega_ratio":              omega,
        "tail_ratio":               tail_ratio,
        "recovery_factor":          recovery,
        "max_drawdown_pct":         max_dd_pct,
        "avg_drawdown_pct":         avg_dd_pct,
        "max_dd_duration_bars":     max_dd_dur_bars,
        "total_trades":             total,
        "win_rate_pct":             win_rate,
        "profit_factor":            profit_factor,
        "expectancy":               expectancy,
        "avg_win_pct":              avg_win_pct,
        "avg_loss_pct":             avg_loss_pct,
        "avg_win":                  avg_win_pnl,
        "avg_loss":                 avg_loss_pnl,
        "largest_win_pct":          largest_win_pct,
        "largest_loss_pct":         largest_loss_pct,
        "avg_trade_duration_hours": avg_dur_h,
        "max_consecutive_wins":     max_consec_wins,
        "max_consecutive_losses":   max_consec_losses,
        "var_95":                   var_95,
        "cvar_95":                  cvar_95,
        "skewness":                 skewness,
        "excess_kurtosis":          ex_kurtosis,
        "sqn":                      sqn,
        "equity_curve":             eq_vals.tolist(),
    }
