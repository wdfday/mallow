"""
Generator: tạo 68 notebook so sánh alm_py vs pandas_ta (+vectorbt) cho mỗi strategy.

Chạy:
    cd almanac/crates/alm-py/notebooks/comparison
    python _generate_cmp.py

Output: cmp_<strategy_name>.ipynb (68 files) trong cùng thư mục.
"""
from __future__ import annotations
import json, pathlib, sys, textwrap

HERE = pathlib.Path(__file__).parent

# ─── Rhai script translations ──────────────────────────────────────────────────
# Source of truth: tests/test_script_parity.py  TRANSLATION_ROWS
# Maps strategy_name → rhai_script string (only for translatable strategies).
_TESTS_DIR = str(HERE.parent.parent / "tests")
if _TESTS_DIR not in sys.path:
    sys.path.insert(0, _TESTS_DIR)
try:
    from test_script_parity import TRANSLATION_ROWS as _TR
    RHAI_SCRIPTS: dict[str, str] = {name: script for name, _, script in _TR}
except ImportError:
    RHAI_SCRIPTS = {}
    print("[warn] test_script_parity.py not found — script cells will be skipped")

# ─── Notebook helpers ──────────────────────────────────────────────────────────

def md(*lines):
    return {"cell_type": "markdown", "metadata": {}, "source": "\n".join(lines)}

def code(*lines):
    return {"cell_type": "code", "metadata": {}, "execution_count": None, "outputs": [], "source": "\n".join(lines)}

def nb(*cells):
    return {
        "cells": list(cells),
        "metadata": {
            "kernelspec": {"display_name": "Python 3 (alm-py venv)", "language": "python", "name": "python3"},
            "language_info": {"name": "python", "pygments_lexer": "ipython3"},
        },
        "nbformat": 4,
        "nbformat_minor": 5,
    }

# ─── Strategy definitions ──────────────────────────────────────────────────────
# Each entry:
#   params: dict  → default params passed to alm_py.run_backtest
#   description: str
#   pta_code: str → pandas_ta-based signal computation (uses close, high, low, vol as Series)
#                   must set `entries` and `exits` as bool arrays/Series
#   bt_code: str | None → backtrader strategy class (class BtStrat(bt.Strategy))

