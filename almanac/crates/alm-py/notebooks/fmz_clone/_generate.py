"""
Sinh notebook port FMZ Quant strategies → alm_py CEL (multi-timeframe) +
backtrader baseline.

Format mỗi notebook (1-3-1):
    1 alm_py CEL base (single TF)
    + 3 alm_py CEL MTF variants
    + 1 backtrader baseline

Chạy:  python _generate.py
Output: nb_fmz_*.ipynb cùng thư mục.
"""
from __future__ import annotations
import json, pathlib

HERE = pathlib.Path(__file__).parent

def md(*lines):  return {"cell_type": "markdown", "metadata": {}, "source": "\n".join(lines)}
def code(*lines): return {"cell_type": "code",     "metadata": {}, "execution_count": None, "outputs": [], "source": "\n".join(lines)}

def nb(*cells):
    return {
        "cells": list(cells),
        "metadata": {
            "kernelspec":     {"display_name": "Python 3 (alm-py venv)", "language": "python", "name": "python3"},
            "language_info":  {"name": "python", "pygments_lexer": "ipython3"},
        },
        "nbformat": 4,
        "nbformat_minor": 5,
    }

def write(name: str, notebook: dict):
    path = HERE / name
    path.write_text(json.dumps(notebook, indent=1, ensure_ascii=False))
    print(f"  → {path.name}")


# ── Common preamble: import alm_py, backtrader, helper ngoài comparison/ ────
PREAMBLE = code(
    "%matplotlib inline",
    "import sys, pathlib",
    "_HERE = pathlib.Path.cwd()",
    "for _p in (_HERE, _HERE.parent / 'comparison'):",
    "    if str(_p) not in sys.path: sys.path.insert(0, str(_p))",
    "import alm_py as alm",
    "import backtrader as bt",
    "import pandas as pd, numpy as np, matplotlib.pyplot as plt",
    "from _shared import bt_run, plot_equity, make_bars      # synthetic fallback",
    "from _data import load_testdata                          # BTCUSDT testdata thật",
    "pd.set_option('display.float_format', lambda v: f'{v:.4f}')",
)

# Helper compare cho format 5 cột (4 alm + 1 BT)
COMPARE_HELPER = code(
    "def compare5(rows: dict, keys=('total_return_pct','sharpe_ratio','max_drawdown_pct','total_trades','win_rate_pct')):",
    "    return pd.DataFrame({name: {k: r.get(k) for k in keys} for name, r in rows.items()})",
)


# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Notebook 1 · FMZ 1-3-1 Red-Green Candlestick Reversal                    ║
# ║ Source: https://www.fmz.com/strategy/430364                               ║
# ║ FMZ md: github.com/fmzquant/strategies → "1-3-1-红绿K线反转策略..."        ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