STRATEGY_IMPLS: dict = {

    # ── MA / EMA ──────────────────────────────────────────────────────────────

    "ma_crossover": {
        "params": {"fast": 20, "slow": 50},
        "description": "EMA fast/slow crossover. Long on cross-above, exit on cross-below.",
        "pta_code": """\
fast = ta.ema(close, length={fast})
slow = ta.ema(close, length={slow})
entries = cross_above(fast, slow)
exits   = cross_below(fast, slow)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        fast = bt.indicators.EMA(self.data.close, period={fast})
        slow = bt.indicators.EMA(self.data.close, period={slow})
        self.cross = bt.indicators.CrossOver(fast, slow)
    def next(self):
        if not self.position and self.cross > 0: self.buy()
        elif self.position and self.cross < 0:   self.close()
""",
    },

    "triple_ema": {
        "params": {"ema1": 10, "ema2": 20, "ema3": 50},
        "description": "Triple EMA: all three EMAs aligned bullish → long; any inversion → exit.",
        "pta_code": """\
e1 = ta.ema(close, length={ema1})
e2 = ta.ema(close, length={ema2})
e3 = ta.ema(close, length={ema3})
entries = (e1 > e2) & (e2 > e3)
exits   = (e1 < e2) | (e2 < e3)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.e1 = bt.indicators.EMA(self.data.close, period={ema1})
        self.e2 = bt.indicators.EMA(self.data.close, period={ema2})
        self.e3 = bt.indicators.EMA(self.data.close, period={ema3})
    def next(self):
        bull = self.e1[0] > self.e2[0] > self.e3[0]
        if not self.position and bull:    self.buy()
        elif self.position and not bull:  self.close()
""",
    },

    "hma_crossover": {
        "params": {"fast": 16, "slow": 49},
        "description": "HMA fast/slow crossover. Long on cross-above, exit on cross-below.",
        "pta_code": """\
fast = ta.hma(close, length={fast})
slow = ta.hma(close, length={slow})
entries = cross_above(fast, slow)
exits   = cross_below(fast, slow)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        fast = bt.indicators.HMA(self.data.close, period={fast})
        slow = bt.indicators.HMA(self.data.close, period={slow})
        self.cross = bt.indicators.CrossOver(fast, slow)
    def next(self):
        if not self.position and self.cross > 0: self.buy()
        elif self.position and self.cross < 0:   self.close()
""",
    },

    "dema_crossover": {
        "params": {"fast": 12, "slow": 26},
        "description": "DEMA fast/slow crossover. Long on cross-above, exit on cross-below.",
        "pta_code": """\
fast = ta.dema(close, length={fast})
slow = ta.dema(close, length={slow})
entries = cross_above(fast, slow)
exits   = cross_below(fast, slow)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        fast = bt.indicators.DEMA(self.data.close, period={fast})
        slow = bt.indicators.DEMA(self.data.close, period={slow})
        self.cross = bt.indicators.CrossOver(fast, slow)
    def next(self):
        if not self.position and self.cross > 0: self.buy()
        elif self.position and self.cross < 0:   self.close()
""",
    },

    "tema_crossover": {
        "params": {"fast": 8, "slow": 21},
        "description": "TEMA fast/slow crossover. Long on cross-above, exit on cross-below.",
        "pta_code": """\
fast = ta.tema(close, length={fast})
slow = ta.tema(close, length={slow})
entries = cross_above(fast, slow)
exits   = cross_below(fast, slow)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        fast = bt.indicators.TEMA(self.data.close, period={fast})
        slow = bt.indicators.TEMA(self.data.close, period={slow})
        self.cross = bt.indicators.CrossOver(fast, slow)
    def next(self):
        if not self.position and self.cross > 0: self.buy()
        elif self.position and self.cross < 0:   self.close()
""",
    },

    "lsma_cross": {
        "params": {"fast": 20, "slow": 50},
        "description": "LSMA (Linear Regression MA) crossover.",
        "pta_code": """\
fast = ta.linreg(close, length={fast})
slow = ta.linreg(close, length={slow})
entries = cross_above(fast, slow)
exits   = cross_below(fast, slow)
""",
        "bt_code": None,
    },

    "alma_cross": {
        "params": {"fast": 9, "slow": 21, "offset": 0.85, "sigma": 6.0},
        "description": "ALMA fast/slow crossover.",
        "pta_code": """\
fast = ta.alma(close, length={fast}, sigma={sigma}, distribution_offset={offset})
slow = ta.alma(close, length={slow}, sigma={sigma}, distribution_offset={offset})
entries = cross_above(fast, slow)
exits   = cross_below(fast, slow)
""",
        "bt_code": None,
    },

    "gmma_crossover": {
        "params": {},
        "description": "GMMA: short group (3,5,8,10,12,15) vs long group (30,35,40,45,50,60). Long when short avg > long avg, exit when reversed.",
        "pta_code": """\
short_periods = [3, 5, 8, 10, 12, 15]
long_periods  = [30, 35, 40, 45, 50, 60]
short_avg = pd.concat([close.ewm(span=p, adjust=False).mean() for p in short_periods], axis=1).mean(axis=1)
long_avg  = pd.concat([close.ewm(span=p, adjust=False).mean() for p in long_periods], axis=1).mean(axis=1)
entries = cross_above(short_avg, long_avg)
exits   = cross_below(short_avg, long_avg)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        short_emas = [bt.indicators.EMA(self.data.close, period=p) for p in [3,5,8,10,12,15]]
        long_emas  = [bt.indicators.EMA(self.data.close, period=p) for p in [30,35,40,45,50,60]]
        # Average of groups via sum / count
        self.short_sum = bt.indicators.SumN(bt.LineSeries(*short_emas), period=1) if False else None
        self._short = short_emas
        self._long  = long_emas
    def next(self):
        short_avg = sum(e[0] for e in self._short) / 6
        long_avg  = sum(e[0] for e in self._long)  / 6
        short_prev = sum(e[-1] for e in self._short) / 6
        long_prev  = sum(e[-1] for e in self._long)  / 6
        if not self.position and short_prev <= long_prev and short_avg > long_avg: self.buy()
        elif self.position and short_prev >= long_prev and short_avg < long_avg:   self.close()
""",
    },

    "ma_pullback": {
        "params": {"ma_period": 50},
        "description": "MA pullback: buy when price pulls back to MA from above, exit when price drops below MA.",
        "pta_code": """\
ma = ta.sma(close, length={ma_period})
proximity = (close - ma) / ma
near_ma = pd.Series(False, index=close.index)
entries = pd.Series(False, index=close.index)
in_pos = False
was_near = False
for i in range(len(close)):
    c_val = close.iloc[i]
    ma_val = ma.iloc[i]
    if pd.isna(ma_val): continue
    if in_pos:
        if c_val < ma_val:
            in_pos = False
            was_near = False
    else:
        if c_val < ma_val:
            was_near = False
        else:
            prox = (c_val - ma_val) / ma_val
            if prox <= 0.02:
                was_near = True
            elif was_near and prox > 0.02:
                was_near = False
                in_pos = True
                entries.iloc[i] = True
exits = close < ma
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.ma = bt.indicators.SMA(self.data.close, period={ma_period})
        self.near_ma = False
    def next(self):
        if self.position:
            if self.data.close[0] < self.ma[0]:
                self.close()
                self.near_ma = False
        else:
            if self.data.close[0] < self.ma[0]:
                self.near_ma = False
            else:
                proximity = (self.data.close[0] - self.ma[0]) / self.ma[0]
                if proximity <= 0.02:
                    self.near_ma = True
                elif self.near_ma and proximity > 0.02:
                    self.buy()
                    self.near_ma = False
""",
    },

    "scalping_ema": {
        "params": {"fast": 8, "slow": 21, "atr_period": 14, "atr_ma_period": 20},
        "description": "Fast EMA/Slow EMA crossover with ATR volatility filter.",
        "pta_code": """\
fast_ema = ta.ema(close, length={fast})
slow_ema = ta.ema(close, length={slow})
atr      = ta.atr(high, low, close, length={atr_period})
atr_ma   = ta.sma(atr, length={atr_ma_period})
entries = cross_above(fast_ema, slow_ema) & (atr > atr_ma)
exits   = cross_below(fast_ema, slow_ema)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.fast = bt.indicators.EMA(self.data.close, period={fast})
        self.slow = bt.indicators.EMA(self.data.close, period={slow})
        self.atr  = bt.indicators.ATR(self.data, period={atr_period})
        self.atr_ma = bt.indicators.SMA(self.atr, period={atr_ma_period})
    def next(self):
        cross_up = self.fast[-1] <= self.slow[-1] and self.fast[0] > self.slow[0]
        cross_dn = self.fast[-1] >= self.slow[-1] and self.fast[0] < self.slow[0]
        if not self.position and cross_up and self.atr[0] > self.atr_ma[0]: self.buy()
        elif self.position and cross_dn: self.close()
""",
    },

    # ── RSI ───────────────────────────────────────────────────────────────────

    "rsi_mean_rev": {
        "params": {"period": 14, "oversold": 30.0, "overbought": 70.0},
        "description": "RSI mean reversion. Long when RSI < oversold, exit when RSI > overbought.",
        "pta_code": """\
rsi     = ta.rsi(close, length={period})
entries = rsi < {oversold}
exits   = rsi > {overbought}
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.rsi = bt.indicators.RSI(self.data.close, period={period})
    def next(self):
        if not self.position and self.rsi < {oversold}: self.buy()
        elif self.position and self.rsi > {overbought}: self.close()
""",
    },

    "rsi_ma_cross": {
        "params": {"fast": 20, "slow": 50, "rsi_period": 14, "rsi_entry": 50.0, "rsi_exit": 45.0},
        "description": "EMA crossover + RSI confirmation. Long when fast>slow AND RSI>entry_level, exit when RSI<exit_level.",
        "pta_code": """\
fast = ta.ema(close, length={fast})
slow = ta.ema(close, length={slow})
rsi  = ta.rsi(close, length={rsi_period})
entries = cross_above(fast, slow) & (rsi > {rsi_entry})
exits   = cross_below(fast, slow) | (rsi < {rsi_exit})
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.fast = bt.indicators.EMA(self.data.close, period={fast})
        self.slow = bt.indicators.EMA(self.data.close, period={slow})
        self.rsi  = bt.indicators.RSI(self.data.close, period={rsi_period})
        self.cross = bt.indicators.CrossOver(self.fast, self.slow)
    def next(self):
        if not self.position and self.cross > 0 and self.rsi[0] > {rsi_entry}: self.buy()
        elif self.position and (self.cross < 0 or self.rsi[0] < {rsi_exit}): self.close()
""",
    },

    # ── MACD ──────────────────────────────────────────────────────────────────

    "macd_crossover": {
        "params": {"fast": 12, "slow": 26, "signal": 9},
        "description": "MACD histogram crosses above 0 → long; below 0 → exit.",
        "pta_code": """\
macd_df = ta.macd(close, fast={fast}, slow={slow}, signal={signal})
hist = macd_df[f'MACDh_{{{fast}}}_{{{slow}}}_{{{signal}}}']
entries = cross_above(hist, pd.Series(0.0, index=hist.index))
exits   = cross_below(hist, pd.Series(0.0, index=hist.index))
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        macd = bt.indicators.MACDHisto(self.data.close, period_me1={fast}, period_me2={slow}, period_signal={signal})
        self.hist = macd.lines.histo
    def next(self):
        if not self.position and self.hist[-1] <= 0 and self.hist[0] > 0: self.buy()
        elif self.position and self.hist[-1] >= 0 and self.hist[0] < 0:   self.close()
""",
    },

    "macd_ma": {
        "params": {"fast": 12, "slow": 26, "signal": 9, "ma": 50},
        "description": "MACD + MA trend filter. Long when MACD hist > 0 AND price > MA, exit when hist < 0.",
        "pta_code": """\
macd_df = ta.macd(close, fast={fast}, slow={slow}, signal={signal})
hist = macd_df[f'MACDh_{{{fast}}}_{{{slow}}}_{{{signal}}}']
ma   = ta.sma(close, length={ma})
entries = (hist > 0) & (close > ma)
exits   = hist < 0
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        macd     = bt.indicators.MACDHisto(self.data.close, period_me1={fast}, period_me2={slow}, period_signal={signal})
        self.hist = macd.lines.histo
        self.ma   = bt.indicators.SMA(self.data.close, period={ma})
    def next(self):
        if not self.position and self.hist > 0 and self.data.close > self.ma: self.buy()
        elif self.position and self.hist < 0: self.close()
""",
    },

    # ── Stochastic ────────────────────────────────────────────────────────────

    "stochastic_crossover": {
        "params": {"k_period": 14, "d_period": 3, "oversold": 20.0, "overbought": 80.0},
        "description": "Stochastic %K crosses above %D in oversold zone → long; crosses below in overbought → exit.",
        "pta_code": """\
stoch   = ta.stoch(high, low, close, k={k_period}, d={d_period}, smooth_k=1)
k = stoch[f'STOCHk_{{{k_period}}}_{{{d_period}}}_1']
d = stoch[f'STOCHd_{{{k_period}}}_{{{d_period}}}_1']
entries = cross_above(k, d) & (d < {oversold})
exits   = cross_below(k, d) & (d > {overbought})
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        stoch = bt.indicators.StochasticFull(self.data, period={k_period}, period_dfast=1, period_dslow={d_period})
        self.k = stoch.percK
        self.d = stoch.percDSlow
    def next(self):
        if not self.position and self.k[-1]<=self.d[-1] and self.k[0]>self.d[0] and self.d[0]<{oversold}: self.buy()
        elif self.position and self.k[-1]>=self.d[-1] and self.k[0]<self.d[0] and self.d[0]>{overbought}: self.close()
""",
    },

    "stochastic_dk": {
        "params": {"k": 14, "d": 3},
        "description": "Pure stochastic %K/%D crossover (no zone filter).",
        "pta_code": """\
stoch   = ta.stoch(high, low, close, k={k}, d={d}, smooth_k=1)
k = stoch[f'STOCHk_{{{k}}}_{{{d}}}_1']
d = stoch[f'STOCHd_{{{k}}}_{{{d}}}_1']
entries = cross_above(k, d)
exits   = cross_below(k, d)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        stoch = bt.indicators.StochasticFull(self.data, period={k}, period_dfast=1, period_dslow={d})
        self.k = stoch.percK
        self.d = stoch.percDSlow
    def next(self):
        if not self.position and self.k[-1]<=self.d[-1] and self.k[0]>self.d[0]: self.buy()
        elif self.position and self.k[-1]>=self.d[-1] and self.k[0]<self.d[0]:   self.close()
""",
    },

    "range_rover": {
        "params": {"k": 14, "d": 3, "ma": 50, "oversold": 20.0, "overbought": 80.0},
        "description": "Stochastic %K oversold + price above SMA → long; %K overbought → exit.",
        "pta_code": """\
stoch = ta.stoch(high, low, close, k={k}, d={d}, smooth_k=1)
k_col = f'STOCHk_{{{k}}}_{{{d}}}_1'
k  = stoch[k_col]
ma = ta.sma(close, length={ma})
entries = (k < {oversold}) & (close > ma)
exits   = k > {overbought}
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        stoch    = bt.indicators.StochasticFull(self.data, period={k}, period_dfast=1, period_dslow={d})
        self.k   = stoch.percK
        self.ma  = bt.indicators.SMA(self.data.close, period={ma})
    def next(self):
        if not self.position and self.k < {oversold} and self.data.close > self.ma: self.buy()
        elif self.position and self.k > {overbought}: self.close()
""",
    },

    "reversal_catcher": {
        "params": {"k": 14, "d": 3, "rsi_period": 14},
        "description": "Stochastic K cross above D AND RSI < 50 → long; K cross below D or RSI > 70 → exit.",
        "pta_code": """\
stoch = ta.stoch(high, low, close, k={k}, d={d}, smooth_k=1)
k_col = f'STOCHk_{{{k}}}_{{{d}}}_1'
d_col = f'STOCHd_{{{k}}}_{{{d}}}_1'
k   = stoch[k_col]
d   = stoch[d_col]
rsi = ta.rsi(close, length={rsi_period})
entries = cross_above(k, d) & (rsi < 50)
exits   = cross_below(k, d) | (rsi > 70)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        stoch    = bt.indicators.StochasticFull(self.data, period={k}, period_dfast=1, period_dslow={d})
        self.stk = stoch.percK
        self.std = stoch.percDSlow
        self.rsi = bt.indicators.RSI(self.data.close, period={rsi_period})
    def next(self):
        cross_up = self.stk[-1] <= self.std[-1] and self.stk[0] > self.std[0]
        cross_dn = self.stk[-1] >= self.std[-1] and self.stk[0] < self.std[0]
        if not self.position and cross_up and self.rsi[0] < 50: self.buy()
        elif self.position and (cross_dn or self.rsi[0] > 70):  self.close()
""",
    },

    # ── ATR / Volatility ──────────────────────────────────────────────────────

    "atr_trailing": {
        "params": {"ema_period": 20, "atr_period": 14, "atr_multiplier": 2.0},
        "description": "EMA + ATR trailing stop. Long when price > EMA, exit when price drops below ATR trailing stop.",
        "pta_code": """\
ema = ta.ema(close, length={ema_period})
atr = ta.atr(high, low, close, length={atr_period})
entries = pd.Series(False, index=close.index)
exits   = pd.Series(False, index=close.index)
in_pos = False
highest_since_entry = 0.0
trailing_stop = 0.0
for i in range(len(close)):
    c_val = close.iloc[i]
    ema_val = ema.iloc[i]
    atr_val = atr.iloc[i]
    if pd.isna(ema_val) or pd.isna(atr_val): continue
    if in_pos:
        if c_val > highest_since_entry:
            highest_since_entry = c_val
            trailing_stop = highest_since_entry - atr_val * {atr_multiplier}
        if c_val < trailing_stop:
            in_pos = False
            exits.iloc[i] = True
            highest_since_entry = 0.0
            trailing_stop = 0.0
    else:
        if c_val > ema_val:
            in_pos = True
            entries.iloc[i] = True
            highest_since_entry = c_val
            trailing_stop = c_val - atr_val * {atr_multiplier}
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.ema = bt.indicators.EMA(self.data.close, period={ema_period})
        self.atr = bt.indicators.ATR(self.data, period={atr_period})
        self.highest_since_entry = 0.0
        self.trailing_stop = 0.0
    def next(self):
        close = self.data.close[0]
        ema_val = self.ema[0]
        atr_val = self.atr[0]
        if self.position:
            if close > self.highest_since_entry:
                self.highest_since_entry = close
                self.trailing_stop = self.highest_since_entry - atr_val * {atr_multiplier}
            if close < self.trailing_stop:
                self.close()
                self.highest_since_entry = 0.0
                self.trailing_stop = 0.0
        else:
            if close > ema_val:
                self.buy()
                self.highest_since_entry = close
                self.trailing_stop = close - atr_val * {atr_multiplier}
""",
    },

    "volatility_squeezer": {
        "params": {"atr_period": 14, "ma_period": 50},
        "description": "ATR squeeze: long when price > MA and ATR is expanding, exit when price < MA.",
        "pta_code": """\
atr     = ta.atr(high, low, close, length={atr_period})
ma      = ta.sma(close, length={ma_period})
entries = (close > ma) & (atr > atr.shift(1))
exits   = close < ma
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.ma  = bt.indicators.SMA(self.data.close, period={ma_period})
        self.atr = bt.indicators.ATR(self.data, period={atr_period})
    def next(self):
        if len(self.ma) < {ma_period} or len(self.atr) < {atr_period} + 1:
            return
        atr_expanding = self.atr[0] > self.atr[-1]
        if not self.position and self.data.close[0] > self.ma[0] and atr_expanding: self.buy()
        elif self.position and self.data.close[0] < self.ma[0]: self.close()
""",
    },

    "volatility_vanguard": {
        "params": {"bb_period": 20, "bb_std": 2.0, "atr_period": 14},
        "description": "BB squeeze + ATR expansion. Long on BB upper breakout with expanding ATR, exit on BB middle.",
        "pta_code": """\
bb = ta.bbands(close, length={bb_period}, std={bb_std})
bb_upper  = bb.filter(like='BBU').iloc[:,0]
bb_middle = bb.filter(like='BBM').iloc[:,0]
atr       = ta.atr(high, low, close, length={atr_period})
entries = (close > bb_upper) & (atr > atr.shift(1))
exits   = close < bb_middle
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        bb = bt.indicators.BollingerBands(self.data.close, period={bb_period}, devfactor={bb_std})
        self.bbu = bb.lines.top
        self.bbm = bb.lines.mid
        self.atr = bt.indicators.ATR(self.data, period={atr_period})
    def next(self):
        if len(self.bbu) < {bb_period} or len(self.atr) < {atr_period} + 1:
            return
        atr_expanding = self.atr[0] > self.atr[-1]
        if not self.position and self.data.close[0] > self.bbu[0] and atr_expanding: self.buy()
        elif self.position and self.data.close[0] < self.bbm[0]: self.close()
""",
    },

    "volatility_ratio": {
        "params": {"lookback": 10, "threshold": 0.5},
        "description": "Volatility ratio breakout: long when VR > threshold and price is rising.",
        "pta_code": """\
tr = ta.true_range(high, low, close)
hh = high.rolling({lookback}).max()
ll = low.rolling({lookback}).min()
vr = tr / (hh - ll + 1e-10)
entries = (vr > {threshold}) & (close > close.shift(1))
exits   = vr <= {threshold}
""",
        "bt_code": """\
class VolatilityRatioInd(bt.Indicator):
    lines = ('vr',)
    params = (('lookback', {lookback}),)
    def __init__(self):
        self.tr = bt.indicators.TR(self.data)
        self.hh = bt.indicators.Highest(self.data.high, period=self.p.lookback)
        self.ll = bt.indicators.Lowest(self.data.low, period=self.p.lookback)
        self.lines.vr = self.tr / (self.hh - self.ll + 1e-10)

class BtStrat(bt.Strategy):
    def __init__(self):
        self.vr = VolatilityRatioInd(self.data)
    def next(self):
        if len(self.vr) < {lookback} + 1:
            return
        bullish_move = self.data.close[0] > self.data.close[-1]
        if not self.position and self.vr[0] > {threshold} and bullish_move: self.buy()
        elif self.position and self.vr[0] <= {threshold}: self.close()
""",
    },

    # ── Bollinger Bands ───────────────────────────────────────────────────────

    "bollinger_macd": {
        "params": {"bb_period": 20, "bb_std": 2.0, "fast": 12, "slow": 26, "signal": 9},
        "description": "BB upper breakout + MACD histogram positive → long; below middle or hist negative → exit.",
        "pta_code": """\
bb = ta.bbands(close, length={bb_period}, std={bb_std})
bb_upper  = bb.filter(like='BBU').iloc[:,0]
bb_middle = bb.filter(like='BBM').iloc[:,0]
macd_df   = ta.macd(close, fast={fast}, slow={slow}, signal={signal})
hist      = macd_df[f'MACDh_{{{fast}}}_{{{slow}}}_{{{signal}}}']
entries = (close > bb_upper) & (hist > 0)
exits   = (close < bb_middle) | (hist < 0)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        bb = bt.indicators.BollingerBands(self.data.close, period={bb_period}, devfactor={bb_std})
        self.bbu = bb.lines.top
        self.bbm = bb.lines.mid
        macd = bt.indicators.MACDHisto(self.data.close, period_me1={fast}, period_me2={slow}, period_signal={signal})
        self.hist = macd.lines.histo
    def next(self):
        if not self.position and self.data.close[0]>self.bbu[0] and self.hist[0]>0: self.buy()
        elif self.position and (self.data.close[0]<self.bbm[0] or self.hist[0]<0):  self.close()
""",
    },

    "bb_squeeze": {
        "params": {"period": 20, "std": 2.0},
        "description": "BB squeeze: wait for bandwidth<4%, then enter on upper band breakout, exit on middle.",
        "pta_code": """\
bb = ta.bbands(close, length={period}, std={std})
bb_upper  = bb.filter(like='BBU').iloc[:,0]
bb_middle = bb.filter(like='BBM').iloc[:,0]
bb_lower  = bb.filter(like='BBL').iloc[:,0]
bw        = (bb_upper - bb_lower) / bb_middle
squeezed  = bw < 0.04
was_squeezed = squeezed.shift(1).fillna(False)
entries = was_squeezed & (close > bb_upper)
exits   = close < bb_middle
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        bb = bt.indicators.BollingerBands(self.data.close, period={period}, devfactor={std})
        self.bbu = bb.lines.top
        self.bbm = bb.lines.mid
        self.bbl = bb.lines.bot
    def next(self):
        bw_prev = (self.bbu[-1] - self.bbl[-1]) / self.bbm[-1] if self.bbm[-1] != 0 else 1.0
        was_squeezed = bw_prev < 0.04
        if not self.position and was_squeezed and self.data.close[0] > self.bbu[0]: self.buy()
        elif self.position and self.data.close[0] < self.bbm[0]: self.close()
""",
    },

    "mean_reversion": {
        "params": {"bb_period": 20, "bb_std": 2.0, "rsi_period": 14, "bars": 4},
        "description": "Price below BB lower for N bars then recovers, RSI confirms oversold. Exit at upper band or RSI>70.",
        "pta_code": """\
bb = ta.bbands(close, length={bb_period}, std={bb_std})
bb_upper  = bb.filter(like='BBU').iloc[:,0]
bb_lower  = bb.filter(like='BBL').iloc[:,0]
rsi = ta.rsi(close, length={rsi_period})
below = close < bb_lower
below_streak = below.rolling({bars}).sum()
entries = (below_streak >= {bars}).shift(1).fillna(False) & (close >= bb_lower) & (rsi < 50)
exits   = (close >= bb_upper) | (rsi > 70)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        bb = bt.indicators.BollingerBands(self.data.close, period={bb_period}, devfactor={bb_std})
        self.bbu = bb.lines.top
        self.bbl = bb.lines.bot
        self.rsi = bt.indicators.RSI(self.data.close, period={rsi_period})
    def next(self):
        # check previous {bars} bars were all below lower band
        streak = all(self.data.close[-i] < self.bbl[-i] for i in range(1, {bars}+1))
        if not self.position and streak and self.data.close[0] >= self.bbl[0] and self.rsi[0] < 50: self.buy()
        elif self.position and (self.data.close[0] >= self.bbu[0] or self.rsi[0] > 70): self.close()
""",
    },

    "bb_rsi_reversal": {
        "params": {"bb_period": 20, "bb_std": 2.0, "rsi_period": 14, "oversold": 35.0, "overbought": 65.0},
        "description": "Price below BB lower + RSI oversold → long; above middle or RSI overbought → exit.",
        "pta_code": """\
bb = ta.bbands(close, length={bb_period}, std={bb_std})
bb_lower  = bb.filter(like='BBL').iloc[:,0]
bb_middle = bb.filter(like='BBM').iloc[:,0]
rsi = ta.rsi(close, length={rsi_period})
entries = (close < bb_lower) & (rsi < {oversold})
exits   = (close > bb_middle) | (rsi > {overbought})
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        bb = bt.indicators.BollingerBands(self.data.close, period={bb_period}, devfactor={bb_std})
        self.bbl = bb.lines.bot
        self.bbm = bb.lines.mid
        self.rsi = bt.indicators.RSI(self.data.close, period={rsi_period})
    def next(self):
        if not self.position and self.data.close[0]<self.bbl[0] and self.rsi<{oversold}: self.buy()
        elif self.position and (self.data.close[0]>self.bbm[0] or self.rsi>{overbought}): self.close()
""",
    },

    "bb_keltner_squeeze": {
        "params": {"bb_period": 20, "bb_std": 2.0, "kc_period": 20, "kc_atr": 10, "kc_mult": 1.5},
        "description": "BB inside Keltner = squeeze. Long on upside breakout post-squeeze, exit when price drops.",
        "pta_code": """\
bb = ta.bbands(close, length={bb_period}, std={bb_std})
bb_upper = bb.filter(like='BBU').iloc[:,0]
bb_middle = bb.filter(like='BBM').iloc[:,0]
bb_lower = bb.filter(like='BBL').iloc[:,0]
kc_center = ta.ema(close, length={kc_period})
kc_atr_val = ta.atr(high, low, close, length={kc_atr})
kc_upper = kc_center + {kc_mult} * kc_atr_val
kc_lower = kc_center - {kc_mult} * kc_atr_val
squeeze = (bb_upper < kc_upper) & (bb_lower > kc_lower)
squeeze_released = squeeze.shift(1).fillna(False) & ~squeeze
entries = squeeze_released & (close > bb_middle)
exits   = close < bb_middle
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.bb = bt.indicators.BollingerBands(self.data.close, period={bb_period}, devfactor={bb_std})
        self.ema = bt.indicators.EMA(self.data.close, period={kc_period})
        self.atr = bt.indicators.ATR(self.data, period={kc_atr})
        self.was_squeezed = False
    def next(self):
        kc_upper = self.ema[0] + {kc_mult} * self.atr[0]
        kc_lower = self.ema[0] - {kc_mult} * self.atr[0]
        squeezed = self.bb.lines.top[0] < kc_upper and self.bb.lines.bot[0] > kc_lower
        squeeze_released = self.was_squeezed and not squeezed
        self.was_squeezed = squeezed
        if not self.position and squeeze_released and self.data.close[0] > self.bb.lines.mid[0]: self.buy()
        elif self.position and self.data.close[0] < self.bb.lines.mid[0]: self.close()
""",
    },

    # ── ADX / DMI ─────────────────────────────────────────────────────────────

    "dmi_adx": {
        "params": {"period": 14, "adx_threshold": 25.0},
        "description": "+DI crosses above -DI AND ADX > threshold → long; -DI crosses above +DI → exit.",
        "pta_code": """\
adx_df = ta.adx(high, low, close, length={period})
adx   = adx_df[f'ADX_{{{period}}}']
plus  = adx_df[f'DMP_{{{period}}}']
minus = adx_df[f'DMN_{{{period}}}']
entries = cross_above(plus, minus) & (adx > {adx_threshold})
exits   = cross_above(minus, plus)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.adx   = bt.indicators.AverageDirectionalMovementIndex(self.data, period={period})
        self.plus  = bt.indicators.PlusDI(self.data, period={period})
        self.minus = bt.indicators.MinusDI(self.data, period={period})
    def next(self):
        cross_up   = self.plus[-1]<=self.minus[-1] and self.plus[0]>self.minus[0]
        cross_down = self.minus[-1]<=self.plus[-1] and self.minus[0]>self.plus[0]
        if not self.position and cross_up and self.adx[0]>{adx_threshold}: self.buy()
        elif self.position and cross_down: self.close()
""",
    },

    "wolfstein": {
        "params": {"adx_period": 14, "long_threshold": 27.5, "short_threshold": 20.5},
        "description": "ADX-based: long when ADX > long_threshold, exit when ADX < short_threshold.",
        "pta_code": """\
adx_df = ta.adx(high, low, close, length={adx_period})
adx    = adx_df[f'ADX_{{{adx_period}}}']
plus   = adx_df[f'DMP_{{{adx_period}}}']
entries = (adx > {long_threshold}) & (plus > adx_df[f'DMN_{{{adx_period}}}'])
exits   = adx < {short_threshold}
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.adx   = bt.indicators.AverageDirectionalMovementIndex(self.data, period={adx_period})
        self.plus  = bt.indicators.PlusDI(self.data, period={adx_period})
        self.minus = bt.indicators.MinusDI(self.data, period={adx_period})
    def next(self):
        if not self.position and self.adx[0] > {long_threshold} and self.plus[0] > self.minus[0]: self.buy()
        elif self.position and self.adx[0] < {short_threshold}: self.close()
""",
    },

    "trend_transition": {
        "params": {"fast": 50, "slow": 200, "adx_period": 14, "adx_threshold": 25.0},
        "description": "EMA crossover (50/200) confirmed by ADX > threshold → long; cross-below → exit.",
        "pta_code": """\
fast = ta.ema(close, length={fast})
slow = ta.ema(close, length={slow})
adx_df = ta.adx(high, low, close, length={adx_period})
adx    = adx_df[f'ADX_{{{adx_period}}}']
entries = cross_above(fast, slow) & (adx > {adx_threshold})
exits   = cross_below(fast, slow)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.fast = bt.indicators.EMA(self.data.close, period={fast})
        self.slow = bt.indicators.EMA(self.data.close, period={slow})
        self.adx  = bt.indicators.AverageDirectionalMovementIndex(self.data, period={adx_period})
    def next(self):
        cross_up   = self.fast[-1]<=self.slow[-1] and self.fast[0]>self.slow[0]
        cross_down = self.fast[-1]>=self.slow[-1] and self.fast[0]<self.slow[0]
        if not self.position and cross_up and self.adx[0]>{adx_threshold}: self.buy()
        elif self.position and cross_down: self.close()
""",
    },

    "swing_trader": {
        "params": {"cci_period": 20, "adx_period": 14, "adx_threshold": 25.0},
        "description": "CCI crosses above +100 AND ADX trend confirmed → long; CCI crosses below -100 → exit.",
        "pta_code": """\
cci = pta_cci(high, low, close, length={cci_period})
adx_df = ta.adx(high, low, close, length={adx_period})
adx    = adx_df[f'ADX_{{{adx_period}}}']
entries = cross_above(cci, pd.Series(100.0, index=cci.index)) & (adx > {adx_threshold})
exits   = cross_below(cci, pd.Series(-100.0, index=cci.index))
""",
        "bt_code": """\
class StandardCCI(bt.Indicator):
    lines = ('cci',)
    params = (('period', {cci_period}), ('factor', 0.015),)
    def __init__(self):
        self.tp = (self.data.high + self.data.low + self.data.close) / 3.0
        self.tp_sma = bt.indicators.SMA(self.tp, period=self.p.period)
    def next(self):
        tps = [self.tp[-i] for i in range(self.p.period)]
        sma = self.tp_sma[0]
        mad = sum(abs(x - sma) for x in tps) / self.p.period
        if mad == 0.0:
            self.lines.cci[0] = 0.0
        else:
            self.lines.cci[0] = (self.tp[0] - sma) / (self.p.factor * mad)

class BtStrat(bt.Strategy):
    def __init__(self):
        self.cci = StandardCCI(self.data, period={cci_period})
        self.adx = bt.indicators.AverageDirectionalMovementIndex(self.data, period={adx_period})
    def next(self):
        cross_up = self.cci[-1] <= 100 and self.cci[0] > 100
        cross_dn = self.cci[-1] >= -100 and self.cci[0] < -100
        if not self.position and cross_up and self.adx[0] > {adx_threshold}: self.buy()
        elif self.position and cross_dn: self.close()
""",
    },

    "adx_ema_cross": {
        "params": {"fast": 20, "slow": 50, "adx_period": 14, "adx_threshold": 25.0},
        "description": "EMA crossover filtered by ADX trend strength.",
        "pta_code": """\
fast = ta.ema(close, length={fast})
slow = ta.ema(close, length={slow})
adx_df = ta.adx(high, low, close, length={adx_period})
adx    = adx_df[f'ADX_{{{adx_period}}}']
entries = cross_above(fast, slow) & (adx > {adx_threshold})
exits   = cross_below(fast, slow)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.fast = bt.indicators.EMA(self.data.close, period={fast})
        self.slow = bt.indicators.EMA(self.data.close, period={slow})
        self.adx  = bt.indicators.AverageDirectionalMovementIndex(self.data, period={adx_period})
    def next(self):
        cross_up = self.fast[-1] <= self.slow[-1] and self.fast[0] > self.slow[0]
        cross_dn = self.fast[-1] >= self.slow[-1] and self.fast[0] < self.slow[0]
        if not self.position and cross_up and self.adx[0] > {adx_threshold}: self.buy()
        elif self.position and cross_dn: self.close()
""",
    },

    # ── CCI ───────────────────────────────────────────────────────────────────

    "cci_reversal": {
        "params": {"period": 20, "entry_level": -100.0, "exit_level": 100.0},
        "description": "CCI crosses above entry_level (-100) → long; crosses above exit_level (100) → exit.",
        "pta_code": """\
cci = pta_cci(high, low, close, length={period})
entries = cross_above(cci, pd.Series({entry_level}, index=cci.index))
exits   = cross_above(cci, pd.Series({exit_level}, index=cci.index))
""",
        "bt_code": """\
class StandardCCI(bt.Indicator):
    lines = ('cci',)
    params = (('period', {period}), ('factor', 0.015),)
    def __init__(self):
        self.tp = (self.data.high + self.data.low + self.data.close) / 3.0
        self.tp_sma = bt.indicators.SMA(self.tp, period=self.p.period)
    def next(self):
        tps = [self.tp[-i] for i in range(self.p.period)]
        sma = self.tp_sma[0]
        mad = sum(abs(x - sma) for x in tps) / self.p.period
        if mad == 0.0:
            self.lines.cci[0] = 0.0
        else:
            self.lines.cci[0] = (self.tp[0] - sma) / (self.p.factor * mad)

class BtStrat(bt.Strategy):
    def __init__(self):
        self.cci = StandardCCI(self.data, period={period})
    def next(self):
        if not self.position and self.cci[-1]<={entry_level} and self.cci[0]>{entry_level}: self.buy()
        elif self.position and self.cci[-1]<={exit_level} and self.cci[0]>{exit_level}: self.close()
""",
    },

    # ── SuperTrend ────────────────────────────────────────────────────────────

    "supertrend": {
        "params": {"period": 10, "multiplier": 3.0},
        "description": "SuperTrend flips bullish → long; flips bearish → exit.",
        "pta_code": """\
st_df = ta.supertrend(high, low, close, length={period}, multiplier={multiplier})
direction = st_df[f'SUPERTd_{{{period}}}_{{{multiplier}}}']
bullish   = direction == 1
entries   = cross_above(bullish.astype(float), pd.Series(0.5, index=bullish.index))
exits     = cross_below(bullish.astype(float), pd.Series(0.5, index=bullish.index))
""",
        "bt_code": """\
class SuperTrendInd(bt.Indicator):
    lines = ('direction',)
    params = (('period', {period}), ('multiplier', {multiplier}),)
    def __init__(self):
        self.atr = bt.indicators.ATR(self.data, period=self.p.period)
        self._up  = [None]
        self._dn  = [None]
        self._dir = [1]
    def next(self):
        hl2 = (self.data.high[0] + self.data.low[0]) / 2
        basic_up = hl2 - self.p.multiplier * self.atr[0]
        basic_dn = hl2 + self.p.multiplier * self.atr[0]
        prev_up = self._up[-1] if self._up[-1] is not None else basic_up
        prev_dn = self._dn[-1] if self._dn[-1] is not None else basic_dn
        final_up = max(basic_up, prev_up) if self.data.close[-1] > prev_up else basic_up
        final_dn = min(basic_dn, prev_dn) if self.data.close[-1] < prev_dn else basic_dn
        prev_dir = self._dir[-1]
        if prev_dir == -1 and self.data.close[0] > prev_dn:
            direction = 1
        elif prev_dir == 1 and self.data.close[0] < prev_up:
            direction = -1
        else:
            direction = prev_dir
        self._up.append(final_up); self._dn.append(final_dn); self._dir.append(direction)
        self.lines.direction[0] = direction

class BtStrat(bt.Strategy):
    def __init__(self):
        self.st = SuperTrendInd(self.data, period={period}, multiplier={multiplier})
    def next(self):
        bullish_now  = self.st.lines.direction[0]  == 1
        bullish_prev = self.st.lines.direction[-1] == 1
        if not self.position and not bullish_prev and bullish_now: self.buy()
        elif self.position and bullish_prev and not bullish_now:   self.close()
""",
    },

    "supertrend_macd": {
        "params": {"period": 10, "multiplier": 3.0, "macd_fast": 12, "macd_slow": 26, "macd_signal": 9},
        "description": "SuperTrend bullish AND MACD hist > 0 → long; SuperTrend bearish → exit.",
        "pta_code": """\
st_df = ta.supertrend(high, low, close, length={period}, multiplier={multiplier})
direction = st_df[f'SUPERTd_{{{period}}}_{{{multiplier}}}']
bullish   = (direction == 1)
macd_df   = ta.macd(close, fast={macd_fast}, slow={macd_slow}, signal={macd_signal})
hist      = macd_df[f'MACDh_{{{macd_fast}}}_{{{macd_slow}}}_{{{macd_signal}}}']
entries = bullish & (hist > 0)
exits   = ~bullish
""",
        "bt_code": """\
class SuperTrendInd2(bt.Indicator):
    lines = ('direction',)
    params = (('period', {period}), ('multiplier', {multiplier}),)
    def __init__(self):
        self.atr = bt.indicators.ATR(self.data, period=self.p.period)
        self._up  = [None]
        self._dn  = [None]
        self._dir = [1]
    def next(self):
        hl2 = (self.data.high[0] + self.data.low[0]) / 2
        basic_up = hl2 - self.p.multiplier * self.atr[0]
        basic_dn = hl2 + self.p.multiplier * self.atr[0]
        prev_up = self._up[-1] if self._up[-1] is not None else basic_up
        prev_dn = self._dn[-1] if self._dn[-1] is not None else basic_dn
        final_up = max(basic_up, prev_up) if self.data.close[-1] > prev_up else basic_up
        final_dn = min(basic_dn, prev_dn) if self.data.close[-1] < prev_dn else basic_dn
        prev_dir = self._dir[-1]
        if prev_dir == -1 and self.data.close[0] > prev_dn:
            direction = 1
        elif prev_dir == 1 and self.data.close[0] < prev_up:
            direction = -1
        else:
            direction = prev_dir
        self._up.append(final_up); self._dn.append(final_dn); self._dir.append(direction)
        self.lines.direction[0] = direction

class BtStrat(bt.Strategy):
    def __init__(self):
        self.st   = SuperTrendInd2(self.data, period={period}, multiplier={multiplier})
        macd      = bt.indicators.MACDHisto(self.data.close, period_me1={macd_fast}, period_me2={macd_slow}, period_signal={macd_signal})
        self.hist = macd.lines.histo
    def next(self):
        bullish = self.st.lines.direction[0] == 1
        if not self.position and bullish and self.hist[0] > 0: self.buy()
        elif self.position and not bullish:                     self.close()
""",
    },

    # ── Parabolic SAR ─────────────────────────────────────────────────────────

    "parabolic_sar": {
        "params": {"step": 0.02, "max": 0.2},
        "description": "Parabolic SAR flips bullish (price > SAR) → long; flips bearish → exit.",
        "pta_code": """\
sar_df = ta.psar(high, low, close, af0={step}, af={step}, max_af={max})
# PSARl = SAR in long mode (NaN when bearish), PSARs = SAR in short mode
sar_l = sar_df.filter(like='PSARl').iloc[:,0]
bullish = sar_l.notna()
entries = cross_above(bullish.astype(float), pd.Series(0.5, index=bullish.index))
exits   = cross_below(bullish.astype(float), pd.Series(0.5, index=bullish.index))
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.psar = CustomPSAR(self.data, step={step}, max_af={max})
    def next(self):
        bullish_now  = self.psar.lines.bullish[0] >= 0.5
        bullish_prev = self.psar.lines.bullish[-1] >= 0.5
        if not self.position and not bullish_prev and bullish_now: self.buy()
        elif self.position and bullish_prev and not bullish_now:   self.close()
""",
    },

    # ── Heiken Ashi ───────────────────────────────────────────────────────────

    "heiken_ashi_color": {
        "params": {"smooth": 1},
        "description": "Heiken Ashi: green bar (HA close > HA open) → long; red bar → exit.",
        "pta_code": """\
# Compute Heiken Ashi
ha_close = (close + high + low + close) / 4
ha_open  = (close.shift(1) + high.shift(1) + low.shift(1) + close.shift(1)) / 4
ha_open  = ha_open.ewm(span={smooth}, adjust=False).mean() if {smooth} > 1 else ha_open
ha_close = ha_close.ewm(span={smooth}, adjust=False).mean() if {smooth} > 1 else ha_close
ha_green = ha_close > ha_open
entries  = ha_green & ~ha_green.shift(1).fillna(True)
exits    = ~ha_green & ha_green.shift(1).fillna(False)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.ha = bt.indicators.HeikinAshi(self.data)
    def next(self):
        green_now  = self.ha.lines.ha_close[0]  > self.ha.lines.ha_open[0]
        green_prev = self.ha.lines.ha_close[-1] > self.ha.lines.ha_open[-1]
        if not self.position and not green_prev and green_now: self.buy()
        elif self.position and green_prev and not green_now:   self.close()
""",
    },

    "heiken_ashi_breakout": {
        "params": {"smooth": 1, "consecutive_bars": 2},
        "description": "N consecutive green HA bars → long; N consecutive red bars → exit.",
        "pta_code": """\
ha_close = (close + high + low + close) / 4
ha_open  = (close.shift(1) + high.shift(1) + low.shift(1) + close.shift(1)) / 4
ha_green = ha_close > ha_open
green_streak = ha_green.rolling({consecutive_bars}).sum() == {consecutive_bars}
red_streak   = (~ha_green).rolling({consecutive_bars}).sum() == {consecutive_bars}
entries = green_streak & ~green_streak.shift(1).fillna(True)
exits   = red_streak
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.ha = bt.indicators.HeikinAshi(self.data)
    def next(self):
        n = {consecutive_bars}
        green_streak = all(self.ha.lines.ha_close[-i] > self.ha.lines.ha_open[-i] for i in range(n))
        red_streak   = all(self.ha.lines.ha_close[-i] < self.ha.lines.ha_open[-i] for i in range(n))
        prev_green_streak = all(self.ha.lines.ha_close[-i-1] > self.ha.lines.ha_open[-i-1] for i in range(n))
        if not self.position and green_streak and not prev_green_streak: self.buy()
        elif self.position and red_streak: self.close()
""",
    },

    "heiken_ashi_harmonizer": {
        "params": {"smooth": 1, "ema_period": 50},
        "description": "HA green bars AND price above EMA → long; HA red bars → exit.",
        "pta_code": """\
ha_close = (close + high + low + close) / 4
ha_open  = (close.shift(1) + high.shift(1) + low.shift(1) + close.shift(1)) / 4
ha_green = ha_close >= ha_open
ema      = ta.ema(close, length={ema_period})
entries  = ha_green & (close > ema)
exits    = ~ha_green | (close < ema)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.ha  = bt.indicators.HeikinAshi(self.data)
        self.ema = bt.indicators.EMA(self.data.close, period={ema_period})
    def next(self):
        green_now  = self.ha.lines.ha_close[0]  >= self.ha.lines.ha_open[0]
        above_now  = self.data.close[0]  > self.ema[0]
        if not self.position:
            if green_now and above_now: self.buy()
        else:
            if not green_now or self.data.close[0] < self.ema[0]: self.close()
""",
    },

    # ── Ichimoku ──────────────────────────────────────────────────────────────

    "ichimoku_cloud": {
        "params": {"tenkan": 9, "kijun": 26, "senkou_b": 52},
        "description": "Price above cloud (span_a and span_b) → long; below cloud → exit.",
        "pta_code": """\
ichi = ta.ichimoku(high, low, close, tenkan={tenkan}, kijun={kijun}, senkou={senkou_b})[0]
span_a = ichi[f'ISA_{{{tenkan}}}']
span_b = ichi[f'ISB_{{{kijun}}}']
cloud_top = span_a.combine(span_b, max)
cloud_bot = span_a.combine(span_b, min)
entries = close > cloud_top
exits   = close < cloud_bot
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        ich = bt.indicators.Ichimoku(self.data, tenkan={tenkan}, kijun={kijun}, senkou={senkou_b})
        self.span_a = ich.lines.senkou_span_a
        self.span_b = ich.lines.senkou_span_b
    def next(self):
        cloud_top = max(self.span_a[0], self.span_b[0])
        cloud_bot = min(self.span_a[0], self.span_b[0])
        if not self.position and self.data.close[0] > cloud_top: self.buy()
        elif self.position and self.data.close[0] < cloud_bot:   self.close()
""",
    },

    "ichimoku_cross": {
        "params": {"tenkan": 9, "kijun": 26, "senkou_b": 52},
        "description": "TK cross (tenkan > kijun) above cloud → long; tenkan crosses below kijun → exit.",
        "pta_code": """\
ichi = ta.ichimoku(high, low, close, tenkan={tenkan}, kijun={kijun}, senkou={senkou_b})[0]
tenkan_line = ichi[f'ITS_{{{tenkan}}}']
kijun_line  = ichi[f'IKS_{{{kijun}}}']
span_a = ichi[f'ISA_{{{tenkan}}}']
span_b = ichi[f'ISB_{{{kijun}}}']
cloud_top = span_a.combine(span_b, max)
entries = cross_above(tenkan_line, kijun_line) & (close > cloud_top)
exits   = cross_below(tenkan_line, kijun_line)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.ich = bt.indicators.Ichimoku(self.data, tenkan={tenkan}, kijun={kijun}, senkou={senkou_b})
    def next(self):
        cross_up = self.ich.lines.tenkan_sen[-1] <= self.ich.lines.kijun_sen[-1] and self.ich.lines.tenkan_sen[0] > self.ich.lines.kijun_sen[0]
        cross_dn = self.ich.lines.tenkan_sen[-1] >= self.ich.lines.kijun_sen[-1] and self.ich.lines.tenkan_sen[0] < self.ich.lines.kijun_sen[0]
        cloud_top = max(self.ich.lines.senkou_span_a[0], self.ich.lines.senkou_span_b[0])
        if not self.position and cross_up and self.data.close[0] > cloud_top: self.buy()
        elif self.position and cross_dn: self.close()
""",
    },

    # ── Alligator ─────────────────────────────────────────────────────────────

    "alligator": {
        "params": {"jaw": 13, "teeth": 8, "lips": 5},
        "description": "Williams Alligator: lips > teeth > jaw (bullish alignment) → long; inversion → exit.",
        "pta_code": """\
mid   = (high + low) / 2
jaw   = ta.smma(mid, length={jaw}).shift(8)
teeth = ta.smma(mid, length={teeth}).shift(5)
lips  = ta.smma(mid, length={lips}).shift(3)
bullish = (lips > teeth) & (teeth > jaw)
entries = bullish & ~bullish.shift(1).fillna(True)
exits   = ~bullish & bullish.shift(1).fillna(False)
""",
        "bt_code": """\
class AlligatorInd(bt.Indicator):
    lines = ('jaw', 'teeth', 'lips',)
    params = (('jaw', {jaw}), ('teeth', {teeth}), ('lips', {lips}),
              ('jaw_shift', 8), ('teeth_shift', 5), ('lips_shift', 3),)
    def __init__(self):
        hl2 = (self.data.high + self.data.low) / 2.0
        jaw_raw   = bt.indicators.SMMA(hl2, period=self.p.jaw)
        teeth_raw = bt.indicators.SMMA(hl2, period=self.p.teeth)
        lips_raw  = bt.indicators.SMMA(hl2, period=self.p.lips)
        self.lines.jaw   = jaw_raw(-self.p.jaw_shift)
        self.lines.teeth = teeth_raw(-self.p.teeth_shift)
        self.lines.lips  = lips_raw(-self.p.lips_shift)

class BtStrat(bt.Strategy):
    def __init__(self):
        self.alg = AlligatorInd(self.data, jaw={jaw}, teeth={teeth}, lips={lips})
    def next(self):
        bull_now  = self.alg.lips[0]  > self.alg.teeth[0]  > self.alg.jaw[0]
        bull_prev = self.alg.lips[-1] > self.alg.teeth[-1] > self.alg.jaw[-1]
        if not self.position and not bull_prev and bull_now: self.buy()
        elif self.position and bull_prev and not bull_now:   self.close()
""",
    },

    # ── Elder Ray ─────────────────────────────────────────────────────────────

    "elder_ray": {
        "params": {"period": 13},
        "description": "EMA rising + bear power negative but increasing → long; bull power turns negative → exit.",
        "pta_code": """\
ema        = ta.ema(close, length={period})
bull_power = high - ema
bear_power = low  - ema
ema_rising  = ema > ema.shift(1)
bear_rising = bear_power > bear_power.shift(1)
entries = ema_rising & (bear_power < 0) & bear_rising
bull_neg = bull_power < 0
exits   = bull_neg & ~bull_neg.shift(1).fillna(True)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.ema = bt.indicators.EMA(self.data.close, period={period})
    def next(self):
        bull_power = self.data.high[0]  - self.ema[0]
        bear_power = self.data.low[0]   - self.ema[0]
        bear_prev  = self.data.low[-1]  - self.ema[-1]
        ema_rising  = self.ema[0]  > self.ema[-1]
        bear_rising = bear_power   > bear_prev
        bull_neg_now  = bull_power < 0
        bull_neg_prev = (self.data.high[-1] - self.ema[-1]) < 0
        if not self.position and ema_rising and bear_power < 0 and bear_rising: self.buy()
        elif self.position and bull_neg_now and not bull_neg_prev: self.close()
""",
    },

    # ── Aroon ─────────────────────────────────────────────────────────────────

    "aroon_trend": {
        "params": {"period": 25, "bull_threshold": 70.0, "bear_threshold": 30.0},
        "description": "Aroon Up > bull_threshold AND Down < bear_threshold → long; Up < Down → exit.",
        "pta_code": """\
aroon_df  = ta.aroon(high, low, length={period})
aroon_up  = aroon_df[f'AROONU_{{{period}}}']
aroon_dn  = aroon_df[f'AROOND_{{{period}}}']
entries = (aroon_up > {bull_threshold}) & (aroon_dn < {bear_threshold})
exits   = aroon_up < aroon_dn
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        aroon = CustomAroon(self.data, period={period})
        self.aroon_up = aroon.lines.aroonup
        self.aroon_dn = aroon.lines.aroondown
    def next(self):
        if not self.position and self.aroon_up[0] > {bull_threshold} and self.aroon_dn[0] < {bear_threshold}: self.buy()
        elif self.position and self.aroon_up[0] < self.aroon_dn[0]: self.close()
""",
    },

    # ── Chandelier Exit ───────────────────────────────────────────────────────

    "chandelier_exit": {
        "params": {"period": 22, "multiplier": 3.0},
        "description": "Long when price crosses above chandelier long stop; exit when crosses below.",
        "pta_code": """\
atr   = ta.atr(high, low, close, length={period})
hh    = high.rolling({period}).max()
long_stop = hh - {multiplier} * atr
prev_long_stop = long_stop.shift(1)
long_stop = long_stop.combine(prev_long_stop, lambda a,b: max(a,b) if close.shift(1).loc[a] > b else a) if False else long_stop
# Simpler: close > chandelier stop → long
bullish = close > long_stop
entries = bullish & ~bullish.shift(1).fillna(True)
exits   = ~bullish & bullish.shift(1).fillna(False)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.atr = bt.indicators.ATR(self.data, period={period})
        self.hh  = bt.indicators.Highest(self.data.high, period={period})
    def next(self):
        stop = self.hh[0] - {multiplier} * self.atr[0]
        stop_prev = self.hh[-1] - {multiplier} * self.atr[-1]
        bull_now  = self.data.close[0]  > stop
        bull_prev = self.data.close[-1] > stop_prev
        if not self.position and not bull_prev and bull_now: self.buy()
        elif self.position and bull_prev and not bull_now:   self.close()
""",
    },

    # ── VWAP ──────────────────────────────────────────────────────────────────

    "vwap_bounce": {
        "params": {"rsi_period": 14, "oversold": 40.0, "overbought": 65.0, "session_gap_mins": 60},
        "description": "Cross above VWAP with RSI near oversold → long; cross below or RSI overbought → exit.",
        "pta_code": """\
vwap_val = ta.vwap(high, low, close, vol)
rsi      = ta.rsi(close, length={rsi_period})
above_vwap = close > vwap_val
was_above  = above_vwap.shift(1).fillna(False)
# Cross above VWAP + RSI near oversold
entries = (~was_above) & above_vwap & (rsi < ({oversold} + 10))
exits   = (was_above & ~above_vwap) | (rsi > {overbought})
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.vwap = ResetVWAP(self.data, session_gap_mins={session_gap_mins})
        self.rsi  = bt.indicators.RSI(self.data.close, period={rsi_period})
    def next(self):
        above_now  = self.data.close[0]  > self.vwap.lines.vwap[0]
        above_prev = self.data.close[-1] > self.vwap.lines.vwap[-1]
        cross_up = not above_prev and above_now
        cross_dn = above_prev and not above_now
        if not self.position and cross_up and self.rsi[0] < ({oversold} + 10): self.buy()
        elif self.position and (cross_dn or self.rsi[0] > {overbought}): self.close()
""",
    },

    "vwap_trend": {
        "params": {"session_gap_mins": 60},
        "description": "Price above VWAP → stay long; price crosses below → exit.",
        "pta_code": """\
vwap_val = ta.vwap(high, low, close, vol)
above    = close > vwap_val
entries  = above & ~above.shift(1).fillna(True)
exits    = ~above & above.shift(1).fillna(False)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.vwap = ResetVWAP(self.data, session_gap_mins={session_gap_mins})
    def next(self):
        above_now  = self.data.close[0]  > self.vwap.lines.vwap[0]
        above_prev = self.data.close[-1] > self.vwap.lines.vwap[-1]
        if not self.position and not above_prev and above_now: self.buy()
        elif self.position and above_prev and not above_now:   self.close()
""",
    },

    # ── Momentum ──────────────────────────────────────────────────────────────

    "momentum_roc": {
        "params": {"roc_period": 10, "ema_period": 50, "entry_threshold": 0.5, "exit_threshold": 0.0},
        "description": "ROC > entry_threshold AND price > EMA → long; ROC < exit_threshold or price < EMA → exit.",
        "pta_code": """\
roc = ta.roc(close, length={roc_period})
ema = ta.ema(close, length={ema_period})
entries = (roc > {entry_threshold}) & (close > ema)
exits   = (roc < {exit_threshold}) | (close < ema)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.roc = bt.indicators.ROC(self.data.close, period={roc_period})
        self.ema = bt.indicators.EMA(self.data.close, period={ema_period})
    def next(self):
        if not self.position and self.roc>{entry_threshold} and self.data.close>self.ema: self.buy()
        elif self.position and (self.roc<{exit_threshold} or self.data.close<self.ema):   self.close()
""",
    },

    "dual_momentum": {
        "params": {"fast": 10, "slow": 30},
        "description": "Both fast and slow ROC positive → long; either negative → exit.",
        "pta_code": """\
roc_fast = ta.roc(close, length={fast})
roc_slow = ta.roc(close, length={slow})
entries = (roc_fast > 0) & (roc_slow > 0)
exits   = (roc_fast < 0) | (roc_slow < 0)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.roc_fast = bt.indicators.ROC(self.data.close, period={fast})
        self.roc_slow = bt.indicators.ROC(self.data.close, period={slow})
    def next(self):
        if not self.position and self.roc_fast[0] > 0 and self.roc_slow[0] > 0: self.buy()
        elif self.position and (self.roc_fast[0] < 0 or self.roc_slow[0] < 0):  self.close()
""",
    },

    "roc": {
        "params": {"period": 10},
        "description": "ROC zero-cross. Long when ROC crosses above 0, exit when crosses below 0.",
        "pta_code": """\
roc = ta.roc(close, length={period})
entries = cross_above(roc, pd.Series(0.0, index=roc.index))
exits   = cross_below(roc, pd.Series(0.0, index=roc.index))
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.roc = bt.indicators.ROC(self.data.close, period={period})
    def next(self):
        if not self.position and self.roc[-1]<=0 and self.roc[0]>0: self.buy()
        elif self.position and self.roc[-1]>=0 and self.roc[0]<0:   self.close()
""",
    },

    "kst": {
        "params": {"period": 10},
        "description": "KST (alias for roc in this implementation). ROC zero-cross.",
        "pta_code": """\
roc = ta.roc(close, length={period})
entries = cross_above(roc, pd.Series(0.0, index=roc.index))
exits   = cross_below(roc, pd.Series(0.0, index=roc.index))
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.roc = bt.indicators.ROC(self.data.close, period={period})
    def next(self):
        if not self.position and self.roc[-1] <= 0 and self.roc[0] > 0: self.buy()
        elif self.position and self.roc[-1] >= 0 and self.roc[0] < 0:   self.close()
""",
    },

    "trix": {
        "params": {"period": 18, "signal": 9},
        "description": "TRIX histogram (TRIX - signal) crosses above 0 → long; below 0 → exit.",
        "pta_code": """\
trix_df = ta.trix(close, length={period}, signal={signal})
trix_line = trix_df[f'TRIX_{{{period}}}_{{{signal}}}']
trix_sig  = trix_df[f'TRIXs_{{{period}}}_{{{signal}}}']
hist = trix_line - trix_sig
entries = cross_above(hist, pd.Series(0.0, index=hist.index))
exits   = cross_below(hist, pd.Series(0.0, index=hist.index))
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        trix = bt.indicators.TRIX(self.data.close, period={period})
        sig  = bt.indicators.EMA(trix, period={signal})
        self.hist_line = trix - sig
    def next(self):
        if not self.position and self.hist_line[-1] <= 0 and self.hist_line[0] > 0: self.buy()
        elif self.position and self.hist_line[-1] >= 0 and self.hist_line[0] < 0:   self.close()
""",
    },

    "tsi": {
        "params": {"first": 25, "second": 13, "entry_threshold": -25.0, "exit_threshold": 25.0},
        "description": "TSI crosses above entry_threshold → long; crosses below exit_threshold → exit.",
        "pta_code": """\
tsi = ta.tsi(close, fast={second}, slow={first}).iloc[:, 0]
entries = cross_above(tsi, pd.Series({entry_threshold}, index=tsi.index))
exits   = cross_below(tsi, pd.Series({exit_threshold}, index=tsi.index))
""",
        "bt_code": """\
class TSIInd(bt.Indicator):
    lines = ('tsi',)
    params = (('slow', {first}), ('fast', {second}),)
    def __init__(self):
        pc = self.data.close - self.data.close(-1)
        dpc = bt.If(pc >= 0, pc, -pc)
        self.lines.tsi = bt.indicators.EMA(bt.indicators.EMA(pc, period=self.p.slow), period=self.p.fast) / \
                         (bt.indicators.EMA(bt.indicators.EMA(dpc, period=self.p.slow), period=self.p.fast) + 1e-10) * 100

class BtStrat(bt.Strategy):
    def __init__(self):
        self.tsi = TSIInd(self.data, slow={first}, fast={second})
    def next(self):
        if not self.position and self.tsi.lines.tsi[-1] <= {entry_threshold} and self.tsi.lines.tsi[0] > {entry_threshold}: self.buy()
        elif self.position and self.tsi.lines.tsi[-1] >= {exit_threshold} and self.tsi.lines.tsi[0] < {exit_threshold}:     self.close()
""",
    },

    # ── StochRSI ──────────────────────────────────────────────────────────────

    "stoch_rsi": {
        "params": {"period": 14, "smooth_d": 3, "oversold": 0.2, "overbought": 0.8},
        "description": "StochRSI K drops below oversold → long; rises above overbought → exit.",
        "pta_code": """\
k = pta_stoch_rsi(close, period={period}, smooth_k=3)
entries = cross_below(k, pd.Series({oversold}, index=k.index))
exits   = cross_above(k, pd.Series({overbought}, index=k.index))
""",
        "bt_code": """\
class StochRSIInd(bt.Indicator):
    lines = ('k',)
    params = (('rsi_period', {period}), ('stoch_period', {period}), ('smooth_k', 3), ('smooth_d', {smooth_d}),)
    def __init__(self):
        rsi = bt.indicators.RSI(self.data.close, period=self.p.rsi_period, movav=bt.indicators.SmoothedMovingAverage)
        rsi_high = bt.indicators.Highest(rsi, period=self.p.stoch_period)
        rsi_low  = bt.indicators.Lowest(rsi,  period=self.p.stoch_period)
        raw_k = (rsi - rsi_low) / (rsi_high - rsi_low + 1e-10)
        self.lines.k = bt.indicators.SMA(raw_k, period=3)

class BtStrat(bt.Strategy):
    def __init__(self):
        self.srsi = StochRSIInd(self.data)
    def next(self):
        if len(self.srsi.lines.k) < {period} + 3:
            return
        k_now  = self.srsi.lines.k[0]
        k_prev = self.srsi.lines.k[-1]
        cross_dn = k_prev >= {oversold} and k_now < {oversold}
        cross_up = k_prev <= {overbought} and k_now > {overbought}
        if not self.position and cross_dn: self.buy()
        elif self.position and cross_up:   self.close()
""",
    },

    # ── Chop Filter ───────────────────────────────────────────────────────────

    "chop_filter": {
        "params": {"chop_period": 14, "fast_ema": 8, "slow_ema": 21, "chop_threshold": 61.8},
        "description": "EMA crossover allowed only when CHOP index < threshold (trending market).",
        "pta_code": """\
chop = ta.chop(high, low, close, length={chop_period})
fast = ta.ema(close, length={fast_ema})
slow = ta.ema(close, length={slow_ema})
trending = chop < {chop_threshold}
entries = cross_above(fast, slow) & trending
exits   = cross_below(fast, slow)
""",
        "bt_code": """\
import math as _math

class ChopInd(bt.Indicator):
    lines = ('chop',)
    params = (('period', {chop_period}),)
    def __init__(self):
        self.atr = bt.indicators.ATR(self.data, period=1)
        self.hh  = bt.indicators.Highest(self.data.high, period=self.p.period)
        self.ll  = bt.indicators.Lowest(self.data.low,   period=self.p.period)
        self.atr_sum = bt.indicators.SumN(self.atr, period=self.p.period)
    def next(self):
        rng = self.hh[0] - self.ll[0]
        if rng == 0:
            self.lines.chop[0] = 100.0
        else:
            self.lines.chop[0] = 100.0 * _math.log10(self.atr_sum[0] / rng) / _math.log10(self.p.period)

class BtStrat(bt.Strategy):
    def __init__(self):
        self.fast = bt.indicators.EMA(self.data.close, period={fast_ema})
        self.slow = bt.indicators.EMA(self.data.close, period={slow_ema})
        self.chop = ChopInd(self.data, period={chop_period})
    def next(self):
        cross_up = self.fast[-1] <= self.slow[-1] and self.fast[0] > self.slow[0]
        cross_dn = self.fast[-1] >= self.slow[-1] and self.fast[0] < self.slow[0]
        if not self.position and cross_up and self.chop.lines.chop[0] < {chop_threshold}: self.buy()
        elif self.position and cross_dn: self.close()
""",
    },

    # ── ConnorsRSI ────────────────────────────────────────────────────────────

    "connors_rsi": {
        "params": {"rsi_period": 3, "streak_period": 2, "rank_period": 100, "oversold": 10.0, "overbought": 70.0},
        "description": "ConnorsRSI < oversold → long; > overbought → exit.",
        "pta_code": """\
# ConnorsRSI = (RSI(3) + RSI_of_streak(2) + percentile_rank(100)) / 3
rsi3 = ta.rsi(close, length={rsi_period})

# Consecutive up/down streak
returns = close.pct_change()
streak = pd.Series(0.0, index=close.index)
for i in range(1, len(returns)):
    if returns.iloc[i] > 0:
        streak.iloc[i] = streak.iloc[i-1] + 1 if streak.iloc[i-1] >= 0 else 1
    elif returns.iloc[i] < 0:
        streak.iloc[i] = streak.iloc[i-1] - 1 if streak.iloc[i-1] <= 0 else -1
rsi_streak = ta.rsi(streak.abs(), length={streak_period})

pct_rank = returns.rolling({rank_period}).apply(
    lambda x: pd.Series(x).rank(pct=True).iloc[-1] * 100, raw=False)

crsi = (rsi3 + rsi_streak + pct_rank) / 3
entries = crsi < {oversold}
exits   = crsi > {overbought}
""",
        "bt_code": """\
class ConnorsRSIInd(bt.Indicator):
    lines = ('crsi',)
    params = (('rsi_period', {rsi_period}), ('streak_period', {streak_period}), ('rank_period', {rank_period}),)
    def __init__(self):
        self.rsi3 = bt.indicators.RSI(self.data.close, period=self.p.rsi_period, safediv=True)
        self._streak = 0.0
        self._roc_window = []
        # Wilder RSI-of-streak state (matches alm Rsi::new(streak_period))
        self._sr_prev = None
        self._sr_count = 0
        self._sr_seed_g = []
        self._sr_seed_l = []
        self._sr_avg_gain = None
        self._sr_avg_loss = None
    def _streak_rsi(self, streak_val):
        if self._sr_prev is None:
            self._sr_prev = streak_val
            return float('nan')
        change = streak_val - self._sr_prev
        self._sr_prev = streak_val
        gain = max(0.0, change)
        loss = max(0.0, -change)
        self._sr_count += 1
        if self._sr_count < self.p.streak_period:
            self._sr_seed_g.append(gain)
            self._sr_seed_l.append(loss)
            return float('nan')
        if self._sr_count == self.p.streak_period:
            self._sr_seed_g.append(gain)
            self._sr_seed_l.append(loss)
            self._sr_avg_gain = sum(self._sr_seed_g) / self.p.streak_period
            self._sr_avg_loss = sum(self._sr_seed_l) / self.p.streak_period
        else:
            a = 1.0 / self.p.streak_period
            self._sr_avg_gain = gain * a + self._sr_avg_gain * (1.0 - a)
            self._sr_avg_loss = loss * a + self._sr_avg_loss * (1.0 - a)
        if self._sr_avg_loss < 1e-10:
            return 100.0
        return 100.0 - 100.0 / (1.0 + self._sr_avg_gain / self._sr_avg_loss)
    def next(self):
        close, prev_close = self.data.close[0], self.data.close[-1]
        ret = (close / prev_close - 1) if prev_close != 0 else 0.0
        if close > prev_close:
            self._streak = self._streak + 1 if self._streak >= 0 else 1
        elif close < prev_close:
            self._streak = self._streak - 1 if self._streak <= 0 else -1
        else:
            self._streak = 0.0
        self._roc_window.append(ret)
        if len(self._roc_window) > self.p.rank_period:
            self._roc_window.pop(0)
        # Percent rank: past rank_period-1 values strictly < ret, divide by (rank_period-1)
        if len(self._roc_window) >= self.p.rank_period:
            past = self._roc_window[:-1]
            rank = sum(1 for r in past if r < ret) / (self.p.rank_period - 1) * 100.0
        else:
            rank = float('nan')
        rsi2 = self._streak_rsi(self._streak)
        self.lines.crsi[0] = (self.rsi3[0] + rsi2 + rank) / 3.0

class BtStrat(bt.Strategy):
    def __init__(self):
        self.crsi = ConnorsRSIInd(self.data)
    def next(self):
        if not self.position and self.crsi.lines.crsi[0] < {oversold}: self.buy()
        elif self.position and self.crsi.lines.crsi[0] > {overbought}: self.close()
""",
    },

    # ── KDJ ──────────────────────────────────────────────────────────────────

    "kdj": {
        "params": {"period": 9, "k_period": 3, "d_period": 3, "oversold": 20.0, "overbought": 80.0},
        "description": "KDJ: K and D both below oversold with K rising → long; K > overbought or J > 100 → exit.",
        "pta_code": """\
stoch = ta.stoch(high, low, close, k={period}, d={k_period}, smooth_k={d_period})
k_col = f'STOCHk_{{{period}}}_{{{k_period}}}_{{{d_period}}}'
d_col = f'STOCHd_{{{period}}}_{{{k_period}}}_{{{d_period}}}'
k = stoch[k_col]
d = stoch[d_col]
j = 3 * k - 2 * d
k_rising = k > k.shift(1)
entries = (k < {oversold}) & (d < {oversold}) & k_rising
exits   = (k > {overbought}) | (j > 100)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        stoch    = bt.indicators.StochasticFull(self.data, period={period}, period_dfast={k_period}, period_dslow={d_period})
        self.stk = stoch.percD
        self.std = stoch.percDSlow
    def next(self):
        j = 3 * self.stk[0] - 2 * self.std[0]
        k_rising = self.stk[0] > self.stk[-1]
        if not self.position and self.stk[0] < {oversold} and self.std[0] < {oversold} and k_rising: self.buy()
        elif self.position and (self.stk[0] > {overbought} or j > 100): self.close()
""",
    },

    # ── Awesome Oscillator ────────────────────────────────────────────────────

    "ao": {
        "params": {"fast": 5, "slow": 34},
        "description": "Awesome Oscillator crosses above 0 → long; below 0 → exit.",
        "pta_code": """\
mid   = (high + low) / 2
ao = mid.rolling({fast}).mean() - mid.rolling({slow}).mean()
entries = cross_above(ao, pd.Series(0.0, index=ao.index))
exits   = cross_below(ao, pd.Series(0.0, index=ao.index))
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        hl2 = (self.data.high + self.data.low) / 2.0
        self.ao = bt.indicators.SMA(hl2, period={fast}) - bt.indicators.SMA(hl2, period={slow})
    def next(self):
        if not self.position and self.ao[-1] <= 0 and self.ao[0] > 0: self.buy()
        elif self.position and self.ao[-1] >= 0 and self.ao[0] < 0:   self.close()
""",
    },

    # ── Williams %R ───────────────────────────────────────────────────────────

    "williams_r_ma": {
        "params": {"wr_period": 14, "ema_period": 50, "oversold": -80.0, "overbought": -20.0},
        "description": "Williams %R crosses above oversold AND price > EMA → long; enters overbought (crosses below -20) → exit.",
        "pta_code": """\
wr  = ta.willr(high, low, close, length={wr_period})
ema = ta.ema(close, length={ema_period})
entries = cross_above(wr, pd.Series({oversold}, index=wr.index)) & (close > ema)
exits   = cross_below(wr, pd.Series({overbought}, index=wr.index))
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.wr  = bt.indicators.WilliamsR(self.data, period={wr_period})
        self.ema = bt.indicators.EMA(self.data.close, period={ema_period})
    def next(self):
        cross_up = self.wr[-1] <= {oversold} and self.wr[0] > {oversold}
        cross_dn = self.wr[-1] >= {overbought} and self.wr[0] < {overbought}
        if not self.position and cross_up and self.data.close[0] > self.ema[0]: self.buy()
        elif self.position and cross_dn: self.close()
""",
    },

    # ── Fisher Transform ──────────────────────────────────────────────────────

    "fisher_crossover": {
        "params": {"period": 10},
        "description": "Fisher transform line crosses above signal → long; below signal → exit.",
        "pta_code": """\
fisher_df = ta.fisher(high, low, length={period})
fisher    = fisher_df[f'FISHERT_{{{period}}}_1']
signal    = fisher_df[f'FISHERTs_{{{period}}}_1']
entries = cross_above(fisher, signal)
exits   = cross_below(fisher, signal)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.fisher_ind = FisherInd(self.data, period={period})
    def next(self):
        fish = self.fisher_ind.lines.fisher
        sig  = self.fisher_ind.lines.signal
        cross_up = fish[-1] <= sig[-1] and fish[0] > sig[0]
        cross_dn = fish[-1] >= sig[-1] and fish[0] < sig[0]
        if not self.position and cross_up: self.buy()
        elif self.position and cross_dn:   self.close()
""",
    },

    # ── Ultimate Oscillator ───────────────────────────────────────────────────

    "uo_reversal": {
        "params": {"fast": 7, "medium": 14, "slow": 28, "oversold": 30.0, "overbought": 70.0},
        "description": "Ultimate Oscillator crosses above oversold → long; crosses above overbought → exit.",
        "pta_code": """\
uo = ta.uo(high, low, close, fast={fast}, medium={medium}, slow={slow})
entries = cross_above(uo, pd.Series({oversold}, index=uo.index))
exits   = uo > {overbought}
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.uo = bt.indicators.UltimateOscillator(self.data, p1={fast}, p2={medium}, p3={slow})
    def next(self):
        cross_up = self.uo[-1] <= {oversold} and self.uo[0] > {oversold}
        if not self.position and cross_up: self.buy()
        elif self.position and self.uo[0] > {overbought}: self.close()
""",
    },

    # ── CMO ───────────────────────────────────────────────────────────────────

    "cmo_zero_cross": {
        "params": {"cmo_period": 14, "ema_period": 50},
        "description": "CMO crosses above 0 AND price > EMA → long; CMO crosses below 0 → exit.",
        "pta_code": """\
cmo = ta.cmo(close, length={cmo_period})
ema = ta.ema(close, length={ema_period})
entries = cross_above(cmo, pd.Series(0.0, index=cmo.index)) & (close > ema)
exits   = cross_below(cmo, pd.Series(0.0, index=cmo.index))
""",
        "bt_code": """\
class CMOInd(bt.Indicator):
    lines = ('cmo',)
    params = (('period', {cmo_period}),)
    def __init__(self):
        diff = self.data.close - self.data.close(-1)
        up   = bt.If(diff > 0, diff, 0.0)
        dn   = bt.If(diff < 0, -diff, 0.0)
        self._up_sum = bt.indicators.SumN(up, period=self.p.period)
        self._dn_sum = bt.indicators.SumN(dn, period=self.p.period)
    def next(self):
        denom = self._up_sum[0] + self._dn_sum[0]
        self.lines.cmo[0] = 100.0 * (self._up_sum[0] - self._dn_sum[0]) / denom if denom != 0 else 0.0

class BtStrat(bt.Strategy):
    def __init__(self):
        self.cmo = CMOInd(self.data, period={cmo_period})
        self.ema = bt.indicators.EMA(self.data.close, period={ema_period})
    def next(self):
        cross_up = self.cmo.lines.cmo[-1] <= 0 and self.cmo.lines.cmo[0] > 0
        cross_dn = self.cmo.lines.cmo[-1] >= 0 and self.cmo.lines.cmo[0] < 0
        if not self.position and cross_up and self.data.close[0] > self.ema[0]: self.buy()
        elif self.position and cross_dn: self.close()
""",
    },

    # ── Vortex ────────────────────────────────────────────────────────────────

    "vortex_trend": {
        "params": {"period": 14},
        "description": "VI+ crosses above VI- → long; VI- crosses above VI+ → exit.",
        "pta_code": """\
vortex_df = ta.vortex(high, low, close, length={period})
vip = vortex_df[f'VTXP_{{{period}}}']
vim = vortex_df[f'VTXM_{{{period}}}']
entries = cross_above(vip, vim)
exits   = cross_above(vim, vip)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        vortex   = bt.indicators.Vortex(self.data, period={period})
        self.vip = vortex.lines.vi_plus
        self.vim = vortex.lines.vi_minus
    def next(self):
        cross_up = self.vip[-1] <= self.vim[-1] and self.vip[0] > self.vim[0]
        cross_dn = self.vim[-1] <= self.vip[-1] and self.vim[0] > self.vip[0]
        if not self.position and cross_up: self.buy()
        elif self.position and cross_dn:   self.close()
""",
    },

    # ── CMF ───────────────────────────────────────────────────────────────────

    "cmf_ema_trend": {
        "params": {"cmf_period": 20, "ema_period": 50, "bull_threshold": 0.1, "bear_threshold": 0.1},
        "description": "CMF > bull_threshold AND price > EMA → long; CMF < -bear_threshold → exit.",
        "pta_code": """\
cmf = ta.cmf(high, low, close, vol, length={cmf_period})
ema = ta.ema(close, length={ema_period})
entries = (cmf > {bull_threshold}) & (close > ema)
exits   = cmf < -{bear_threshold}
""",
        "bt_code": """\
class CMFInd(bt.Indicator):
    lines = ('cmf',)
    params = (('period', {cmf_period}),)
    def __init__(self):
        hl = self.data.high - self.data.low
        mfm = bt.If(hl != 0, (self.data.close - self.data.low - (self.data.high - self.data.close)) / (hl + 1e-10), 0.0)
        mfv = mfm * self.data.volume
        self._mfv_sum = bt.indicators.SumN(mfv, period=self.p.period)
        self._vol_sum = bt.indicators.SumN(self.data.volume, period=self.p.period)
    def next(self):
        self.lines.cmf[0] = self._mfv_sum[0] / self._vol_sum[0] if self._vol_sum[0] != 0 else 0.0

class BtStrat(bt.Strategy):
    def __init__(self):
        self.cmf = CMFInd(self.data, period={cmf_period})
        self.ema = bt.indicators.EMA(self.data.close, period={ema_period})
    def next(self):
        if not self.position and self.cmf.lines.cmf[0] > {bull_threshold} and self.data.close[0] > self.ema[0]: self.buy()
        elif self.position and self.cmf.lines.cmf[0] < -{bear_threshold}: self.close()
""",
    },

    # ── OBV ───────────────────────────────────────────────────────────────────

    "obv_ema_trend": {
        "params": {"obv_ema_period": 20, "price_ema_period": 50},
        "description": "OBV > OBV EMA AND price > price EMA → long; either reverses → exit.",
        "pta_code": """\
obv     = ta.obv(close, vol)
obv_ema = ta.ema(obv, length={obv_ema_period})
p_ema   = ta.ema(close, length={price_ema_period})
entries = (obv > obv_ema) & (close > p_ema)
exits   = (obv < obv_ema) | (close < p_ema)
""",
        "bt_code": """\
class OBVInd(bt.Indicator):
    lines = ('obv',)
    def __init__(self):
        self._obv = 0.0
    def next(self):
        if self.data.close[0] > self.data.close[-1]:
            self._obv += self.data.volume[0]
        elif self.data.close[0] < self.data.close[-1]:
            self._obv -= self.data.volume[0]
        self.lines.obv[0] = self._obv

class BtStrat(bt.Strategy):
    def __init__(self):
        self.obv    = OBVInd(self.data)
        self.obv_ma = bt.indicators.EMA(self.obv.lines.obv, period={obv_ema_period})
        self.p_ema  = bt.indicators.EMA(self.data.close, period={price_ema_period})
    def next(self):
        if not self.position and self.obv.lines.obv[0] > self.obv_ma[0] and self.data.close[0] > self.p_ema[0]: self.buy()
        elif self.position and (self.obv.lines.obv[0] < self.obv_ma[0] or self.data.close[0] < self.p_ema[0]): self.close()
""",
    },

    # ── MFI ───────────────────────────────────────────────────────────────────

    "mfi_trend": {
        "params": {"period": 14, "bull_threshold": 50.0, "bear_threshold": 40.0},
        "description": "MFI crosses above bull_threshold → long; falls below bear_threshold → exit.",
        "pta_code": """\
mfi = ta.mfi(high, low, close, vol, length={period})
entries = cross_above(mfi, pd.Series({bull_threshold}, index=mfi.index))
exits   = mfi < {bear_threshold}
""",
        "bt_code": """\
class MFIInd(bt.Indicator):
    lines = ('mfi',)
    params = (('period', {period}),)
    def __init__(self):
        tp  = (self.data.high + self.data.low + self.data.close) / 3.0
        mf  = tp * self.data.volume
        pos = bt.If(tp > tp(-1), mf, 0.0)
        neg = bt.If(tp < tp(-1), mf, 0.0)
        self._pos_sum = bt.indicators.SumN(pos, period=self.p.period)
        self._neg_sum = bt.indicators.SumN(neg, period=self.p.period)
    def next(self):
        denom = self._pos_sum[0] + self._neg_sum[0]
        self.lines.mfi[0] = 100.0 * self._pos_sum[0] / denom if denom != 0 else 50.0

class BtStrat(bt.Strategy):
    def __init__(self):
        self.mfi = MFIInd(self.data, period={period})
    def next(self):
        cross_up = self.mfi.lines.mfi[-1] <= {bull_threshold} and self.mfi.lines.mfi[0] > {bull_threshold}
        if not self.position and cross_up: self.buy()
        elif self.position and self.mfi.lines.mfi[0] < {bear_threshold}: self.close()
""",
    },

    "mfi_revert": {
        "params": {"period": 14, "oversold": 20.0, "overbought": 80.0},
        "description": "MFI crosses above oversold (recovery) → long; crosses above overbought → exit.",
        "pta_code": """\
mfi = ta.mfi(high, low, close, vol, length={period})
entries = cross_above(mfi, pd.Series({oversold}, index=mfi.index))
exits   = cross_above(mfi, pd.Series({overbought}, index=mfi.index))
""",
        "bt_code": """\
class MFIInd2(bt.Indicator):
    lines = ('mfi',)
    params = (('period', {period}),)
    def __init__(self):
        tp  = (self.data.high + self.data.low + self.data.close) / 3.0
        mf  = tp * self.data.volume
        pos = bt.If(tp > tp(-1), mf, 0.0)
        neg = bt.If(tp < tp(-1), mf, 0.0)
        self._pos_sum = bt.indicators.SumN(pos, period=self.p.period)
        self._neg_sum = bt.indicators.SumN(neg, period=self.p.period)
    def next(self):
        denom = self._pos_sum[0] + self._neg_sum[0]
        self.lines.mfi[0] = 100.0 * self._pos_sum[0] / denom if denom != 0 else 50.0

class BtStrat(bt.Strategy):
    def __init__(self):
        self.mfi = MFIInd2(self.data, period={period})
    def next(self):
        cross_above_os = self.mfi.lines.mfi[-1] <= {oversold} and self.mfi.lines.mfi[0] > {oversold}
        cross_above_ob = self.mfi.lines.mfi[-1] <= {overbought} and self.mfi.lines.mfi[0] > {overbought}
        if not self.position and cross_above_os: self.buy()
        elif self.position and cross_above_ob:   self.close()
""",
    },

    # ── Waddah Attar ──────────────────────────────────────────────────────────

    "waddah_attar": {
        "params": {"fast": 12, "slow": 26, "bb_period": 20, "bb_std": 2.0},
        "description": "Waddah Attar explosion: (MACD diff × 150) > BB width AND hist > 0 → long.",
        "pta_code": """\
macd_df  = ta.macd(close, fast={fast}, slow={slow}, signal=9)
macd_line = macd_df[f'MACD_{{{fast}}}_{{{slow}}}_9']
hist      = macd_df[f'MACDh_{{{fast}}}_{{{slow}}}_9']
bb        = ta.bbands(close, length={bb_period}, std={bb_std})
bb_bw     = bb.filter(like='BBU').iloc[:,0] - bb.filter(like='BBL').iloc[:,0]
explosion = (macd_line - macd_line.shift(1)) * 150
entries   = (explosion > bb_bw) & (hist > 0)
exits     = (explosion < bb_bw) | (hist < 0)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        macd     = bt.indicators.MACDHisto(self.data.close, period_me1={fast}, period_me2={slow}, period_signal=9)
        self.macd_line = macd.lines.macd
        self.hist      = macd.lines.histo
        bb = bt.indicators.BollingerBands(self.data.close, period={bb_period}, devfactor={bb_std})
        self.bbu = bb.lines.top
        self.bbl = bb.lines.bot
    def next(self):
        explosion = (self.macd_line[0] - self.macd_line[-1]) * 150
        bb_bw     = self.bbu[0] - self.bbl[0]
        if not self.position and explosion > bb_bw and self.hist[0] > 0: self.buy()
        elif self.position and (explosion < bb_bw or self.hist[0] < 0):  self.close()
""",
    },

    # ── Multi-indicator ───────────────────────────────────────────────────────

    "oscillator_overlord": {
        "params": {"rsi_period": 14, "stoch_k": 14, "stoch_d": 3, "cci_period": 20},
        "description": "Majority-vote: RSI, Stoch, CCI all bullish → long; majority bearish → exit.",
        "pta_code": """\
rsi   = ta.rsi(close, length={rsi_period})
hh    = high.rolling({stoch_k}).max()
ll    = low.rolling({stoch_k}).min()
stk   = (close - ll) / (hh - ll + 1e-10) * 100.0
cci   = pta_cci(high, low, close, length={cci_period})
rsi_os = rsi < 30.0
stk_os = stk < 20.0
cci_os = cci < -100.0
rsi_ob = rsi > 70.0
stk_ob = stk > 80.0
cci_ob = cci > 100.0
entries = (rsi_os.astype(int) + stk_os.astype(int) + cci_os.astype(int)) >= 2
exits   = (rsi_ob.astype(int) + stk_ob.astype(int) + cci_ob.astype(int)) >= 2
""",
        "bt_code": """\
class StochasticRaw(bt.Indicator):
    lines = ('k',)
    params = (('period', {stoch_k}),)
    def __init__(self):
        hh = bt.indicators.Highest(self.data.high, period=self.p.period)
        ll = bt.indicators.Lowest(self.data.low, period=self.p.period)
        self.lines.k = 100.0 * (self.data.close - ll) / (hh - ll + 1e-10)

class StandardCCI(bt.Indicator):
    lines = ('cci',)
    params = (('period', {cci_period}), ('factor', 0.015),)
    def __init__(self):
        self.tp = (self.data.high + self.data.low + self.data.close) / 3.0
        self.tp_sma = bt.indicators.SMA(self.tp, period=self.p.period)
    def next(self):
        tps = [self.tp[-i] for i in range(self.p.period)]
        sma = self.tp_sma[0]
        mad = sum(abs(x - sma) for x in tps) / self.p.period
        if mad == 0.0:
            self.lines.cci[0] = 0.0
        else:
            self.lines.cci[0] = (self.tp[0] - sma) / (self.p.factor * mad)

class BtStrat(bt.Strategy):
    def __init__(self):
        self.rsi  = bt.indicators.RSI(self.data.close, period={rsi_period})
        self.stk_ind = StochasticRaw(self.data, period={stoch_k})
        self.stk  = self.stk_ind.lines.k
        self.cci  = StandardCCI(self.data, period={cci_period})
    def next(self):
        os_count = int(self.rsi[0] < 30.0) + int(self.stk[0] < 20.0) + int(self.cci[0] < -100.0)
        ob_count = int(self.rsi[0] > 70.0) + int(self.stk[0] > 80.0) + int(self.cci[0] > 100.0)
        if not self.position and os_count >= 2: self.buy()
        elif self.position and ob_count >= 2:   self.close()
""",
    },

    "equilibrium_explorer": {
        "params": {"ema_period": 200, "stoch_k": 14, "stoch_d": 3, "stoch_oversold": 20.0,
                   "stoch_overbought": 80.0, "macd_fast": 12, "macd_slow": 26, "macd_signal": 9},
        "description": "Price > 200 EMA + Stoch oversold recovery + MACD hist positive → long.",
        "pta_code": """\
ema   = ta.ema(close, length={ema_period})
stoch = ta.stoch(high, low, close, k={stoch_k}, d={stoch_d}, smooth_k=1)
k_col = f'STOCHk_{{{stoch_k}}}_{{{stoch_d}}}_1'
stk   = stoch[k_col]
macd_df = ta.macd(close, fast={macd_fast}, slow={macd_slow}, signal={macd_signal})
hist    = macd_df[f'MACDh_{{{macd_fast}}}_{{{macd_slow}}}_{{{macd_signal}}}']
entries = (close > ema) & cross_above(stk, pd.Series({stoch_oversold}, index=stk.index)) & (hist > 0)
exits   = (close < ema) | (stk > {stoch_overbought})
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.ema  = bt.indicators.EMA(self.data.close, period={ema_period})
        stoch     = bt.indicators.StochasticFull(self.data, period={stoch_k}, period_dfast={stoch_d})
        self.stk  = stoch.percK
        macd      = bt.indicators.MACDHisto(self.data.close, period_me1={macd_fast}, period_me2={macd_slow}, period_signal={macd_signal})
        self.hist = macd.lines.histo
    def next(self):
        stk_cross = self.stk[-1] <= {stoch_oversold} and self.stk[0] > {stoch_oversold}
        if not self.position and self.data.close[0] > self.ema[0] and stk_cross and self.hist[0] > 0: self.buy()
        elif self.position and (self.data.close[0] < self.ema[0] or self.stk[0] > {stoch_overbought}): self.close()
""",
    },

    "trend_follower": {
        "params": {"fast_ma": 50, "slow_ma": 200, "macd_fast": 12, "macd_slow": 26, "macd_signal": 9},
        "description": "SMA(50) > SMA(200) AND MACD hist > 0 → long; either reverses → exit.",
        "pta_code": """\
fast = ta.sma(close, length={fast_ma})
slow = ta.sma(close, length={slow_ma})
macd_df = ta.macd(close, fast={macd_fast}, slow={macd_slow}, signal={macd_signal})
hist    = macd_df[f'MACDh_{{{macd_fast}}}_{{{macd_slow}}}_{{{macd_signal}}}']
entries = (fast > slow) & (hist > 0)
exits   = (fast < slow) | (hist < 0)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.fast = bt.indicators.SMA(self.data.close, period={fast_ma})
        self.slow = bt.indicators.SMA(self.data.close, period={slow_ma})
        macd = bt.indicators.MACDHisto(self.data.close, period_me1={macd_fast}, period_me2={macd_slow}, period_signal={macd_signal})
        self.hist = macd.lines.histo
    def next(self):
        if not self.position and self.fast[0] > self.slow[0] and self.hist[0] > 0: self.buy()
        elif self.position and (self.fast[0] < self.slow[0] or self.hist[0] < 0): self.close()
""",
    },

    # ── Price Action ──────────────────────────────────────────────────────────

    "price_action_swing": {
        "params": {"swing_bars": 5, "ema_trend": 50, "atr_period": 14},
        "description": "Swing low near EMA support → long; swing high or price drops below EMA → exit.",
        "pta_code": """\
ema = ta.ema(close, length={ema_trend})
atr = ta.atr(high, low, close, length={atr_period})
entries_list = []
exits_list = []
swing_high = float('-inf')
swing_low = float('inf')
in_position = False
stop_level = 0.0
swing_bars = {swing_bars}
cap = swing_bars * 2 + 1
window = []
for i in range(len(close)):
    curr_close = close.iloc[i]
    curr_high = high.iloc[i]
    curr_low = low.iloc[i]
    curr_open = O.iloc[i]
    curr_ema = ema.iloc[i]
    curr_atr = atr.iloc[i]
    window.append({{"high": curr_high, "low": curr_low, "close": curr_close, "open": curr_open}})
    if len(window) > cap:
        window.pop(0)
    entry = False
    exit = False
    if len(window) == cap and not pd.isna(curr_ema) and not pd.isna(curr_atr):
        mid_idx = swing_bars
        mid_high = window[mid_idx]["high"]
        is_ph = True
        for j in range(swing_bars):
            if window[j]["high"] >= mid_high or window[2 * swing_bars - j]["high"] >= mid_high:
                is_ph = False
                break
        if is_ph:
            swing_high = mid_high
        mid_low = window[mid_idx]["low"]
        is_pl = True
        for j in range(swing_bars):
            if window[j]["low"] <= mid_low or window[2 * swing_bars - j]["low"] <= mid_low:
                is_pl = False
                break
        if is_pl:
            swing_low = mid_low
        if swing_high != float('-inf') and swing_low != float('inf'):
            above_trend = curr_close > curr_ema
            if not in_position:
                if curr_close > swing_high and above_trend:
                    in_position = True
                    stop_level = swing_low
                    entry = True
                else:
                    body = abs(curr_close - curr_open)
                    lower_wick = min(curr_open, curr_close) - curr_low
                    upper_wick = curr_high - max(curr_open, curr_close)
                    is_pin = body > 0.0 and lower_wick > 2.0 * body and upper_wick < body
                    near_support = abs(curr_close - swing_low) < curr_atr * 0.5
                    if is_pin and near_support and above_trend:
                        in_position = True
                        stop_level = curr_low - curr_atr * 0.5
                        entry = True
            else:
                if curr_close < stop_level:
                    in_position = False
                    exit = True
                else:
                    if swing_low > stop_level:
                        stop_level = swing_low
    entries_list.append(entry)
    exits_list.append(exit)
entries = pd.Series(entries_list, index=close.index)
exits = pd.Series(exits_list, index=close.index)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.ema = bt.indicators.EMA(self.data.close, period={ema_trend})
        self.atr = bt.indicators.ATR(self.data, period={atr_period})
        self.window = []
        self.swing_high = float('-inf')
        self.swing_low = float('inf')
        self.stop_level = 0.0
    def next(self):
        if len(self.ema) < {ema_trend} or len(self.atr) < {atr_period}:
            return
        self.window.append({{
            "high": self.data.high[0],
            "low": self.data.low[0],
            "close": self.data.close[0],
            "open": self.data.open[0]
        }})
        cap = {swing_bars} * 2 + 1
        if len(self.window) > cap:
            self.window.pop(0)
        if len(self.window) < cap:
            return
        mid_idx = {swing_bars}
        mid_high = self.window[mid_idx]["high"]
        is_ph = True
        for j in range({swing_bars}):
            if self.window[j]["high"] >= mid_high or self.window[2 * {swing_bars} - j]["high"] >= mid_high:
                is_ph = False
                break
        if is_ph:
            self.swing_high = mid_high
        mid_low = self.window[mid_idx]["low"]
        is_pl = True
        for j in range({swing_bars}):
            if self.window[j]["low"] <= mid_low or self.window[2 * {swing_bars} - j]["low"] <= mid_low:
                is_pl = False
                break
        if is_pl:
            self.swing_low = mid_low
        if self.swing_high == float('-inf') or self.swing_low == float('inf'):
            return
        above_trend = self.data.close[0] > self.ema[0]
        if not self.position:
            if self.data.close[0] > self.swing_high and above_trend:
                self.buy()
                self.stop_level = self.swing_low
            else:
                curr_close = self.data.close[0]
                curr_open = self.data.open[0]
                curr_high = self.data.high[0]
                curr_low = self.data.low[0]
                body = abs(curr_close - curr_open)
                lower_wick = min(curr_open, curr_close) - curr_low
                upper_wick = curr_high - max(curr_open, curr_close)
                is_pin = body > 0.0 and lower_wick > 2.0 * body and upper_wick < body
                near_support = abs(curr_close - self.swing_low) < self.atr[0] * 0.5
                if is_pin and near_support and above_trend:
                    self.buy()
                    self.stop_level = curr_low - self.atr[0] * 0.5
        else:
            if self.data.close[0] < self.stop_level:
                self.close()
            else:
                if self.swing_low > self.stop_level:
                    self.stop_level = self.swing_low
""",
    },

    # ── Breakout ──────────────────────────────────────────────────────────────

    "orb_breakout": {
        "params": {"range_bars": 30, "session_gap_mins": 60},
        "description": "ORB: break above opening range high (first N bars) → long; below range low → exit.",
        "pta_code": """\
time_diff = close.index.to_series().diff().dt.total_seconds() / 60.0
is_new_session = time_diff.fillna(True) > {session_gap_mins}
session_id = is_new_session.cumsum()
cumcount = close.groupby(session_id).cumcount()
high_in_session = high.groupby(session_id).expanding().max().reset_index(level=0, drop=True)
low_in_session = low.groupby(session_id).expanding().min().reset_index(level=0, drop=True)
or_high_val = high_in_session.where(cumcount == {range_bars} - 1).groupby(session_id).ffill()
or_low_val = low_in_session.where(cumcount == {range_bars} - 1).groupby(session_id).ffill()
range_ready = cumcount >= {range_bars}
entries = range_ready & (close > or_high_val)
exits   = close < or_low_val
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.last_ts = None
        self.bars_in_session = 0
        self.or_high = float('-inf')
        self.or_low = float('inf')
        self.range_ready = False
    def next(self):
        curr_dt = self.data.datetime.datetime(0)
        is_new_session = False
        if self.last_ts is None:
            is_new_session = True
        else:
            diff_mins = (curr_dt - self.last_ts).total_seconds() / 60.0
            if diff_mins > {session_gap_mins}:
                is_new_session = True
        self.last_ts = curr_dt
        if is_new_session:
            self.bars_in_session = 0
            self.or_high = float('-inf')
            self.or_low = float('inf')
            self.range_ready = False
        if not self.range_ready:
            self.bars_in_session += 1
            if self.data.high[0] > self.or_high:
                self.or_high = self.data.high[0]
            if self.data.low[0] < self.or_low:
                self.or_low = self.data.low[0]
            if self.bars_in_session >= {range_bars}:
                self.range_ready = True
            return
        if not self.position and self.data.close[0] > self.or_high:
            self.buy()
        elif self.position and self.data.close[0] < self.or_low:
            self.close()
""",
    },

    "highest_breakout": {
        "params": {"period": 20},
        "description": "Close breaks above N-bar highest close → long; below N-bar lowest close → exit.",
        "pta_code": """\
period = {period}
prev_closes   = close.shift(1)

highest = prev_closes.rolling(period).max()
lowest  = prev_closes.rolling(period).min()
entries = close > highest
exits   = close < lowest
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.hh = bt.indicators.Highest(self.data.close, period={period})
        self.ll = bt.indicators.Lowest(self.data.close,  period={period})
    def next(self):
        highest = self.hh[-1]
        lowest  = self.ll[-1]
        if not self.position and self.data.close[0] > highest: self.buy()
        elif self.position and self.data.close[0] < lowest:    self.close()
""",
    },

    "donchian_breakout": {
        "params": {"entry": 20, "exit": 10},
        "description": "Close breaks above Donchian upper of entry period → long; below lower of exit period → exit.",
        "pta_code": """\
dc_entry = ta.donchian(high, low, lower_length={entry}, upper_length={entry})
dc_exit = ta.donchian(high, low, lower_length={exit}, upper_length={exit})
upper_entry = dc_entry['DCU_{entry}_{entry}']
lower_exit = dc_exit['DCL_{exit}_{exit}']
entries = close > upper_entry.shift(1)
exits   = close < lower_exit.shift(1)
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.entry_high = bt.indicators.Highest(self.data.high, period={entry})
        self.exit_low = bt.indicators.Lowest(self.data.low, period={exit})
    def next(self):
        if not self.position and self.data.close[0] > self.entry_high[-1]: self.buy()
        elif self.position and self.data.close[0] < self.exit_low[-1]:     self.close()
""",
    },

    "keltner_breakout": {
        "params": {"period": 20, "atr_period": 10, "multiplier": 2.0},
        "description": "Close breaks above Keltner upper → long; below lower → exit.",
        "pta_code": """\
kc = ta.kc(high, low, close, length={period}, scalar={multiplier})
kc_upper = kc[f'KCUe_{{{period}}}_{{{multiplier}}}']
kc_lower = kc[f'KCLe_{{{period}}}_{{{multiplier}}}']
entries = close > kc_upper
exits   = close < kc_lower
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        self.ema = bt.indicators.EMA(self.data.close, period={period})
        self.atr = bt.indicators.ATR(self.data, period={atr_period})
    def next(self):
        kc_upper = self.ema[0] + {multiplier} * self.atr[0]
        kc_lower = self.ema[0] - {multiplier} * self.atr[0]
        if not self.position and self.data.close[0] > kc_upper: self.buy()
        elif self.position and self.data.close[0] < kc_lower:   self.close()
""",
    },

    # ── RWI ───────────────────────────────────────────────────────────────────

    "rwi": {
        "params": {"period": 14, "threshold": 1.0},
        "description": "RWI high > threshold → trending up → long; RWI low > threshold (trending down) → exit.",
        "pta_code": """\
tr = pd.concat([
    high - low,
    (high - close.shift(1)).abs(),
    (low - close.shift(1)).abs()
], axis=1).max(axis=1)
rwi_h = pd.Series(0.0, index=close.index)
rwi_l = pd.Series(0.0, index=close.index)
for n in range(2, {period} + 1):
    sqrt_n = n ** 0.5
    atr_n = tr.rolling(n).mean()
    rh = (high - low.shift(n)) / (atr_n * sqrt_n)
    rl = (high.shift(n) - low) / (atr_n * sqrt_n)
    rwi_h = pd.concat([rwi_h, rh], axis=1).max(axis=1)
    rwi_l = pd.concat([rwi_l, rl], axis=1).max(axis=1)
entries = rwi_h > {threshold}
exits   = (rwi_l > {threshold}) & (~entries)
""",
        "bt_code": """\
class RwiInd(bt.Indicator):
    lines = ('rwi_high', 'rwi_low',)
    params = (('period', {period}),)
    def __init__(self):
        self.tr = bt.indicators.TrueRange(self.data)
    def next(self):
        if len(self) < self.p.period + 1:
            self.lines.rwi_high[0] = 0.0
            self.lines.rwi_low[0] = 0.0
            return
        rwi_h = 0.0
        rwi_l = 0.0
        for n in range(2, self.p.period + 1):
            tr_sum = sum(self.tr[-i] for i in range(n))
            atr = tr_sum / n
            if atr <= 1e-10:
                continue
            past_low = self.data.low[-n]
            past_high = self.data.high[-n]
            sqrt_n = n ** 0.5
            rh = (self.data.high[0] - past_low) / (atr * sqrt_n)
            rl = (past_high - self.data.low[0]) / (atr * sqrt_n)
            rwi_h = max(rwi_h, rh)
            rwi_l = max(rwi_l, rl)
        self.lines.rwi_high[0] = rwi_h
        self.lines.rwi_low[0] = rwi_l

class BtStrat(bt.Strategy):
    def __init__(self):
        self.rwi = RwiInd(self.data, period={period})
    def next(self):
        if self.rwi.lines.rwi_high[0] > {threshold}:
            if not self.position:
                self.buy()
        elif self.rwi.lines.rwi_low[0] > {threshold}:
            if self.position:
                self.close()
""",
    },

    # ── KAMA ──────────────────────────────────────────────────────────────────

    "kama": {
        "params": {"er_period": 10, "fast": 2, "slow": 30},
        "description": "KAMA crossover (price > KAMA from below → long; price crosses below KAMA → exit).",
        "pta_code": """\
kama = ta.kama(close, length={er_period})
entries = cross_above(close, kama)
exits   = cross_below(close, kama)
""",
        "bt_code": """\
class KamaInd(bt.Indicator):
    lines = ('kama',)
    params = (('er_period', {er_period}), ('fast', {fast}), ('slow', {slow}),)
    def __init__(self):
        self.fast_sc = 2.0 / (self.p.fast + 1.0)
        self.slow_sc = 2.0 / (self.p.slow + 1.0)
    def next(self):
        if len(self) < self.p.er_period + 1:
            self.lines.kama[0] = float('nan')
            return
        if len(self) == self.p.er_period + 1:
            self.lines.kama[0] = self.data[0]
            return
        close = self.data[0]
        oldest = self.data[-self.p.er_period]
        direction = abs(close - oldest)
        volatility = sum(abs(self.data[-i] - self.data[-i-1]) for i in range(self.p.er_period))
        if volatility == 0.0:
            er = 0.0
        else:
            er = max(0.0, min(1.0, direction / volatility))
        sc = (er * (self.fast_sc - self.slow_sc) + self.slow_sc) ** 2
        prev_kama = self.lines.kama[-1]
        self.lines.kama[0] = prev_kama + sc * (close - prev_kama)

class BtStrat(bt.Strategy):
    def __init__(self):
        self.kama = KamaInd(self.data.close, er_period={er_period}, fast={fast}, slow={slow})
    def next(self):
        cross_up = self.data.close[-1] <= self.kama[-1] and self.data.close[0] > self.kama[0]
        cross_dn = self.data.close[-1] >= self.kama[-1] and self.data.close[0] < self.kama[0]
        if not self.position and cross_up: self.buy()
        elif self.position and cross_dn:   self.close()
""",
    },

    # ── SMI Reversal ─────────────────────────────────────────────────────────

    "smi_reversal": {
        "params": {"period": 13, "smooth1": 25, "smooth2": 2, "signal_period": 9, "oversold": -40.0, "overbought": 40.0},
        "description": "SMI crosses above oversold → long; crosses above overbought → exit.",
        "pta_code": """\
hh = high.rolling({period}).max()
ll = low.rolling({period}).min()
midpoint = (hh + ll) / 2.0
d = close - midpoint
ds = ta.ema(ta.ema(d, length={smooth1}), length={smooth2})
dhl = ta.ema(ta.ema(hh - ll, length={smooth1}), length={smooth2})
smi = 100 * ds / (0.5 * dhl + 1e-10)
signal = ta.ema(smi, length={signal_period})
entries = cross_above(smi, signal) & (smi.shift(1) < {oversold})
exits   = (smi > {overbought}) | cross_below(smi, signal)
""",
        "bt_code": """\
class SMIInd(bt.Indicator):
    lines = ('smi', 'signal',)
    params = (('period', {period}), ('smooth1', {smooth1}), ('smooth2', {smooth2}), ('signal_period', {signal_period}),)
    def __init__(self):
        hh = bt.indicators.Highest(self.data.high, period=self.p.period)
        ll = bt.indicators.Lowest(self.data.low, period=self.p.period)
        midpoint = (hh + ll) / 2.0
        d = self.data.close - midpoint
        ds = bt.indicators.EMA(bt.indicators.EMA(d, period=self.p.smooth1), period=self.p.smooth2)
        hl = hh - ll
        dhl = bt.indicators.EMA(bt.indicators.EMA(hl, period=self.p.smooth1), period=self.p.smooth2)
        self.lines.smi = 100.0 * ds / (0.5 * dhl + 1e-10)
        self.lines.signal = bt.indicators.EMA(self.lines.smi, period=self.p.signal_period)

class BtStrat(bt.Strategy):
    def __init__(self):
        self.smi = SMIInd(self.data)
    def next(self):
        if len(self) < 2: return
        crossed_above = self.smi.lines.smi[-1] <= self.smi.lines.signal[-1] and self.smi.lines.smi[0] > self.smi.lines.signal[0]
        crossed_below = self.smi.lines.smi[-1] >= self.smi.lines.signal[-1] and self.smi.lines.smi[0] < self.smi.lines.signal[0]
        if not self.position and crossed_above and self.smi.lines.smi[-1] < {oversold}: self.buy()
        elif self.position and (self.smi.lines.smi[0] > {overbought} or crossed_below): self.close()
""",
    },

    # ── PPO Histogram ─────────────────────────────────────────────────────────

    "ppo_histogram": {
        "params": {"fast": 12, "slow": 26, "signal": 9},
        "description": "PPO histogram crosses above 0 → long; below 0 → exit.",
        "pta_code": """\
ppo_df = ta.ppo(close, fast={fast}, slow={slow}, signal={signal})
hist   = ppo_df[f'PPOh_{{{fast}}}_{{{slow}}}_{{{signal}}}']
entries = cross_above(hist, pd.Series(0.0, index=hist.index))
exits   = cross_below(hist, pd.Series(0.0, index=hist.index))
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        fast_ema   = bt.indicators.EMA(self.data.close, period={fast})
        slow_ema   = bt.indicators.EMA(self.data.close, period={slow})
        # PPO = (fast_ema - slow_ema) / slow_ema * 100
        ppo        = (fast_ema - slow_ema) / (slow_ema + 1e-10) * 100
        sig        = bt.indicators.EMA(ppo, period={signal})
        self.hist  = ppo - sig
    def next(self):
        if not self.position and self.hist[-1] <= 0 and self.hist[0] > 0: self.buy()
        elif self.position and self.hist[-1] >= 0 and self.hist[0] < 0:   self.close()
""",
    },

    # ── VWMA + RSI ────────────────────────────────────────────────────────────

    "vwma_rsi": {
        "params": {"vwma_period": 20, "rsi_period": 14, "rsi_entry": 50.0, "rsi_exit": 45.0},
        "description": "Price > VWMA AND RSI > rsi_entry → long; RSI < rsi_exit → exit.",
        "pta_code": """\
vwma = ta.vwma(close, vol, length={vwma_period})
rsi  = ta.rsi(close, length={rsi_period})
entries = (close > vwma) & (rsi > {rsi_entry})
exits   = rsi < {rsi_exit}
""",
        "bt_code": """\
class VWMAInd(bt.Indicator):
    lines = ('vwma',)
    params = (('period', {vwma_period}),)
    def __init__(self):
        pv = self.data.close * self.data.volume
        self._pv_sum  = bt.indicators.SumN(pv,                  period=self.p.period)
        self._vol_sum = bt.indicators.SumN(self.data.volume, period=self.p.period)
    def next(self):
        self.lines.vwma[0] = self._pv_sum[0] / self._vol_sum[0] if self._vol_sum[0] != 0 else self.data.close[0]

class BtStrat(bt.Strategy):
    def __init__(self):
        self.vwma = VWMAInd(self.data, period={vwma_period})
        self.rsi  = bt.indicators.RSI(self.data.close, period={rsi_period})
    def next(self):
        if not self.position and self.data.close[0] > self.vwma.lines.vwma[0] and self.rsi[0] > {rsi_entry}: self.buy()
        elif self.position and self.rsi[0] < {rsi_exit}: self.close()
""",
    },

    "pixel_3": {
        "params": {},
        "description": "Pixel 3 strategy based on rolling high/low midpoint thresholds.",
        "pta_code": """\
mid5 = (high.rolling(5).max() + low.rolling(5).min()) / 2
mid20 = (high.rolling(20).max() + low.rolling(20).min()) / 2
mid60 = (high.rolling(60).max() + low.rolling(60).min()) / 2
ts5 = close > mid5
ts4 = close > mid20
ts3 = close > mid60
green_count = ts5.astype(int) + ts4.astype(int) + ts3.astype(int)
entries = green_count >= 2
exits = green_count == 0
""",
        "bt_code": """\
class BtStrat(bt.Strategy):
    def __init__(self):
        hh5 = bt.indicators.Highest(self.data.high, period=5)
        ll5 = bt.indicators.Lowest(self.data.low, period=5)
        self.mid5 = (hh5 + ll5) / 2.0
        
        hh20 = bt.indicators.Highest(self.data.high, period=20)
        ll20 = bt.indicators.Lowest(self.data.low, period=20)
        self.mid20 = (hh20 + ll20) / 2.0
        
        hh60 = bt.indicators.Highest(self.data.high, period=60)
        ll60 = bt.indicators.Lowest(self.data.low, period=60)
        self.mid60 = (hh60 + ll60) / 2.0
        
    def next(self):
        ts5 = self.data.close[0] > self.mid5[0]
        ts4 = self.data.close[0] > self.mid20[0]
        ts3 = self.data.close[0] > self.mid60[0]
        green_count = int(ts5) + int(ts4) + int(ts3)
        if not self.position and green_count >= 2:
            self.buy()
        elif self.position and green_count == 0:
            self.close()
""",
    },

}


# ─── COMMON PREAMBLE ───────────────────────────────────────────────────────────

PREAMBLE_CODE = """\
%matplotlib inline
import sys, pathlib, warnings
warnings.filterwarnings('ignore')
sys.path.insert(0, str(pathlib.Path.cwd()))

import numpy as np, pandas as pd
import pandas_ta as ta
import backtrader as bt
import alm_py
import matplotlib.pyplot as plt
from _shared import load_parquet, vbt_run

CAPITAL, COMM, SLIP = 10_000.0, 0.0, 0.0
try:
    ALM_BARS, DF = load_parquet('BTCUSDT', 'M1', 'testdata', n=20000)
except FileNotFoundError:
    from _shared import make_bars
    ALM_BARS, DF = make_bars(n=20000, trend=0.0008, noise=0.006, seed=42)
    print("WARNING: testdata parquet not found — using synthetic data")

close = DF['close']
high  = DF['high']
low   = DF['low']
vol   = DF['volume']
O     = DF['open']
C = close.values;  H = high.values;  L = low.values;  V = vol.values
print(f"Loaded {len(C)} bars: {DF.index[0].date()} -> {DF.index[-1].date()}")
"""

HELPERS_CODE = """\
def fillna(a):
    s = pd.Series(a, dtype=float)
    return s.ffill().bfill().fillna(0).values

def cross_above(a, b):
    a_s = a if isinstance(a, pd.Series) else pd.Series(a, dtype=float)
    b_s = b if isinstance(b, pd.Series) else pd.Series(b, dtype=float)
    return ((a_s.shift(1) <= b_s.shift(1)) & (a_s > b_s)).fillna(False)

def cross_below(a, b):
    a_s = a if isinstance(a, pd.Series) else pd.Series(a, dtype=float)
    b_s = b if isinstance(b, pd.Series) else pd.Series(b, dtype=float)
    return ((a_s.shift(1) >= b_s.shift(1)) & (a_s < b_s)).fillna(False)

def pta_cci(high, low, close, length=20, factor=0.015):
    tp = (high + low + close) / 3.0
    tp_sma = tp.rolling(length).mean()
    mad = tp.rolling(length).apply(lambda x: np.abs(x - x.mean()).mean(), raw=True)
    return (tp - tp_sma) / (factor * mad + 1e-10)

def pta_stoch_rsi(close, period=14, smooth_k=3):
    delta = close.diff()
    gain = delta.clip(lower=0)
    loss = (-delta).clip(lower=0)
    avg_gain = np.zeros(len(close))
    avg_loss = np.zeros(len(close))
    avg_gain[period] = np.mean(gain.values[1:period+1])
    avg_loss[period] = np.mean(loss.values[1:period+1])
    alpha = 1.0 / period
    for i in range(period + 1, len(close)):
        avg_gain[i] = gain.values[i] * alpha + avg_gain[i-1] * (1.0 - alpha)
        avg_loss[i] = loss.values[i] * alpha + avg_loss[i-1] * (1.0 - alpha)
    rs = avg_gain / (avg_loss + 1e-10)
    rsi = 100.0 - (100.0 / (1.0 + rs))
    rsi = rsi / 100.0
    rsi[:period] = np.nan
    rsi_s = pd.Series(rsi, index=close.index)
    rsi_low = rsi_s.rolling(period).min()
    rsi_high = rsi_s.rolling(period).max()
    raw_k = (rsi_s - rsi_low) / (rsi_high - rsi_low + 1e-10)
    k = raw_k.rolling(smooth_k).mean()
    return k

class CustomPSAR(bt.Indicator):
    lines = ('psar', 'bullish')
    params = (('step', 0.02), ('max_af', 0.2))
    
    def __init__(self):
        self.sar = None
        self.ep = None
        self.af = self.p.step
        self.is_bullish = True
        self.prev_high = None
        self.prev_low = None
        self.prev_high2 = None
        self.prev_low2 = None
        
    def next(self):
        high = self.data.high[0]
        low = self.data.low[0]
        
        if self.prev_high is None:
            self.prev_high = high
            self.prev_low = low
            self.lines.psar[0] = float('nan')
            self.lines.bullish[0] = 0.0
            return
            
        if self.sar is None:
            self.sar = min(self.prev_low, low)
            self.ep = high
            self.is_bullish = True
            self.af = self.p.step
            self.prev_high2 = self.prev_high
            self.prev_low2 = self.prev_low
            self.prev_high = high
            self.prev_low = low
            self.lines.psar[0] = self.sar
            self.lines.bullish[0] = 1.0
            return
            
        prev_sar = self.sar
        prev_ep = self.ep
        
        new_sar = prev_sar + self.af * (prev_ep - prev_sar)
        
        if self.is_bullish:
            new_sar = min(new_sar, self.prev_low)
            if self.prev_low2 is not None:
                new_sar = min(new_sar, self.prev_low2)
            if low < new_sar:
                self.is_bullish = False
                new_sar = prev_ep
                self.ep = low
                self.af = self.p.step
            else:
                if high > prev_ep:
                    self.ep = high
                    self.af = min(self.af + self.p.step, self.p.max_af)
                else:
                    self.ep = prev_ep
        else:
            new_sar = max(new_sar, self.prev_high)
            if self.prev_high2 is not None:
                new_sar = max(new_sar, self.prev_high2)
            if high > new_sar:
                self.is_bullish = True
                new_sar = prev_ep
                self.ep = high
                self.af = self.p.step
            else:
                if low < prev_ep:
                    self.ep = low
                    self.af = min(self.af + self.p.step, self.p.max_af)
                else:
                    self.ep = prev_ep
                    
        self.sar = new_sar
        self.prev_high2 = self.prev_high
        self.prev_low2 = self.prev_low
        self.prev_high = high
        self.prev_low = low
        
        self.lines.psar[0] = new_sar
        self.lines.bullish[0] = 1.0 if self.is_bullish else 0.0

class CustomAroon(bt.Indicator):
    lines = ('aroonup', 'aroondown',)
    params = (('period', 25),)
    
    def __init__(self):
        self.addminperiod(self.p.period + 1)
        
    def next(self):
        highs = [self.data.high[-i] for i in range(self.p.period + 1)]
        lows = [self.data.low[-i] for i in range(self.p.period + 1)]
        
        max_val = max(highs)
        min_val = min(lows)
        
        hhidx = 0
        for idx, val in enumerate(highs):
            if abs(val - max_val) < 1e-9:
                hhidx = idx
                
        llidx = 0
        for idx, val in enumerate(lows):
            if abs(val - min_val) < 1e-9:
                llidx = idx
                
        self.lines.aroonup[0] = 100.0 * (self.p.period - hhidx) / self.p.period
        self.lines.aroondown[0] = 100.0 * (self.p.period - llidx) / self.p.period

class ResetVWAP(bt.Indicator):
    lines = ('vwap',)
    params = (('session_gap_mins', 60),)
    
    def __init__(self):
        self._tp = (self.data.high + self.data.low + self.data.close) / 3.0
        self._vol = self.data.volume
        self._tp_vol_sum = 0.0
        self._vol_sum = 0.0
        
    def next(self):
        if len(self) > 1:
            dt_now = self.data.datetime.datetime(0)
            dt_prev = self.data.datetime.datetime(-1)
            gap = (dt_now - dt_prev).total_seconds() / 60.0
            if gap > self.p.session_gap_mins:
                self._tp_vol_sum = 0.0
                self._vol_sum = 0.0
                
        self._tp_vol_sum += self._tp[0] * self._vol[0]
        self._vol_sum += self._vol[0]
        self.lines.vwap[0] = self._tp_vol_sum / self._vol_sum if self._vol_sum > 0 else self.data.close[0]

import math as _math
class FisherInd(bt.Indicator):
    lines = ('fisher', 'signal',)
    params = (('period', 10),)
    
    def __init__(self):
        self.midpoint = (self.data.high + self.data.low) / 2.0
        self.hh = bt.indicators.Highest(self.midpoint, period=self.p.period)
        self.ll = bt.indicators.Lowest(self.midpoint, period=self.p.period)
        self.value = 0.0
        self.fisher = 0.0
        
    def next(self):
        mid = self.midpoint[0]
        hh = self.hh[0]
        ll = self.ll[0]
        rng = hh - ll
        
        raw = 2.0 * (mid - ll) / rng - 1.0 if rng > 1e-10 else 0.0
        val = 0.33 * raw + 0.67 * self.value
        val = max(min(val, 0.999), -0.999)
        
        fish = 0.5 * _math.log((1.0 + val) / (1.0 - val)) + 0.5 * self.fisher
        
        self.lines.fisher[0] = fish
        self.lines.signal[0] = self.fisher
        
        self.value = val
        self.fisher = fish

"""