nb_131 = nb(
    md(
        "# FMZ · 1-3-1 Red-Green Candlestick Reversal",
        "",
        "**Nguồn:** [fmzquant/strategies — 1-3-1 Red-Green Reversal](https://github.com/fmzquant/strategies)",
        "  ([fmz.com/strategy/430364](https://www.fmz.com/strategy/430364))",
        "",
        "## Logic gốc (PineScript v5)",
        "",
        "```pinescript",
        "redCandle    = close[3] < open[3] and low[3] < low[2] and low[3] < low[1] and low[3] < low[0]",
        "greenCandles = close > open and close[1] > open[1] and close[2] > open[2]",
        "higherClose  = close > close[1] and close[1] > close[2]",
        "if redCandle and greenCandles and higherClose and position == 0:",
        "    enter long; SL = low[3]; TP = close + (close - low[3])   # 1:1 RR",
        "```",
        "",
        "**Pattern:** 4 nến liên tiếp — bar[3] đỏ (low thấp nhất 4 bar), bar[2,1,0] xanh với close tăng dần.",
        "",
        "## Giới hạn port",
        "- alm_py CEL **không** hỗ trợ SL theo giá tuyệt đối (`sl=low[3]`); chỉ %/ATR.",
        "- Để parity gọn, **bỏ SL/TP, exit = bar đỏ tiếp theo (`close < open`)** ở cả alm và BT.",
        "- TP/SL là chủ đề riêng — xem note cuối notebook.",
        "",
        "## Format 1-3-1",
        "1 alm_py CEL base (single TF) + 3 MTF variants + 1 backtrader baseline.",
    ),
    PREAMBLE,
    md(
        "## 1. Load BTCUSDT M1 thật từ `almanac/crates/data/testdata/`",
        "",
        "30000 bar M1 cuối ≈ 21 ngày. Đủ cho H1 EMA(200) (~200 H1 bar = 8 ngày) và D1 RSI(14) settle.",
    ),
    code(
        "alm_bars, df = load_testdata('BTCUSDT', 'M1', n=30000)",
        "print(f'M1 bars: {len(df)}, range: {df.index[0]} → {df.index[-1]}')",
        "print(f'price: {df[\"close\"].min():.0f} → {df[\"close\"].max():.0f}')",
    ),
    md(
        "## 2. CEL expression cho 1-3-1 pattern",
        "",
        "Map 1:1 từ PineScript sang CEL — `close[N]` cùng convention (N = số bar lùi).",
    ),
    code(
        "# Pattern building blocks",
        "RED_BAR3   = 'close[3] < open[3]'",
        "LOW_LOWEST = 'low[3] < low[2] && low[3] < low[1] && low[3] < low[0]'",
        "GREEN_3    = 'close > open && close[1] > open[1] && close[2] > open[2]'",
        "ASC_CLOSE  = 'close > close[1] && close[1] > close[2]'",
        "",
        "PATTERN_131 = f'{RED_BAR3} && {LOW_LOWEST} && {GREEN_3} && {ASC_CLOSE}'",
        "EXIT_RED    = 'close < open'   # exit khi bar tiếp theo đỏ",
        "print(PATTERN_131)",
    ),
    md("## 3. alm_py CEL · base (single TF — pattern thuần)"),
    code(
        "    bars=alm_bars, capital=10_000.0, commission=0.001, slippage=0.0005,",
        ")",
        "{k: alm_base[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
    ),
    md(
        "## 4. alm_py CEL · MTF v1 — pattern + H1 trend filter",
        "",
        "Chỉ vào lệnh khi **H1 EMA(50) > EMA(200)** (xu hướng tăng trên timeframe lớn).",
    ),
    code(
        "ENTRY_MTF1 = PATTERN_131 + ' && H1.ema(50) > H1.ema(200)'",
        "    bars=alm_bars, capital=10_000.0, commission=0.001, slippage=0.0005,",
        ")",
        "{k: alm_mtf1[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
    ),
    md(
        "## 5. alm_py CEL · MTF v2 — pattern + H1 momentum confirm",
        "",
        "**H1 MACD histogram > 0** (động lượng dương trên H1).",
    ),
    code(
        "ENTRY_MTF2 = PATTERN_131 + ' && H1.macd_hist(12) > 0.0'",
        "    bars=alm_bars, capital=10_000.0, commission=0.001, slippage=0.0005,",
        ")",
        "{k: alm_mtf2[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
    ),
    md(
        "## 6. alm_py CEL · MTF v3 — Triple Screen (Elder)",
        "",
        "**H1 trend + D1 momentum** — pattern M5 chỉ kích hoạt khi cả H1 và D1 đều ủng hộ long.",
    ),
    code(
        "ENTRY_MTF3 = PATTERN_131 + ' && H1.ema(50) > H1.ema(200) && D1.rsi(14) > 50.0'",
        "    bars=alm_bars, capital=10_000.0, commission=0.001, slippage=0.0005,",
        ")",
        "{k: alm_mtf3[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
    ),
    md(
        "## 7. backtrader baseline (single TF — port pattern thuần)",
        "",
        "Backtrader convention: `data.close[0]` = current, `data.close[-N]` = N bar lùi.",
        "Map: `close[3]` (PineScript/CEL) ↔ `data.close[-3]` (backtrader).",
    ),
    code(
        "class Fmz131(bt.Strategy):",
        "    def __init__(self):",
        "        self.entered_bar = -1",
        "    def next(self):",
        "        if len(self) < 4:  # cần ít nhất 4 bar lookback",
        "            return",
        "        d = self.data",
        "        red_bar3      = d.close[-3] < d.open[-3]",
        "        low_lowest    = d.low[-3] < d.low[-2] and d.low[-3] < d.low[-1] and d.low[-3] < d.low[0]",
        "        green_3       = d.close[0] > d.open[0] and d.close[-1] > d.open[-1] and d.close[-2] > d.open[-2]",
        "        asc_close     = d.close[0] > d.close[-1] and d.close[-1] > d.close[-2]",
        "        pattern       = red_bar3 and low_lowest and green_3 and asc_close",
        "        if not self.position:",
        "            if pattern:",
        "                self.buy()",
        "                self.entered_bar = len(self)",
        "        else:",
        "            # Exit khi bar đỏ (close < open) và không phải bar entry",
        "            if d.close[0] < d.open[0] and len(self) > self.entered_bar:",
        "                self.close()",
        "",
        "bt_result = bt_run(Fmz131, df, timeframe=bt.TimeFrame.Minutes, compression=1,   # M1",
        "                    commission=0.001, slippage=0.0005)",
        "{k: bt_result[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
    ),
    md("## 8. So sánh 5 cấu hình"),
    COMPARE_HELPER,
    code(
        "rows = {",
        "    'alm.base':        alm_base,",
        "    'alm.mtf.h1trend': alm_mtf1,",
        "    'alm.mtf.h1macd':  alm_mtf2,",
        "    'alm.mtf.triple':  alm_mtf3,",
        "    'backtrader':      bt_result,",
        "}",
        "compare5(rows)",
    ),
    md("## 9. Equity curves — 4 alm_py variants vs backtrader"),
    code(
        "fig, ax = plt.subplots(figsize=(12, 5))",
        "for name, r in rows.items():",
        "    eq = r.get('equity_curve')",
        "    if not eq: continue",
        "    if isinstance(eq[0], dict):  # alm_py format",
        "        eq_df = pd.DataFrame(eq)",
        "        ax.plot(pd.to_datetime(eq_df['t'], unit='ms', utc=True), eq_df['equity'], label=name, linewidth=1.2)",
        "    else:  # backtrader (list of floats)",
        "        ax.plot(df.index[-len(eq):], eq, label=name, linewidth=1.2, alpha=0.85)",
        "ax.set_title('1-3-1 Red-Green Reversal · 5 cấu hình')",
        "ax.legend(); ax.grid(alpha=0.3); ax.set_ylabel('Equity'); plt.tight_layout(); plt.show()",
    ),
    md(
        "## 10. Kỳ vọng kết quả & quan sát",
        "",
        "- **alm.base ↔ backtrader**: total_return phải gần nhau (sai khác do timing exit close-vs-close).",
        "- **3 MTF variants**: số trade giảm dần khi thêm filter — đó là dấu hiệu filter đang work.",
        "- **alm.mtf.triple**: thường ít trade nhất nhưng win-rate cao hơn (Elder triple screen).",
        "",
        "## Note về SL/TP của bản gốc FMZ",
        "",
        "PineScript gốc dùng `strategy.exit(stop=low[3], limit=close+(close-low[3]))` — bracket order với SL/TP",
        "tuyệt đối, intra-bar fill. CEL chưa hỗ trợ. 2 hướng work-around:",
        "",
        "1. Dùng `exit={'sl_pct': X, 'tp_pct': Y}` xấp xỉ — không identical pattern gốc.",
        "2. Implement absolute-price SL trong engine layer (deferred — xem `project_engine_deferred.md`).",
    ),
)
write("nb_fmz_131_red_green_reversal.ipynb", nb_131)


# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Notebook 2 · FMZ 1-2-3 Pattern + EMA + MACD + 4th Candle Extension       ║
# ║ Source: https://www.fmz.com/strategy/444003                               ║
# ║ FMZ md: "1-2-3-形态与-EMAMACD-和第四根蜡烛线延伸..."                        ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

nb_123 = nb(
    md(
        "# FMZ · 1-2-3 Pattern with EMA/MACD and 4th Candle Extension",
        "",
        "**Nguồn:** [fmzquant/strategies — 1-2-3 Pattern](https://github.com/fmzquant/strategies)",
        "  ([fmz.com/strategy/444003](https://www.fmz.com/strategy/444003))",
        "",
        "## Logic gốc (PineScript v5) — long",
        "",
        "```pinescript",
        "buy_candle1_above_open  = close[3] > open[3]      # bar[3] xanh",
        "buy_candle2_below_open  = close[2] < open[2]      # bar[2] đỏ",
        "buy_candle3_above_close = close[1] > close[3]     # bar[1] close vượt bar[3]",
        "buy_candle4_above_close = close   > close[3]     # bar[0] tiếp tục vượt",
        "ema_9, ema_20  = ta.ema(close, 9), ta.ema(close, 20)",
        "[macd_line, sig, _] = ta.macd(close, 12, 26, 9)",
        "if pattern AND close > ema_9 AND close > ema_20 AND macd_line > sig:",
        "    enter long",
        "exit when close < open",
        "```",
        "",
        "## Format 1-3-1",
        "1 alm_py CEL base (single TF, full filter EMA9/20+MACD) +",
        "3 MTF variants (đẩy filter sang TF cao) + 1 backtrader baseline.",
    ),
    PREAMBLE,
    md(
        "## 1. Load BTCUSDT M1 thật",
        "",
        "30000 bar M1 cuối ≈ 21 ngày. Đủ cho H1 EMA(200) và H4 MACD settle.",
    ),
    code(
        "alm_bars, df = load_testdata('BTCUSDT', 'M1', n=30000)",
        "print(f'M1 bars: {len(df)}, range: {df.index[0]} → {df.index[-1]}')",
    ),
    md("## 2. CEL expression — 1-2-3 pattern + EMA + MACD"),
    code(
        "# 1-2-3 pattern (long)",
        "PATTERN_123 = (",
        "    'close[3] > open[3] && '            # bar[3] xanh",
        "    'close[2] < open[2] && '            # bar[2] đỏ",
        "    'close[1] > close[3] && '            # bar[1] vượt bar[3]",
        "    'close > close[3]'                   # bar[0] tiếp tục vượt",
        ")",
        "EMA_FILTER  = 'close > ema(9) && close > ema(20)'",
        "MACD_FILTER = 'macd_line(12) > 0.0'   # macd_line - signal_line approx; xem note",
        "EXIT_RED    = 'close < open'",
        "print(PATTERN_123)",
    ),
    md(
        "**Lưu ý:** CEL có `macd_line(N)` (line value, signal hardcoded 9) và `macd_hist(N)` (= line − signal).",
        "Trong gốc PineScript: `macd_line > signal_line` ↔ `macd_hist > 0`. Dùng `macd_hist(12) > 0` cho parity.",
    ),
    code("MACD_FILTER = 'macd_hist(12) > 0.0'   # = macd_line - signal_line > 0"),
    md("## 3. alm_py CEL · base (single TF, đầy đủ filter EMA + MACD)"),
    code(
        "ENTRY_BASE = f'{PATTERN_123} && {EMA_FILTER} && {MACD_FILTER}'",
        "    bars=alm_bars, capital=10_000.0, commission=0.001, slippage=0.0005,",
        ")",
        "{k: alm_base[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
    ),
    md(
        "## 4. alm_py CEL · MTF v1 — đẩy EMA filter sang H1",
        "",
        "Pattern detect trên M5, nhưng trend filter dùng **H1 EMA(9) > H1 EMA(20)** thay cho close > ema base TF.",
    ),
    code(
        "ENTRY_MTF1 = f'{PATTERN_123} && H1.ema(9) > H1.ema(20) && {MACD_FILTER}'",
        "    bars=alm_bars, capital=10_000.0, commission=0.001, slippage=0.0005,",
        ")",
        "{k: alm_mtf1[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
    ),
    md(
        "## 5. alm_py CEL · MTF v2 — đẩy MACD filter sang H1",
        "",
        "EMA filter giữ ở base TF, **MACD chuyển sang H1** (`H1.macd_hist(12) > 0`).",
    ),
    code(
        "ENTRY_MTF2 = f'{PATTERN_123} && {EMA_FILTER} && H1.macd_hist(12) > 0.0'",
        "    bars=alm_bars, capital=10_000.0, commission=0.001, slippage=0.0005,",
        ")",
        "{k: alm_mtf2[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
    ),
    md(
        "## 6. alm_py CEL · MTF v3 — full MTF (H1 EMA + H4 MACD)",
        "",
        "Đẩy cả 2 filter lên: **H1 EMA + H4 MACD**, kết hợp 3 timeframe (M5 entry, H1 trend, H4 momentum).",
    ),
    code(
        "ENTRY_MTF3 = f'{PATTERN_123} && H1.ema(9) > H1.ema(20) && H4.macd_hist(12) > 0.0'",
        "    bars=alm_bars, capital=10_000.0, commission=0.001, slippage=0.0005,",
        ")",
        "{k: alm_mtf3[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
    ),
    md("## 7. backtrader baseline (single TF — port full filter)"),
    code(
        "class Fmz123(bt.Strategy):",
        "    params = (('ema_fast', 9), ('ema_slow', 20),",
        "              ('macd_fast', 12), ('macd_slow', 26), ('macd_sig', 9))",
        "    def __init__(self):",
        "        self.ema9  = bt.indicators.EMA(self.data.close, period=self.p.ema_fast)",
        "        self.ema20 = bt.indicators.EMA(self.data.close, period=self.p.ema_slow)",
        "        macd = bt.indicators.MACD(self.data.close,",
        "                                   period_me1=self.p.macd_fast,",
        "                                   period_me2=self.p.macd_slow,",
        "                                   period_signal=self.p.macd_sig)",
        "        self.macd_hist = macd.macd - macd.signal",
        "        self.entered_bar = -1",
        "    def next(self):",
        "        if len(self) < 4: return",
        "        d = self.data",
        "        c1_green = d.close[-3] > d.open[-3]",
        "        c2_red   = d.close[-2] < d.open[-2]",
        "        c3_above = d.close[-1] > d.close[-3]",
        "        c4_above = d.close[0]  > d.close[-3]",
        "        pattern  = c1_green and c2_red and c3_above and c4_above",
        "        ema_ok   = d.close[0] > self.ema9[0] and d.close[0] > self.ema20[0]",
        "        macd_ok  = self.macd_hist[0] > 0",
        "        if not self.position:",
        "            if pattern and ema_ok and macd_ok:",
        "                self.buy()",
        "                self.entered_bar = len(self)",
        "        else:",
        "            if d.close[0] < d.open[0] and len(self) > self.entered_bar:",
        "                self.close()",
        "",
        "bt_result = bt_run(Fmz123, df, timeframe=bt.TimeFrame.Minutes, compression=1,   # M1",
        "                    commission=0.001, slippage=0.0005)",
        "{k: bt_result[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
    ),
    md("## 8. So sánh 5 cấu hình"),
    COMPARE_HELPER,
    code(
        "rows = {",
        "    'alm.base':         alm_base,",
        "    'alm.mtf.h1ema':    alm_mtf1,",
        "    'alm.mtf.h1macd':   alm_mtf2,",
        "    'alm.mtf.h1+h4':    alm_mtf3,",
        "    'backtrader':       bt_result,",
        "}",
        "compare5(rows)",
    ),
    md("## 9. Equity curves"),
    code(
        "fig, ax = plt.subplots(figsize=(12, 5))",
        "for name, r in rows.items():",
        "    eq = r.get('equity_curve')",
        "    if not eq: continue",
        "    if isinstance(eq[0], dict):",
        "        eq_df = pd.DataFrame(eq)",
        "        ax.plot(pd.to_datetime(eq_df['t'], unit='ms', utc=True), eq_df['equity'], label=name, linewidth=1.2)",
        "    else:",
        "        ax.plot(df.index[-len(eq):], eq, label=name, linewidth=1.2, alpha=0.85)",
        "ax.set_title('1-2-3 Pattern + EMA/MACD · 5 cấu hình')",
        "ax.legend(); ax.grid(alpha=0.3); ax.set_ylabel('Equity'); plt.tight_layout(); plt.show()",
    ),
    md(
        "## 10. Quan sát",
        "",
        "- **alm.base ↔ backtrader**: parity test cốt lõi — return phải sát.",
        "- **alm.mtf.h1ema**: đẩy EMA lên H1 → ít trade hơn, tránh whipsaw trên TF nhỏ.",
        "- **alm.mtf.h1macd**: tương tự nhưng cho MACD.",
        "- **alm.mtf.h1+h4**: full MTF — số trade thấp nhất, kỳ vọng quality cao nhất.",
        "",
        "## Note: short side",
        "",
        "Bản gốc FMZ có cả buy + sell. Notebook này chỉ port long để giữ ngắn gọn —",
        "short = mirror (close[3] < open[3], close[2] > open[2], ...) tương tự, dễ thêm.",
    ),
)
write("nb_fmz_123_pattern_ema_macd.ipynb", nb_123)


# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Generic template cho rule-based strategy (Turtle, Aberration, ...)        ║
# ║ Pattern strategy (1-3-1, 1-2-3) viết tay riêng vì candle-pattern phức tạp.║
# ╚═══════════════════════════════════════════════════════════════════════════╝

def gen_strategy_nb(cfg: dict) -> dict:
    """
    cfg = {
        'file':       'nb_fmz_03_turtle.ipynb',
        'title':      'FMZ · Turtle / Donchian Breakout',
        'fmz_link':   '[Breakout Strategy Based on Turtle Trading](https://github.com/...)',
        'description': '...short markdown intro...',
        'data_tf':    'M1',
        'data_n':     30000,
        'bt_compression': 1,            # backtrader compression matching data_tf
        'base_entry': '...CEL...',
        'base_exit':  '...CEL...',
        'bt_class':   'class FmzXxx(bt.Strategy): ...\\n',
        'plot_title': 'Turtle Breakout · 5 cấu hình',
    }
    """
    cells = [
        md(
            f"# {cfg['title']}",
            "",
            f"**Nguồn:** {cfg['fmz_link']}",
            "",
            cfg["description"],
            "",
            "## Format 1-3-1: 1 base + 3 MTF + 1 backtrader",
        ),
        PREAMBLE,
        md(f"## 1. Load BTCUSDT {cfg['data_tf']} (`almanac/crates/data/testdata/`)"),
        code(
            f"alm_bars, df = load_testdata('BTCUSDT', '{cfg['data_tf']}', n={cfg['data_n']})",
            "print(f'{cfg[\"data_tf\"]} bars: {len(df)}, range: {df.index[0]} → {df.index[-1]}')".replace(
                'cfg["data_tf"]', f"'{cfg['data_tf']}'"
            ),
        ),
        md("## 2. CEL expression — base"),
        code(
            f"ENTRY_BASE = {cfg['base_entry']!r}",
            f"EXIT_BASE  = {cfg['base_exit']!r}",
            "print('entry:', ENTRY_BASE)",
            "print('exit :', EXIT_BASE)",
        ),
        md("## 3. alm_py CEL · base (single TF)"),
        code(
            "    bars=alm_bars, capital=10_000.0, commission=0.001, slippage=0.0005,",
            ")",
            "{k: alm_base[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
        ),
    ]
    # 3 MTF variants
    for idx, (label, entry, exit_) in enumerate(cfg["mtf"], start=1):
        cells += [
            md(f"## {3+idx}. alm_py CEL · MTF v{idx} — {label}"),
            code(
                f"ENTRY_MTF{idx} = {entry!r}",
                f"EXIT_MTF{idx}  = {exit_!r}",
                "    bars=alm_bars, capital=10_000.0, commission=0.001, slippage=0.0005,",
                ")",
                f"{{k: alm_mtf{idx}[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}}",
            ),
        ]
    cells += [
        md("## 7. backtrader baseline (port logic gốc)"),
        code(
            cfg["bt_class"],
            "",
            f"bt_result = bt_run(FmzBT, df, timeframe=bt.TimeFrame.Minutes, compression={cfg['bt_compression']},",
            "                    commission=0.001, slippage=0.0005)",
            "{k: bt_result[k] for k in ('total_return_pct','sharpe_ratio','total_trades','win_rate_pct')}",
        ),
        md("## 8. So sánh 5 cấu hình"),
        COMPARE_HELPER,
        code(
            "rows = {",
            "    'alm.base':   alm_base,",
            "    'alm.mtf.v1': alm_mtf1,",
            "    'alm.mtf.v2': alm_mtf2,",
            "    'alm.mtf.v3': alm_mtf3,",
            "    'backtrader': bt_result,",
            "}",
            "compare5(rows)",
        ),
        md("## 9. Equity curves"),
        code(
            "fig, ax = plt.subplots(figsize=(12, 5))",
            "for name, r in rows.items():",
            "    eq = r.get('equity_curve')",
            "    if not eq: continue",
            "    if isinstance(eq[0], dict):",
            "        eq_df = pd.DataFrame(eq)",
            "        ax.plot(pd.to_datetime(eq_df['t'], unit='ms', utc=True), eq_df['equity'], label=name, linewidth=1.2)",
            "    else:",
            "        ax.plot(df.index[-len(eq):], eq, label=name, linewidth=1.2, alpha=0.85)",
            f"ax.set_title({cfg['plot_title']!r})",
            "ax.legend(); ax.grid(alpha=0.3); ax.set_ylabel('Equity'); plt.tight_layout(); plt.show()",
        ),
    ]
    return nb(*cells)


# ── 5 strategy configs ───────────────────────────────────────────────────────

STRATEGIES = [
    # ── 03 · Turtle / Donchian Breakout ─────────────────────────────────────
    {
        "file":  "nb_fmz_03_turtle_donchian.ipynb",
        "title": "FMZ · Turtle / Donchian Breakout (20-in / 10-out)",
        "fmz_link": (
            "[Breakout Strategy Based on Turtle Trading]"
            "(https://github.com/fmzquant/strategies/blob/master/"
            "%E5%9F%BA%E4%BA%8E%E6%B5%B7%E9%BE%9F%E4%BA%A4%E6%98%93%E6%B3%95"
            "%E7%9A%84%E7%AA%81%E7%A0%B4%E7%AD%96%E7%95%A5"
            "Breakout-Strategy-Based-on-Turtle-Trading.md)"
        ),
        "description": (
            "## Logic\n\n"
            "- Entry long: `close > Donchian_upper(20)` (break previous 20-bar high)\n"
            "- Exit: `close < Donchian_lower(10)` (break previous 10-bar low)\n\n"
            "Đây là biến thể classic Turtle System 1 — kết hợp với MTF filter để giảm whipsaw."
        ),
        "data_tf": "M1", "data_n": 30000, "bt_compression": 1,
        "base_entry": "close > donchian_upper(20)[1]",
        "base_exit":  "close < donchian_lower(10)[1]",
        "mtf": [
            ("+ H1 trend filter (EMA 50>200)",
             "close > donchian_upper(20)[1] && H1.ema(50) > H1.ema(200)",
             "close < donchian_lower(10)[1]"),
            ("+ H4 trend filter",
             "close > donchian_upper(20)[1] && H4.ema(50) > H4.ema(200)",
             "close < donchian_lower(10)[1]"),
            ("+ H1 trend + D1 RSI > 50",
             "close > donchian_upper(20)[1] && H1.ema(50) > H1.ema(200) && D1.rsi(14) > 50.0",
             "close < donchian_lower(10)[1]"),
        ],
        "bt_class": (
            "class FmzBT(bt.Strategy):\n"
            "    params = (('entry_n', 20), ('exit_n', 10))\n"
            "    def __init__(self):\n"
            "        self.upper = bt.indicators.Highest(self.data.high, period=self.p.entry_n)\n"
            "        self.lower = bt.indicators.Lowest (self.data.low,  period=self.p.exit_n)\n"
            "    def next(self):\n"
            "        if not self.position:\n"
            "            if self.data.close[0] > self.upper[-1]:\n"
            "                self.buy()\n"
            "        else:\n"
            "            if self.data.close[0] < self.lower[-1]:\n"
            "                self.close()"
        ),
        "plot_title": "Turtle Donchian Breakout · 5 cấu hình",
    },
    # ── 04 · Aberration (Bollinger trend follower) ──────────────────────────
    {
        "file":  "nb_fmz_04_aberration.ipynb",
        "title": "FMZ · Aberration (Bollinger Trend Follower)",
        "fmz_link": (
            "Classic strategy ([fmzquant/strategies](https://github.com/fmzquant/strategies) — "
            "tham khảo các Bollinger trend follower variants)"
        ),
        "description": (
            "## Logic\n\n"
            "- Entry long: `close > BB_upper(20, 2σ)` (break upper band → trend up)\n"
            "- Exit: `close < BB_mid(20)` (giá quay về middle band)\n\n"
            "Aberration là 1 trong những public BB trend strategy đầu tiên (Keith Fitschen, 90s)."
        ),
        "data_tf": "M1", "data_n": 30000, "bt_compression": 1,
        "base_entry": "close > bb_upper(20)",
        "base_exit":  "close < bb_mid(20)",
        "mtf": [
            ("+ H1 BB confirm",
             "close > bb_upper(20) && H1.close > H1.bb_mid(20)",
             "close < bb_mid(20)"),
            ("+ H4 trend (close > H4.ema(50))",
             "close > bb_upper(20) && H4.close > H4.ema(50)",
             "close < bb_mid(20)"),
            ("+ H1 BB + D1 RSI > 50",
             "close > bb_upper(20) && H1.close > H1.bb_mid(20) && D1.rsi(14) > 50.0",
             "close < bb_mid(20)"),
        ],
        "bt_class": (
            "class FmzBT(bt.Strategy):\n"
            "    params = (('period', 20), ('mult', 2.0))\n"
            "    def __init__(self):\n"
            "        self.bb = bt.indicators.BollingerBands(self.data.close,\n"
            "                                                period=self.p.period,\n"
            "                                                devfactor=self.p.mult)\n"
            "    def next(self):\n"
            "        if not self.position:\n"
            "            if self.data.close[0] > self.bb.lines.top[0]:\n"
            "                self.buy()\n"
            "        else:\n"
            "            if self.data.close[0] < self.bb.lines.mid[0]:\n"
            "                self.close()"
        ),
        "plot_title": "Aberration BB Trend · 5 cấu hình",
    },
    # ── 05 · SuperTrend + RSI Pullback ──────────────────────────────────────
    {
        "file":  "nb_fmz_05_supertrend_rsi.ipynb",
        "title": "FMZ · SuperTrend Trend + RSI Pullback Entry",
        "fmz_link": (
            "[Mini Pullback Supertrend Strategy]"
            "(https://github.com/fmzquant/strategies) (FMZ collection)"
        ),
        "description": (
            "## Logic\n\n"
            "- Entry long: `SuperTrend bullish` AND `RSI(14) < 40` (pullback in uptrend)\n"
            "- Exit: `SuperTrend bearish` OR `RSI(14) > 70`\n\n"
            "Combine SuperTrend (trend identifier) với RSI (pullback timing). "
            "Ý tưởng: chỉ mua dip khi xu hướng vẫn lên."
        ),
        "data_tf": "M5", "data_n": 20000, "bt_compression": 5,
        "base_entry": "st_bull(10) > 0.5 && rsi(14) < 40.0",
        "base_exit":  "st_bull(10) < 0.5 || rsi(14) > 70.0",
        "mtf": [
            ("+ H1 trend confirm",
             "st_bull(10) > 0.5 && rsi(14) < 40.0 && H1.st_bull(10) > 0.5",
             "st_bull(10) < 0.5 || rsi(14) > 70.0"),
            ("+ H4 trend confirm",
             "st_bull(10) > 0.5 && rsi(14) < 40.0 && H4.st_bull(10) > 0.5",
             "st_bull(10) < 0.5 || rsi(14) > 70.0"),
            ("+ H1 + D1 (triple-screen)",
             "st_bull(10) > 0.5 && rsi(14) < 40.0 && H1.st_bull(10) > 0.5 && D1.ema(20) > D1.ema(50)",
             "st_bull(10) < 0.5 || rsi(14) > 70.0"),
        ],
        "bt_class": (
            "# Backtrader không có SuperTrend built-in — dùng hàm custom đơn giản\n"
            "class SuperTrend(bt.Indicator):\n"
            "    lines = ('trend',)\n"
            "    params = (('period', 10), ('mult', 3.0))\n"
            "    plotinfo = dict(plot=False)\n"
            "    def __init__(self):\n"
            "        atr = bt.indicators.ATR(self.data, period=self.p.period)\n"
            "        hl2 = (self.data.high + self.data.low) / 2.0\n"
            "        self.up_band = hl2 - self.p.mult * atr\n"
            "        self.dn_band = hl2 + self.p.mult * atr\n"
            "        self.addminperiod(self.p.period + 2)\n"
            "    def next(self):\n"
            "        prev_trend = self.lines.trend[-1] if len(self) > 1 else 1.0\n"
            "        if self.data.close[0] > self.dn_band[-1]:\n"
            "            self.lines.trend[0] = 1.0\n"
            "        elif self.data.close[0] < self.up_band[-1]:\n"
            "            self.lines.trend[0] = -1.0\n"
            "        else:\n"
            "            self.lines.trend[0] = prev_trend\n"
            "\n"
            "class FmzBT(bt.Strategy):\n"
            "    def __init__(self):\n"
            "        self.st  = SuperTrend(self.data, period=10, mult=3.0)\n"
            "        self.rsi = bt.indicators.RSI(self.data.close, period=14)\n"
            "    def next(self):\n"
            "        if not self.position:\n"
            "            if self.st.lines.trend[0] > 0 and self.rsi[0] < 40:\n"
            "                self.buy()\n"
            "        else:\n"
            "            if self.st.lines.trend[0] < 0 or self.rsi[0] > 70:\n"
            "                self.close()"
        ),
        "plot_title": "SuperTrend + RSI Pullback · 5 cấu hình",
    },
    # ── 06 · Triple Screen Elder ────────────────────────────────────────────
    {
        "file":  "nb_fmz_06_triple_screen_elder.ipynb",
        "title": "Elder Triple Screen (W1 trend + D1 momentum + H1 entry)",
        "fmz_link": (
            "[Elder Ray Bull Power Combo Strategy]"
            "(https://github.com/fmzquant/strategies) — Alexander Elder methodology"
        ),
        "description": (
            "## Logic\n\n"
            "Pure MTF play (Alexander Elder, 1989):\n"
            "- **Screen 1 (W1)**: trend xác nhận — `EMA(13) > EMA(34)` trên W1\n"
            "- **Screen 2 (D1)**: momentum — `MACD histogram > 0` trên D1\n"
            "- **Screen 3 (H1)**: entry timing — `Stoch K < 30` (pullback) trên H1\n\n"
            "Notebook này base TF = H1; 3 MTF variants vary screening combos."
        ),
        "data_tf": "H1", "data_n": 8000, "bt_compression": 60,
        "base_entry": "D1.ema(13) > D1.ema(34) && H4.macd_hist(12) > 0.0 && stoch_k(14) < 30.0",
        "base_exit":  "stoch_k(14) > 70.0",
        "mtf": [
            ("Đổi entry: dùng RSI(14) < 35 thay Stoch",
             "D1.ema(13) > D1.ema(34) && H4.macd_hist(12) > 0.0 && rsi(14) < 35.0",
             "rsi(14) > 65.0"),
            ("Strict trend: D1 EMA + D1 MACD + H4 momentum",
             "D1.ema(13) > D1.ema(34) && D1.macd_hist(12) > 0.0 && H4.macd_hist(12) > 0.0 && stoch_k(14) < 30.0",
             "stoch_k(14) > 70.0"),
            ("Full 4-screens: W1 + D1 + H4 + H1",
             "W1.ema(13) > W1.ema(34) && D1.macd_hist(12) > 0.0 && H4.ema(13) > H4.ema(34) && stoch_k(14) < 30.0",
             "stoch_k(14) > 70.0"),
        ],
        "bt_class": (
            "# Backtrader baseline single-TF approximation: dùng EMA(13×24×7) ~ W1\n"
            "# trên data H1; MACD trên data H1; Stoch H1. Không phải MTF thật.\n"
            "class FmzBT(bt.Strategy):\n"
            "    def __init__(self):\n"
            "        # Approx W1 trend bằng EMA dài trên H1: 13*168 và 34*168\n"
            "        self.ema_fast = bt.indicators.EMA(self.data.close, period=13*168)\n"
            "        self.ema_slow = bt.indicators.EMA(self.data.close, period=34*168)\n"
            "        macd = bt.indicators.MACD(self.data.close, period_me1=12, period_me2=26, period_signal=9)\n"
            "        self.macd_hist = macd.macd - macd.signal\n"
            "        self.stoch_k   = bt.indicators.Stochastic(self.data, period=14).lines.percK\n"
            "    def next(self):\n"
            "        if not self.position:\n"
            "            if (self.ema_fast[0] > self.ema_slow[0]\n"
            "                and self.macd_hist[0] > 0\n"
            "                and self.stoch_k[0] < 30):\n"
            "                self.buy()\n"
            "        else:\n"
            "            if self.stoch_k[0] > 70:\n"
            "                self.close()"
        ),
        "plot_title": "Elder Triple Screen · 5 cấu hình",
    },
    # ── 07 · Vortex + ADX ───────────────────────────────────────────────────
    {
        "file":  "nb_fmz_07_vortex_adx.ipynb",
        "title": "FMZ · Vortex Cross + ADX Strength Filter",
        "fmz_link": (
            "[Advanced Vortex Momentum Analysis Strategy]"
            "(https://github.com/fmzquant/strategies)"
        ),
        "description": (
            "## Logic\n\n"
            "- Entry long: `Vortex+ > Vortex−` AND `ADX(14) > 25` (trend strong)\n"
            "- Exit: `Vortex+ < Vortex−`\n\n"
            "Vortex Indicator (Etienne Botes, Douglas Siepman, 2010) phát hiện đảo chiều xu hướng. "
            "Kết hợp với ADX để chỉ trade khi xu hướng đủ mạnh."
        ),
        "data_tf": "M15", "data_n": 12000, "bt_compression": 15,
        "base_entry": "vortex_plus(14) > vortex_minus(14) && adx(14) > 25.0",
        "base_exit":  "vortex_plus(14) < vortex_minus(14)",
        "mtf": [
            ("+ H1 ADX > 25 (trend strong cả 2 TF)",
             "vortex_plus(14) > vortex_minus(14) && adx(14) > 25.0 && H1.adx(14) > 25.0",
             "vortex_plus(14) < vortex_minus(14)"),
            ("+ H4 vortex confirm",
             "vortex_plus(14) > vortex_minus(14) && adx(14) > 25.0 && H4.vortex_plus(14) > H4.vortex_minus(14)",
             "vortex_plus(14) < vortex_minus(14)"),
            ("+ H1 ADX + D1 trend",
             "vortex_plus(14) > vortex_minus(14) && adx(14) > 25.0 && H1.adx(14) > 25.0 && D1.ema(20) > D1.ema(50)",
             "vortex_plus(14) < vortex_minus(14)"),
        ],
        "bt_class": (
            "class FmzBT(bt.Strategy):\n"
            "    def __init__(self):\n"
            "        self.vp  = bt.indicators.Vortex(self.data, period=14).lines.vi_plus\n"
            "        self.vm  = bt.indicators.Vortex(self.data, period=14).lines.vi_minus\n"
            "        self.adx = bt.indicators.ADX(self.data, period=14)\n"
            "    def next(self):\n"
            "        if not self.position:\n"
            "            if self.vp[0] > self.vm[0] and self.adx[0] > 25:\n"
            "                self.buy()\n"
            "        else:\n"
            "            if self.vp[0] < self.vm[0]:\n"
            "                self.close()"
        ),
        "plot_title": "Vortex + ADX · 5 cấu hình",
    },
]