METRICS_TABLE_CODE = """\
import math, plotly.graph_objects as go

# ── helpers ──────────────────────────────────────────────────────────────────
def _na(*_, **__): return 'N/A'

def _f(v, fmt='.3f', suffix=''):
    if v is None or (isinstance(v, float) and math.isnan(v)): return 'N/A'
    if isinstance(v, float) and math.isinf(v): return '∞' if v > 0 else '-∞'
    try: return f'{v:{fmt}}{suffix}'
    except: return str(v)

def _eq_returns(result):
    eq_raw = result.get('equity_curve', [])
    if len(eq_raw) < 2: return np.array([])
    eq_v = np.array([pt['equity'] if isinstance(pt, dict) else pt for pt in eq_raw], float)
    return np.diff(eq_v) / np.where(eq_v[:-1] != 0, eq_v[:-1], np.nan)

def _sortino(result):
    so = result.get('sortino_ratio', float('nan'))
    if not math.isnan(so): return so
    r = _eq_returns(result); neg = r[r < 0]
    return float(r.mean() / neg.std()) if len(neg) > 0 else float('nan')

def _from_trades(result):
    pnls = np.array([t.get('pnl', 0) for t in result.get('trades', [])], float)
    wins = pnls[pnls > 0]; loses = pnls[pnls <= 0]
    avg_win   = float(wins.mean())  if len(wins)  else float('nan')
    avg_loss  = float(loses.mean()) if len(loses) else float('nan')
    pf        = (float(wins.sum() / abs(loses.sum()))
                 if len(loses) and loses.sum() != 0 else float('inf'))
    max_win   = float(wins.max())  if len(wins)  else float('nan')
    max_loss  = float(loses.min()) if len(loses) else float('nan')
    consec_l = result.get('max_consecutive_losses', float('nan'))
    consec_w = result.get('max_consecutive_wins',   float('nan'))
    return avg_win, avg_loss, pf, max_win, max_loss, consec_l, consec_w

def _calmar(result):
    r = result.get('total_return_pct', float('nan'))
    dd = result.get('max_drawdown_pct', float('nan'))
    if math.isnan(r) or math.isnan(dd) or dd == 0: return float('nan')
    return r / dd

def _omega(result):
    r = _eq_returns(result)
    if len(r) == 0: return float('nan')
    pos = r[r > 0].sum(); neg = abs(r[r < 0].sum())
    return float(pos / neg) if neg > 0 else float('inf')

# ── pre-compute per engine ────────────────────────────────────────────────────
_E = [('alm_py', alm_result), ('pandas+vbt', vbt_result), ('backtrader', bt_result)]
_T = {n: _from_trades(r) for n, r in _E}   # (avg_win, avg_loss, pf, max_win, max_loss, cl, cw)
_S = {n: _sortino(r)      for n, r in _E}

# ── metric definitions ────────────────────────────────────────────────────────
# (label, alm_key_or_fn, vbt_fn, bt_fn)
# fn signature: fn(result, trades_extra, sortino) -> str
def _g(key, fmt='.3f', suffix=''):   # get from result dict
    return lambda r, t, s: _f(r.get(key), fmt, suffix)
def _tg(idx, fmt='.2f', suffix=''):  # get from trades_extra tuple
    return lambda r, t, s: _f(t[idx], fmt, suffix)

METRICS = [
    # ── Returns ───────────────────────────────────────────────────────────────
    ('Final Equity',           _g('final_equity',              ',.2f'),        _g('final_equity',              ',.2f'),  _g('final_equity',              ',.2f')),
    ('Return %',               _g('total_return_pct',          '+.2f', '%'),   _g('total_return_pct',          '+.2f','%'),_g('total_return_pct',        '+.2f','%')),
    ('CAGR %',                 _g('cagr_pct',                  '+.2f', '%'),   _na,                                      _g('cagr_pct',                  '+.2f','%')),
    ('Ann. Volatility %',      _g('annualized_volatility_pct', '.2f',  '%'),   _na,                                      _g('annualized_volatility_pct', '.2f','%')),
    # ── Risk-adjusted ─────────────────────────────────────────────────────────
    ('Sharpe',                 _g('sharpe_ratio',    '.3f'),   _g('sharpe_ratio',    '.3f'),   _g('sharpe_ratio',    '.3f')),
    ('Sortino',                _g('sortino_ratio',   '.3f'),   lambda r,t,s: _f(s,  '.3f'),   _g('sortino_ratio',   '.3f')),
    ('Calmar',                 _g('calmar_ratio',    '.3f'),   lambda r,t,s: _f(_calmar(r),'.3f'), _g('calmar_ratio','.3f')),
    ('Omega Ratio',            _g('omega_ratio',     '.3f'),   lambda r,t,s: _f(_omega(r), '.3f'), _g('omega_ratio', '.3f')),
    ('Tail Ratio',             _g('tail_ratio',      '.3f'),   _na,                            _g('tail_ratio',      '.3f')),
    ('Recovery Factor',        _g('recovery_factor', '.3f'),   _na,                            _g('recovery_factor', '.3f')),
    # ── Drawdown ──────────────────────────────────────────────────────────────
    ('Max DD %',               _g('max_drawdown_pct',      '.2f','%'), _g('max_drawdown_pct','.2f','%'), _g('max_drawdown_pct',     '.2f','%')),
    ('Avg DD %',               _g('avg_drawdown_pct',     '.2f','%'), _g('avg_drawdown_pct',     '.2f','%'), _na),
    ('Max DD Duration (bars)', _g('max_dd_duration_bars', 'd'),        _g('max_dd_duration_bars', 'd'),       _g('max_dd_duration_bars', 'd')),
    # ── Trade stats ───────────────────────────────────────────────────────────
    ('Total Trades',           _g('total_trades',  'd'),         _g('total_trades',  'd'),         _g('total_trades',  'd')),
    ('Win Rate',               _g('win_rate_pct',  '.1f','%'),   _g('win_rate_pct',  '.1f','%'),   _g('win_rate_pct',  '.1f','%')),
    ('Profit Factor',          _g('profit_factor', '.3f'),       _g('profit_factor', '.3f'),       _g('profit_factor', '.3f')),
    ('Expectancy',             _g('expectancy',    '.2f'),       _g('expectancy',    '.2f'),       _g('expectancy',    '.2f')),
    ('Avg Win %',              _g('avg_win_pct',   '.2f','%'),   _g('avg_win_pct',   '.2f','%'),   _na),
    ('Avg Loss %',             _g('avg_loss_pct',  '.2f','%'),   _g('avg_loss_pct',  '.2f','%'),   _na),
    ('Avg Win $ (net)',         _na,                              _g('avg_win',  '.2f'),            _g('avg_win_pnl',   '.2f')),
    ('Avg Loss $ (net)',        _na,                              _g('avg_loss', '.2f'),            _g('avg_loss_pnl',  '.2f')),
    ('Largest Win %',          _g('largest_win_pct',  '.2f','%'), _g('largest_win_pct',  '.2f','%'), _na),
    ('Largest Loss %',         _g('largest_loss_pct', '.2f','%'), _g('largest_loss_pct', '.2f','%'), _na),
    ('Largest Win $ (net)',    _na,                               _na,                              _g('largest_win_pnl',  '.2f')),
    ('Largest Loss $ (net)',   _na,                               _na,                              _g('largest_loss_pnl', '.2f')),
    ('Avg Duration (h)',       _g('avg_trade_duration_hours','.1f','h'), _g('avg_trade_duration_hours','.1f','h'), _g('avg_trade_duration_hours','.1f','h')),
    ('Max Consec Wins',        _g('max_consecutive_wins',   'd'), _g('max_consecutive_wins',   'd'), _g('max_consecutive_wins',   'd')),
    ('Max Consec Losses',      _g('max_consecutive_losses', 'd'), _g('max_consecutive_losses', 'd'), _g('max_consecutive_losses', 'd')),
    # ── Distribution ──────────────────────────────────────────────────────────
    ('VaR 95%',         _g('var_95',          '.4f'), _g('var_95',          '.4f'), _g('var_95',          '.4f')),
    ('CVaR 95%',        _g('cvar_95',         '.4f'), _g('cvar_95',         '.4f'), _g('cvar_95',         '.4f')),
    ('Skewness',        _g('skewness',        '.3f'), _g('skewness',        '.3f'), _g('skewness',        '.3f')),
    ('Excess Kurtosis', _g('excess_kurtosis', '.3f'), _g('excess_kurtosis', '.3f'), _g('excess_kurtosis', '.3f')),
    # ── Quality ───────────────────────────────────────────────────────────────
    ('SQN',  _g('sqn', '.3f'), _g('sqn', '.3f'), _g('sqn', '.3f')),
    ('PSR',  _g('psr', '.4f'), _na,              _na),
]

# ── build table data ──────────────────────────────────────────────────────────
_header = ['<b>Metric</b>'] + [f'<b>{n}</b>' for n, _ in _E]
_cols   = [[] for _ in range(len(_E) + 1)]

for _label, *_fns in METRICS:
    _cols[0].append(_label)
    for _ci, ((_ename, _er), _fn) in enumerate(zip(_E, _fns)):
        _cols[_ci + 1].append(_fn(_er, _T[_ename], _S[_ename]))

_n = len(METRICS)
_row_bg  = ['#1a2035' if i % 2 == 0 else '#1e2840' for i in range(_n)]
_all_clr = [['#253550'] * _n] + [_row_bg] * len(_E)  # metric col slightly distinct

_fig_t = go.Figure(go.Table(
    header=dict(
        values=_header,
        fill_color='#162032', font=dict(color='white', size=13),
        align='center', height=36,
    ),
    cells=dict(
        values=_cols,
        fill_color=_all_clr,
        font=dict(color='white', size=12),
        align=['left'] + ['center'] * len(_E),
        height=30,
    ),
))
_fig_t.update_layout(
    title=dict(text=f'<b>{STRATEGY_NAME}</b> — Full Metrics Comparison', font=dict(size=15)),
    width=750 + 200 * len(_E),
    height=60 + 36 + 30 * _n,
    margin=dict(l=10, r=10, t=60, b=10),
    template='plotly_dark',
)
_fig_t.show()
"""

EQUITY_CHART_CODE = """\
import plotly.graph_objects as go
from plotly.subplots import make_subplots

# ── Helper: markers from alm_py trade list ────────────────────────────────────
def _alm_markers(trades, ts_ms):
    ex, ey, wx, wy, wpnl, lx, ly, lpnl = [], [], [], [], [], [], [], []
    for t in trades:
        ei = np.searchsorted(ts_ms, t.get('entry_ts', 0))
        xi = np.searchsorted(ts_ms, t.get('exit_ts', 0))
        pnl = t.get('pnl', 0)
        if ei < len(C): ex.append(DF.index[ei]); ey.append(C[ei])
        if xi < len(C):
            if pnl >= 0: wx.append(DF.index[xi]); wy.append(C[xi]); wpnl.append(pnl)
            else:        lx.append(DF.index[xi]); ly.append(C[xi]); lpnl.append(pnl)
    return ex, ey, wx, wy, wpnl, lx, ly, lpnl

# ── Helper: markers from bt trade list (entry_dt / exit_dt / pnl) ─────────────
def _bt_markers(trades):
    ex, ey, wx, wy, wpnl, lx, ly, lpnl = [], [], [], [], [], [], [], []
    for t in trades:
        ei = DF.index.searchsorted(t['entry_dt'])
        xi = DF.index.searchsorted(t['exit_dt'])
        pnl = t.get('pnl', 0)
        if ei < len(C): ex.append(DF.index[min(ei, len(C)-1)]); ey.append(C[min(ei, len(C)-1)])
        xi = min(xi, len(C)-1)
        if pnl >= 0: wx.append(DF.index[xi]); wy.append(C[xi]); wpnl.append(pnl)
        else:        lx.append(DF.index[xi]); ly.append(C[xi]); lpnl.append(pnl)
    return ex, ey, wx, wy, wpnl, lx, ly, lpnl

# ── Helper: markers from boolean entry/exit arrays ────────────────────────────
def _sig_markers(entries_arr, exits_arr):
    ex = DF.index[entries_arr].tolist(); ey = C[entries_arr].tolist()
    xi = DF.index[exits_arr].tolist();   xy = C[exits_arr].tolist()
    return ex, ey, xi, xy

def _add_price(fig, row):
    fig.add_trace(go.Scatter(
        x=DF.index, y=C, name='price', showlegend=False,
        line=dict(color='#777', width=0.7), opacity=0.4,
    ), row=row, col=1)

def _add_entry(fig, row, x, y, name, color, legend=True):
    if not x: return
    fig.add_trace(go.Scatter(
        x=x, y=y, mode='markers', name=f'{name} entry', showlegend=legend,
        legendgroup=name,
        marker=dict(symbol='triangle-up', color=color, size=9, opacity=0.9,
                    line=dict(width=0.5, color='white')),
    ), row=row, col=1)

def _add_exit(fig, row, wx, wy, wpnl, lx, ly, lpnl, name, legend=True):
    if wx:
        fig.add_trace(go.Scatter(
            x=wx, y=wy, mode='markers', name=f'{name} win', showlegend=legend,
            legendgroup=name,
            marker=dict(symbol='triangle-down', color='limegreen', size=9, opacity=0.9,
                        line=dict(width=0.5, color='white')),
            customdata=wpnl, hovertemplate='PnL: %{customdata:.2f}<extra></extra>',
        ), row=row, col=1)
    if lx:
        fig.add_trace(go.Scatter(
            x=lx, y=ly, mode='markers', name=f'{name} loss', showlegend=legend,
            legendgroup=name,
            marker=dict(symbol='triangle-down', color='tomato', size=9, opacity=0.9,
                        line=dict(width=0.5, color='white')),
            customdata=lpnl, hovertemplate='PnL: %{customdata:.2f}<extra></extra>',
        ), row=row, col=1)

# ── Build subplots ────────────────────────────────────────────────────────────
# Layout: Row 1 = equity curves
#         Row 2 = alm_py + backtrader on SAME price chart (different colors)
#         Row 3 = pandas+vbt signals (separate)
ts_ms      = (DF.index.astype(np.int64) // 10**6).values
alm_trades = alm_result.get('trades', [])
bt_trades  = bt_result.get('trades', [])
has_bt     = bool(bt_trades)

subtitles = [
    "Equity Curves",
    "alm_py 🔵 vs backtrader 🟠  (▲entry  ▼win  ▼loss)" if has_bt else "alm_py trades  (▲entry  ▼win  ▼loss)",
    "pandas+vbt signals  (▲entry  ▼exit)",
]
fig = make_subplots(
    rows=3, cols=1, shared_xaxes=True,
    row_heights=[0.28, 0.42, 0.30],
    vertical_spacing=0.03,
    subplot_titles=subtitles,
)

# ── Row 1: equity curves ──────────────────────────────────────────────────────
alm_eq = [pt['equity'] for pt in alm_result.get('equity_curve', [])]
if alm_eq:
    fig.add_trace(go.Scatter(x=DF.index[-len(alm_eq):], y=alm_eq,
        name='alm_py', line=dict(width=2, color='royalblue')), row=1, col=1)
vbt_eq = vbt_result.get('equity_curve', [])
if vbt_eq:
    fig.add_trace(go.Scatter(x=DF.index[-len(vbt_eq):], y=vbt_eq,
        name='pandas+vbt', line=dict(width=1.5, color='orange'), opacity=0.85), row=1, col=1)
bt_eq = bt_result.get('equity_curve', [])
if bt_eq:
    fig.add_trace(go.Scatter(x=DF.index[-len(bt_eq):], y=bt_eq,
        name='backtrader', line=dict(width=1.5, color='mediumpurple', dash='dot'), opacity=0.85), row=1, col=1)

# ── Row 2: alm_py + backtrader on same price panel ───────────────────────────
_add_price(fig, 2)

# alm_py — blue palette
ex, ey, wx, wy, wpnl, lx, ly, lpnl = _alm_markers(alm_trades, ts_ms)
_add_entry(fig, 2, ex, ey, 'alm_py', 'royalblue')
if wx:
    fig.add_trace(go.Scatter(x=wx, y=wy, mode='markers',
        name='alm_py win', legendgroup='alm_py',
        marker=dict(symbol='triangle-down', color='deepskyblue', size=9, opacity=0.9,
                    line=dict(width=0.5, color='white')),
        customdata=wpnl, hovertemplate='alm_py win PnL: %{customdata:.2f}<extra></extra>',
    ), row=2, col=1)
if lx:
    fig.add_trace(go.Scatter(x=lx, y=ly, mode='markers',
        name='alm_py loss', legendgroup='alm_py',
        marker=dict(symbol='triangle-down', color='steelblue', size=9, opacity=0.9,
                    line=dict(width=0.5, color='white')),
        customdata=lpnl, hovertemplate='alm_py loss PnL: %{customdata:.2f}<extra></extra>',
    ), row=2, col=1)

# backtrader — orange palette
if has_bt:
    bex, bey, bwx, bwy, bwp, blx, bly, blp = _bt_markers(bt_trades)
    _add_entry(fig, 2, bex, bey, 'backtrader', 'darkorange')
    if bwx:
        fig.add_trace(go.Scatter(x=bwx, y=bwy, mode='markers',
            name='bt win', legendgroup='backtrader',
            marker=dict(symbol='triangle-down', color='gold', size=9, opacity=0.9,
                        line=dict(width=0.5, color='white')),
            customdata=bwp, hovertemplate='bt win PnL: %{customdata:.2f}<extra></extra>',
        ), row=2, col=1)
    if blx:
        fig.add_trace(go.Scatter(x=blx, y=bly, mode='markers',
            name='bt loss', legendgroup='backtrader',
            marker=dict(symbol='triangle-down', color='sienna', size=9, opacity=0.9,
                        line=dict(width=0.5, color='white')),
            customdata=blp, hovertemplate='bt loss PnL: %{customdata:.2f}<extra></extra>',
        ), row=2, col=1)

# ── Row 3: pandas+vbt signals ─────────────────────────────────────────────────
_add_price(fig, 3)
try:
    _e = np.asarray(entries, bool); _x = np.asarray(exits, bool)
    vex, vey, vxx, vxy = _sig_markers(_e, _x)
    _add_entry(fig, 3, vex, vey, 'pandas+vbt', 'orange')
    if vxx:
        fig.add_trace(go.Scatter(x=vxx, y=vxy, mode='markers',
            name='vbt exit', legendgroup='pandas+vbt',
            marker=dict(symbol='triangle-down', color='gold', size=9, opacity=0.9,
                        line=dict(width=0.5, color='white')),
        ), row=3, col=1)
except NameError:
    pass

fig.update_layout(
    title=dict(text=f"<b>{STRATEGY_NAME}</b> — Signal Comparison", font=dict(size=16)),
    width=1600, height=850,
    hovermode='x unified',
    template='plotly_dark',
    legend=dict(orientation='h', yanchor='bottom', y=1.01, xanchor='right', x=1),
    margin=dict(l=60, r=30, t=90, b=40),
    dragmode='zoom',
)
fig.update_xaxes(rangeslider_visible=False)
fig.show()
"""