for cfg in STRATEGIES:
    write(cfg["file"], gen_strategy_nb(cfg))


# ── README ──────────────────────────────────────────────────────────────────
README = """\
# FMZ Quant strategies → alm_py CEL (multi-timeframe) parity notebooks

Mỗi notebook port 1 chiến lược từ [fmzquant/strategies](https://github.com/fmzquant/strategies)
sang alm_py CEL DSL, với 3 multi-timeframe variants + 1 backtrader baseline.

## Format mỗi notebook (1-3-1)

1. **alm_py CEL base** — single TF, port logic gốc 1:1
2. **alm_py CEL MTF v1** — pattern base TF + filter v1 ở TF cao
3. **alm_py CEL MTF v2** — pattern base TF + filter v2 ở TF cao
4. **alm_py CEL MTF v3** — full MTF (2-3 timeframe đồng thời)
5. **backtrader** — port logic gốc 1:1, làm baseline parity

## Chạy

```bash
cd almanac/crates/alm-py
maturin develop --release
source .venv/bin/activate
jupyter lab notebooks/fmz_clone/
```

## Notebook hiện có

| File | FMZ Strategy | Logic | MTF variants |
|------|--------------|-------|--------------|
| `nb_fmz_131_red_green_reversal.ipynb` | [1-3-1 Red-Green Reversal](https://www.fmz.com/strategy/430364) | candle pattern (red+3 green ascending) | H1 trend / H1 macd / triple-screen |
| `nb_fmz_123_pattern_ema_macd.ipynb` | [1-2-3 Pattern + EMA/MACD](https://www.fmz.com/strategy/444003) | 1-2-3 pattern + EMA9/20 + MACD | H1 EMA / H1 MACD / H1+H4 |
| `nb_fmz_03_turtle_donchian.ipynb` | Turtle / Donchian Breakout | HH(20) entry, LL(10) exit | H1 trend / H4 trend / H1+D1 |
| `nb_fmz_04_aberration.ipynb` | Aberration (BB trend) | close > BB upper, exit < BB mid | H1 BB / H4 trend / H1 BB + D1 RSI |
| `nb_fmz_05_supertrend_rsi.ipynb` | SuperTrend + RSI Pullback | ST bullish + RSI < 40 | H1 ST / H4 ST / triple-screen |
| `nb_fmz_06_triple_screen_elder.ipynb` | Elder Triple Screen | W1 trend + D1 momentum + H1 stoch entry | D1+H4 / RSI variant / 4-screen |
| `nb_fmz_07_vortex_adx.ipynb` | Vortex + ADX | VI+ > VI− + ADX > 25 | H1 ADX / H4 vortex / H1+D1 |

## Regen

```bash
python _generate.py    # sinh lại tất cả nb_fmz_*.ipynb
```

## Giới hạn port

- **Absolute-price SL** (vd `sl=low[3]`) chưa hỗ trợ trong CEL — bỏ TP/SL, exit = next red candle.
- **MTF cho bar fields** chưa có (`H1.close[1]` không hợp lệ); chỉ MTF cho indicator (`H1.ema(20)`).
- **Cross-above/below**: CEL không có operator riêng → viết tay `prev_X <= prev_Y && X > Y`.

## TODO (chuẩn bị mở rộng)

Các ứng viên tiếp theo (ưu tiên candle-pattern + MTF):
- 3-bar reversal (engulfing pattern)
- 5-bar pullback in trend
- Inside bar breakout với HTF trend filter
- Pin bar reversal + EMA confluence
"""
(HERE / "README.md").write_text(README)
print("  → README.md")
print("\nDone. 2 notebook prototype + README.")