# ─── Notebook builder ─────────────────────────────────────────────────────────

def build_notebook(strategy_name: str, impl: dict) -> dict:
    params      = impl["params"]
    description = impl["description"]
    pta_raw     = impl["pta_code"]
    bt_raw      = impl.get("bt_code")
    rhai_script = RHAI_SCRIPTS.get(strategy_name)

    # Format pta_code with params
    try:
        pta_code = pta_raw.format(**params)
    except (KeyError, ValueError):
        pta_code = pta_raw  # leave as-is if formatting fails

    has_bt = bt_raw is not None

    # Format bt_code
    if has_bt:
        try:
            bt_fmt = bt_raw.format(**params)
        except (KeyError, ValueError):
            bt_fmt = bt_raw

    # alm_py params string
    if params:
        param_str = json.dumps(params)
    else:
        param_str = "{}"

    # Build cells
    cells = [
        md(
            f"# `{strategy_name}` — alm_py vs pandas_ta vs backtrader",
            "",
            description,
            "",
            f"**Default params:** `{param_str}`",
        ),
        code(PREAMBLE_CODE),
        code(
            f"STRATEGY_NAME = '{strategy_name}'",
            HELPERS_CODE,
        ),
        code(
            f"# ── alm_py named strategy run ───────────────────────────────────────",
            f"alm_result = alm_py.run_backtest(",
            f"    'BTCUSDT', '{strategy_name}', {param_str}, ALM_BARS,",
            f"    initial_capital=CAPITAL, commission_pct=COMM, slippage_pct=SLIP, lot_size=0.0, strength_sizing=False",
            f")",
            f"print(f\"alm_py (named): {{alm_result['total_trades']}} trades, "
            f"return={{alm_result['total_return_pct']:.2f}}%, "
            f"sharpe={{alm_result['sharpe_ratio']:.3f}}, "
            f"sortino={{alm_result.get('sortino_ratio', float('nan')):.3f}}, "
            f"maxdd={{alm_result.get('max_drawdown_pct', float('nan')):.2f}}%, "
            f"winrate={{alm_result.get('win_rate_pct', float('nan')):.1f}}%, "
            f"pf={{alm_result.get('profit_factor', float('nan')):.3f}}\")",
        ),
        code(
            f"# ── Native pandas_ta signals → vectorbt ─────────────────────────────",
            pta_code,
            "",
            "entries = np.asarray(entries.fillna(False) if hasattr(entries,'fillna') else entries, bool)",
            "exits   = np.asarray(exits.fillna(False)   if hasattr(exits,  'fillna') else exits,   bool)",
            "print(f'pandas_ta: {entries.sum()} entries, {exits.sum()} exits')",
            "vbt_result = vbt_run(entries, exits, C, capital=CAPITAL, commission=COMM, slippage=SLIP, freq='1min')",
            "print(f\"vectorbt: {vbt_result['total_trades']} trades, "
            "return={vbt_result['total_return_pct']:.2f}%, "
            "sharpe={vbt_result['sharpe_ratio']:.3f}, "
            "sortino={vbt_result.get('sortino_ratio', float('nan')):.3f}, "
            "maxdd={vbt_result.get('max_drawdown_pct', float('nan')):.2f}%, "
            "winrate={vbt_result.get('win_rate_pct', float('nan')):.1f}%, "
            "pf={vbt_result.get('profit_factor', float('nan')):.3f}\")",
        ),
    ]

    # ── Rhai script cell (inserted after named, before pandas_ta) ────────────
    if rhai_script is not None:
        # Escape the script for embedding in a Python triple-quoted string
        escaped = rhai_script.replace("\\", "\\\\").replace('"""', '\\"\\"\\"')
        cells.insert(4, code(
            f"# ── alm_py Rhai script run ──────────────────────────────────────────",
            f"RHAI_SCRIPT = \"\"\"{escaped}\"\"\"",
            f"script_result = alm_py.run_script_backtest(",
            f"    'BTCUSDT', RHAI_SCRIPT, ALM_BARS,",
            f"    initial_capital=CAPITAL, commission_pct=COMM, slippage_pct=SLIP, lot_size=0.0, strength_sizing=False",
            f")",
            f"print(f\"alm_py (script): {{script_result['total_trades']}} trades, "
            f"return={{script_result['total_return_pct']:.2f}}%, "
            f"sharpe={{script_result['sharpe_ratio']:.3f}}, "
            f"sortino={{script_result.get('sortino_ratio', float('nan')):.3f}}, "
            f"maxdd={{script_result.get('max_drawdown_pct', float('nan')):.2f}}%, "
            f"winrate={{script_result.get('win_rate_pct', float('nan')):.1f}}%, "
            f"pf={{script_result.get('profit_factor', float('nan')):.3f}}\")",
            f"# ── parity check ─────────────────────────────────────────────────────",
            f"_n, _s = alm_result['total_trades'], script_result['total_trades']",
            f"if _n == _s:",
            f"    print(f'✓ PARITY OK — both produce {{_n}} trades')",
            f"else:",
            f"    print(f'✗ PARITY FAIL — named={{_n}} script={{_s}}')",
        ))

    if has_bt:
        cells.append(code(
            f"# ── Backtrader native strategy ───────────────────────────────────────",
            bt_fmt,
            "",
            "from _shared import bt_run",
            "bt_result = bt_run(BtStrat, DF, capital=CAPITAL, commission=COMM, slippage=SLIP)",
            "print(f\"backtrader: {bt_result['total_trades']} trades, "
            "return={bt_result['total_return_pct']:.2f}%, "
            "sharpe={bt_result['sharpe_ratio']:.3f}, "
            "sortino={bt_result.get('sortino_ratio', float('nan')):.3f}, "
            "maxdd={bt_result.get('max_drawdown_pct', float('nan')):.2f}%, "
            "winrate={bt_result.get('win_rate_pct', float('nan')):.1f}%, "
            "pf={bt_result.get('profit_factor', float('nan')):.3f}\")",
        ))
    else:
        cells.append(code(
            "# backtrader: skip (no native indicator equivalent)",
            "bt_result = {'total_return_pct': float('nan'), 'sharpe_ratio': float('nan'),",
            "             'sortino_ratio': float('nan'), 'max_drawdown_pct': float('nan'),",
            "             'total_trades': 0, 'win_rate_pct': 0.0,",
            "             'avg_win': float('nan'), 'avg_loss': float('nan'),",
            "             'profit_factor': float('nan'), 'equity_curve': [], 'trades': []}",
        ))
    cells.append(code(METRICS_TABLE_CODE))

    cells.append(code(EQUITY_CHART_CODE))

    return nb(*cells)


# ─── Main ─────────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    total = len(STRATEGY_IMPLS)
    print(f"Generating {total} comparison notebooks in {HERE}/")
    ok = 0
    for name, impl in STRATEGY_IMPLS.items():
        try:
            notebook = build_notebook(name, impl)
            path = HERE / f"cmp_{name}.ipynb"
            path.write_text(json.dumps(notebook, indent=1, ensure_ascii=False))
            print(f"  [OK] cmp_{name}.ipynb")
            ok += 1
        except Exception as e:
            print(f"  [ERROR] {name}: {e}")
    print(f"\nDone: {ok}/{total} notebooks written.")
