import json
import pathlib

def make_md(*lines):
    return {"cell_type": "markdown", "metadata": {}, "source": [line + "\n" for line in lines]}

def make_code(*lines):
    return {"cell_type": "code", "metadata": {}, "execution_count": None, "outputs": [], "source": [line + "\n" for line in lines]}

def get_common_setup(title, desc):
    cells = []
    cells.append(make_md(
        f"# {title}",
        "",
        desc,
        "",
        "### Phương pháp đối soát:",
        "- **Tập dữ liệu**: Sử dụng 1000 nến thực tế BTCUSDT M1 từ file Parquet.",
        "- **Chỉ số sai số**: Sau giai đoạn warmup (warmup=150 nến), chúng ta tính toán:",
        "  - **MAE** (Mean Absolute Error): Sai số tuyệt đối trung bình.",
        "  - **Max Diff**: Sai lệch lớn nhất.",
        "  - **Correlation**: Hệ số tương quan Pearson.",
        "- **Trực quan hóa (Overlay)**: Vẽ biểu đồ đè nhau giữa các thư viện:",
        "  - Almanac: nét liền màu xanh dương (Solid Blue, linewidth=2.5)",
        "  - Pandas-TA: nét đứt màu cam (Dashed Orange, linewidth=1.5)",
        "  - Backtrader: nét chấm màu xanh lá (Dotted Green, linewidth=1.5)",
        "- **Độ tương thích cấu trúc (Structural Parity)**: Kiểm tra xem các trường đầu ra của Almanac có bị thừa hoặc thiếu so với các thư viện tham chiếu hay không."
    ))
    
    cells.append(make_code(
        "import pandas as pd",
        "import numpy as np",
        "import matplotlib.pyplot as plt",
        "import pandas_ta as ta",
        "import backtrader as bt",
        "import pyarrow.parquet as pq",
        "import pathlib",
        "import alm_py as alm",
        "",
        "# Cấu hình hiển thị biểu đồ",
        "plt.style.use('seaborn-v0_8-darkgrid' if 'seaborn-v0_8-darkgrid' in plt.style.available else 'default')",
        "plt.rcParams['figure.figsize'] = (14, 6)",
        "",
        "# Hàm chuyển đổi None thành np.nan để vẽ biểu đồ an toàn",
        "def clean(arr):",
        "    return np.array([float(x) if x is not None else np.nan for x in arr])",
        "",
        "# Hàm tính toán và hiển thị sai số đối soát",
        "def print_parity_stats(alm_arr, ta_arr, bt_arr, name, warmup=150):",
        "    a = clean(alm_arr)[warmup:]",
        "    t = clean(ta_arr)[warmup:] if ta_arr is not None else None",
        "    b = clean(bt_arr)[warmup:] if bt_arr is not None else None",
        "    ",
        "    res = f'=== {name} (warmup={warmup}) ==='",
        "    ",
        "    if t is not None:",
        "        mask_ta = ~np.isnan(a) & ~np.isnan(t)",
        "        if np.sum(mask_ta) > 0:",
        "            diff_ta = np.abs(a[mask_ta] - t[mask_ta])",
        "            mae_ta = np.mean(diff_ta)",
        "            max_ta = np.max(diff_ta)",
        "            corr_ta = np.corrcoef(a[mask_ta], t[mask_ta])[0, 1] if len(a[mask_ta]) > 1 else 1.0",
        "            status_ta = \"✅ MATCH\" if max_ta < 1e-4 else \"⚠️ MINOR DIFF\" if max_ta < 1.0 else \"❌ MISMATCH\"",
        "            res += f\"\\n  [Pandas-TA]  {status_ta} -> MAE: {mae_ta:.6e} | Max Diff: {max_ta:.6e} | Correlation: {corr_ta:.6f}\"",
        "        else:",
        "            res += f\"\\n  [Pandas-TA]  N/A (No overlapping data)\"",
        "            ",
        "    if b is not None:",
        "        mask_bt = ~np.isnan(a) & ~np.isnan(b)",
        "        if np.sum(mask_bt) > 0:",
        "            diff_bt = np.abs(a[mask_bt] - b[mask_bt])",
        "            mae_bt = np.mean(diff_bt)",
        "            max_bt = np.max(diff_bt)",
        "            corr_bt = np.corrcoef(a[mask_bt], b[mask_bt])[0, 1] if len(a[mask_bt]) > 1 else 1.0",
        "            status_bt = \"✅ MATCH\" if max_bt < 1e-4 else \"⚠️ MINOR DIFF\" if max_bt < 1.0 else \"❌ MISMATCH\"",
        "            res += f\"\\n  [Backtrader] {status_bt} -> MAE: {mae_bt:.6e} | Max Diff: {max_bt:.6e} | Correlation: {corr_bt:.6f}\"",
        "        else:",
        "            res += f\"\\n  [Backtrader] N/A (No overlapping data)\"",
        "            ",
        "    print(res)",
        "",
        "# Hàm vẽ biểu đồ overlay đối soát kết hợp OHLCV",
        "def plot_parity(alm_data, ta_data, bt_data, title, is_price=False):",
        "    g_df = globals().get('df', None)",
        "    if g_df is not None:",
        "        df_slice = g_df.tail(150).copy().reset_index(drop=True)",
        "        ",
        "        if not isinstance(alm_data, dict):",
        "            alm_dict = {'': alm_data}",
        "            ta_dict = {'': ta_data} if ta_data is not None else {}",
        "            bt_dict = {'': bt_data} if bt_data is not None else {}",
        "        else:",
        "            alm_dict = alm_data",
        "            ta_dict = ta_data if ta_data is not None else {}",
        "            bt_dict = bt_data if bt_data is not None else {}",
        "        ",
        "        up = df_slice[df_slice['c'] >= df_slice['o']]",
        "        down = df_slice[df_slice['c'] < df_slice['o']]",
        "        ",
        "        if is_price:",
        "            fig, (ax1, ax2) = plt.subplots(2, 1, sharex=True, gridspec_kw={'height_ratios': [3, 1]}, figsize=(14, 8))",
        "            main_ax = ax1",
        "            # Vẽ nến",
        "            ax1.vlines(up.index, up['l'], up['h'], color='green', linewidth=1.2)",
        "            ax1.vlines(down.index, down['l'], down['h'], color='red', linewidth=1.2)",
        "            ax1.bar(up.index, up['c'] - up['o'], bottom=up['o'], color='green', width=0.6)",
        "            ax1.bar(down.index, down['o'] - down['c'], bottom=down['c'], color='red', width=0.6)",
        "            ax1.set_title(title)",
        "            ax1.set_ylabel('Price (USDT)')",
        "            ",
        "            # Khối lượng",
        "            ax2.bar(up.index, up['v'], color='green', width=0.6, alpha=0.5)",
        "            ax2.bar(down.index, down['v'], color='red', width=0.6, alpha=0.5)",
        "            ax2.set_ylabel('Volume')",
        "            ax2.set_xlabel('Bar Index')",
        "        else:",
        "            fig, (ax1, ax2, ax3) = plt.subplots(3, 1, sharex=True, gridspec_kw={'height_ratios': [2.5, 0.8, 1.2]}, figsize=(14, 9))",
        "            main_ax = ax3",
        "            # Vẽ nến ở panel trên cùng",
        "            ax1.vlines(up.index, up['l'], up['h'], color='green', linewidth=1.2)",
        "            ax1.vlines(down.index, down['l'], down['h'], color='red', linewidth=1.2)",
        "            ax1.bar(up.index, up['c'] - up['o'], bottom=up['o'], color='green', width=0.6)",
        "            ax1.bar(down.index, down['o'] - down['c'], bottom=down['c'], color='red', width=0.6)",
        "            ax1.set_ylabel('Price (USDT)')",
        "            ax1.set_title(title)",
        "            ",
        "            # Khối lượng ở panel giữa",
        "            ax2.bar(up.index, up['v'], color='green', width=0.6, alpha=0.5)",
        "            ax2.bar(down.index, down['v'], color='red', width=0.6, alpha=0.5)",
        "            ax2.set_ylabel('Volume')",
        "            ",
        "            main_ax.set_ylabel('Indicator Value')",
        "            ",
        "        colors = ['#1f77b4', '#ff7f0e', '#2ca02c', '#d62728', '#9467bd', '#8c564b', '#e377c2', '#7f7f7f', '#bcbd22', '#17becf']",
        "        key_list = list(alm_dict.keys())",
        "        ",
        "        for i, k in enumerate(key_list):",
        "            if len(key_list) <= 1:",
        "                c_alm = '#1f77b4'",
        "                c_ta = '#ff7f0e'",
        "                c_bt = '#2ca02c'",
        "            else:",
        "                c_alm = colors[i % len(colors)]",
        "                c_ta = c_alm",
        "                c_bt = c_alm",
        "            ",
        "            label_suffix = f' {k}' if k else ''",
        "            alm_slice = clean(alm_dict[k])[-150:]",
        "            main_ax.plot(alm_slice, label=f'Almanac{label_suffix}', color=c_alm, linewidth=2.5)",
        "            ",
        "            if k in ta_dict:",
        "                ta_slice = clean(ta_dict[k])[-150:]",
        "                main_ax.plot(ta_slice, label=f'Pandas-TA{label_suffix}', color=c_ta, linestyle='--', linewidth=1.5, alpha=0.8)",
        "            elif not k and ta_data is not None:",
        "                ta_slice = clean(ta_data)[-150:]",
        "                main_ax.plot(ta_slice, label='Pandas-TA', color=c_ta, linestyle='--', linewidth=1.5, alpha=0.8)",
        "                ",
        "            if k in bt_dict:",
        "                bt_slice = clean(bt_dict[k])[-150:]",
        "                main_ax.plot(bt_slice, label=f'Backtrader{label_suffix}', color=c_bt, linestyle=':', linewidth=1.5, alpha=0.8)",
        "            elif not k and bt_data is not None:",
        "                bt_slice = clean(bt_data)[-150:]",
        "                main_ax.plot(bt_slice, label='Backtrader', color=c_bt, linestyle=':', linewidth=1.5, alpha=0.8)",
        "                ",
        "        main_ax.legend(loc='upper left', bbox_to_anchor=(1.01, 1.0), borderaxespad=0.)",
        "        plt.tight_layout()",
        "        plt.show()",
        "    else:",
        "        plt.figure()",
        "        if not isinstance(alm_data, dict):",
        "            alm_dict = {'': alm_data}",
        "            ta_dict = {'': ta_data} if ta_data is not None else {}",
        "            bt_dict = {'': bt_data} if bt_data is not None else {}",
        "        else:",
        "            alm_dict = alm_data",
        "            ta_dict = ta_data if ta_data is not None else {}",
        "            bt_dict = bt_data if bt_data is not None else {}",
        "            ",
        "        colors = ['#1f77b4', '#ff7f0e', '#2ca02c', '#d62728', '#9467bd', '#8c564b', '#e377c2', '#7f7f7f', '#bcbd22', '#17becf']",
        "        key_list = list(alm_dict.keys())",
        "        ",
        "        for i, k in enumerate(key_list):",
        "            if len(key_list) <= 1:",
        "                c_alm = '#1f77b4'",
        "                c_ta = '#ff7f0e'",
        "                c_bt = '#2ca02c'",
        "            else:",
        "                c_alm = colors[i % len(colors)]",
        "                c_ta = c_alm",
        "                c_bt = c_alm",
        "                ",
        "            label_suffix = f' {k}' if k else ''",
        "            plt.plot(clean(alm_dict[k]), label=f'Almanac{label_suffix}', color=c_alm, linewidth=2.5)",
        "            ",
        "            if k in ta_dict:",
        "                plt.plot(clean(ta_dict[k]), label=f'Pandas-TA{label_suffix}', color=c_ta, linestyle='--', linewidth=1.5, alpha=0.8)",
        "            elif not k and ta_data is not None:",
        "                plt.plot(clean(ta_data), label='Pandas-TA', color=c_ta, linestyle='--', linewidth=1.5, alpha=0.8)",
        "                ",
        "            if k in bt_dict:",
        "                plt.plot(clean(bt_dict[k]), label=f'Backtrader{label_suffix}', color=c_bt, linestyle=':', linewidth=1.5, alpha=0.8)",
        "            elif not k and bt_data is not None:",
        "                plt.plot(clean(bt_data), label='Backtrader', color=c_bt, linestyle=':', linewidth=1.5, alpha=0.8)",
        "                ",
        "        plt.title(title)",
        "        plt.legend(loc='upper left', bbox_to_anchor=(1.01, 1.0), borderaxespad=0.)",
        "        plt.tight_layout()",
        "        plt.show()",
        "",
        "# Hàm vẽ biểu đồ OHLCV giới thiệu ở đầu",
        "def plot_ohlcv(df, title='BTCUSDT M1 OHLCV Candlestick & Volume (150 bars)'):",
        "    df_slice = df.tail(150).copy().reset_index(drop=True)",
        "    fig, (ax1, ax2) = plt.subplots(2, 1, sharex=True, gridspec_kw={'height_ratios': [3, 1]}, figsize=(14, 6))",
        "    up = df_slice[df_slice['c'] >= df_slice['o']]",
        "    down = df_slice[df_slice['c'] < df_slice['o']]",
        "    # Draw wicks",
        "    ax1.vlines(up.index, up['l'], up['h'], color='green', linewidth=1.2)",
        "    ax1.vlines(down.index, down['l'], down['h'], color='red', linewidth=1.2)",
        "    # Draw bodies",
        "    ax1.bar(up.index, up['c'] - up['o'], bottom=up['o'], color='green', width=0.6)",
        "    ax1.bar(down.index, down['o'] - down['c'], bottom=down['c'], color='red', width=0.6)",
        "    ax1.set_title(title)",
        "    ax1.set_ylabel('Price (USDT)')",
        "    # Draw volume",
        "    ax2.bar(up.index, up['v'], color='green', width=0.6, alpha=0.5)",
        "    ax2.bar(down.index, down['v'], color='red', width=0.6, alpha=0.5)",
        "    ax2.set_ylabel('Volume')",
        "    ax2.set_xlabel('Bar Index')",
        "    plt.tight_layout()",
        "    plt.show()",
        "",
        "# Custom Aligned DPO for Backtrader to match standard formula",
        "class AlignedDPO(bt.Indicator):",
        "    lines = ('dpo',)",
        "    params = (('period', 20),)",
        "    def __init__(self):",
        "        shift = self.p.period // 2 + 1",
        "        sma = bt.indicators.SimpleMovingAverage(self.data, period=self.p.period)",
        "        self.lines.dpo = self.data(-shift) - sma",
        "",
        "# Helper chạy các chỉ báo trong Backtrader",
        "class BTIndicatorHelper(bt.Strategy):",
        "    params = (('indicators', []),)",
        "    def __init__(self):",
        "        self.inds = {}",
        "        for name, ind_cls, args, kwargs in self.p.indicators:",
        "            resolved_args = []",
        "            for arg in args:",
        "                if arg == 'close': resolved_args.append(self.data.close)",
        "                elif arg == 'high': resolved_args.append(self.data.high)",
        "                elif arg == 'low': resolved_args.append(self.data.low)",
        "                elif arg == 'volume': resolved_args.append(self.data.volume)",
        "                elif arg == 'open': resolved_args.append(self.data.open)",
        "                elif arg == 'data': resolved_args.append(self.data)",
        "                else: resolved_args.append(arg)",
        "            self.inds[name] = ind_cls(*resolved_args, **kwargs)",
        "    def next(self):",
        "        pass",
        "",
        "def run_bt_indicators(df, specs):",
        "    df_bt = df.copy()",
        "    if 'datetime' not in df_bt.columns:",
        "        df_bt['datetime'] = pd.to_datetime(df_bt['t'], unit='ms')",
        "    df_bt.set_index('datetime', inplace=True)",
        "    df_bt = df_bt.rename(columns={'o': 'open', 'h': 'high', 'l': 'low', 'c': 'close', 'v': 'volume'})",
        "    ",
        "    data = bt.feeds.PandasData(dataname=df_bt, openinterest=None)",
        "    cerebro = bt.Cerebro()",
        "    cerebro.adddata(data)",
        "    cerebro.addstrategy(BTIndicatorHelper, indicators=specs)",
        "    results = cerebro.run()",
        "    strat = results[0]",
        "    ",
        "    res = {}",
        "    for name, _, _, _ in specs:",
        "        ind = strat.inds[name]",
        "        aliases = ind.lines.getlinealiases()",
        "        if len(aliases) > 1:",
        "            for i, line_name in enumerate(aliases):",
        "                res[f'{name}_{line_name}'] = list(ind.lines[i].get(size=len(df_bt)))",
        "        else:",
        "            res[name] = list(ind.get(size=len(df_bt)))",
        "    return res"
    ))
    
    # Load dataset & plot OHLCV
    cells.append(make_code(
        "# Load dữ liệu thực tế BTCUSDT M1 (1000 nến)",
        "parquet_path = pathlib.Path('../../../data/testdata/BTCUSDT/M1/BTCUSDT_M1_2026-01.parquet')",
        "tbl = pq.read_table(parquet_path)",
        "df = tbl.to_pandas().tail(1000).reset_index(drop=True)  # Lấy đúng 1000 nến",
        "",
        "close = df['c'].tolist()",
        "high = df['h'].tolist()",
        "low = df['l'].tolist()",
        "open_ = df['o'].tolist()",
        "volume = df['v'].tolist()",
        "",
        "print(f'Loaded {len(df)} candles. Close range: {df[\"c\"].min()} - {df[\"c\"].max()}')",
        "plot_ohlcv(df)"
    ))
    return cells

def write_notebook(filename, cells):
    nb_data = {
        "cells": cells,
        "metadata": {
            "kernelspec": {"display_name": "Python 3 (alm-py venv)", "language": "python", "name": "python3"},
            "language_info": {"name": "python", "pygments_lexer": "ipython3"},
        },
        "nbformat": 4,
        "nbformat_minor": 5,
    }
    out_path = pathlib.Path(__file__).parent / filename
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(nb_data, f, indent=1, ensure_ascii=False)
    print(f"Generated notebook at {out_path.resolve()}")

def generate_trend_notebook():
    cells = get_common_setup(
        "Nhóm 1: Chỉ báo Xu hướng (Trend Indicators)",
        "Notebook này chứa so sánh đối soát cấu trúc và giá trị của các chỉ báo xu hướng."
    )
    
    # Run Backtrader specs for trend
    cells.append(make_code(
        "# Chạy Backtrader specs cho nhóm Trend",
        "bt_specs = [",
        "    ('sma20', bt.indicators.SMA, ['close'], {'period': 20}),",
        "    ('ema20', bt.indicators.EMA, ['close'], {'period': 20}),",
        "    ('wma20', bt.indicators.WMA, ['close'], {'period': 20}),",
        "    ('hma20', bt.indicators.HMA, ['close'], {'period': 20}),",
        "    ('dema20', bt.indicators.DEMA, ['close'], {'period': 20}),",
        "    ('tema20', bt.indicators.TEMA, ['close'], {'period': 20}),",
        "    ('smma20', bt.indicators.SMMA, ['close'], {'period': 20}),",
        "    ('kama', bt.indicators.KAMA, ['close'], {'period': 10, 'fast': 2, 'slow': 30}),",
        "    ('macd', bt.indicators.MACDHisto, ['close'], {'period_me1': 12, 'period_me2': 26, 'period_signal': 9}),",
        "    ('trix', bt.indicators.Trix, ['close'], {'period': 18}),", # Note: Backtrader's Trix is single line, TrixSignal has signal
        "    ('adx', bt.indicators.ADX, ['data'], {'period': 14}),",
        "    ('dmi', bt.indicators.DMI, ['data'], {'period': 14}),",
        "    ('aroon', bt.indicators.AroonUpDown, ['data'], {'period': 25}),",
        "    ('aroon_osc', bt.indicators.AroonOscillator, ['data'], {'period': 25}),",
        "    ('vortex', bt.indicators.Vortex, ['data'], {'period': 14}),",
        "]",
        "bt_results = run_bt_indicators(df, bt_specs)",
        "print('Backtrader trend indicators calculated successfully.')"
    ))
    
    # 1. SMA
    cells.append(make_md("### 1. SMA (Simple Moving Average)", "Đường trung bình động đơn giản."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [sma]')",
        "print('Pandas-TA returns: 1 column [SMA_20]')",
        "print('Backtrader returns: 1 line [sma]')",
        "alm_sma = alm.indicators.sma(close, period=20)",
        "ta_sma = ta.sma(df['c'], length=20)",
        "bt_sma = bt_results['sma20']",
        "print_parity_stats(alm_sma, ta_sma, bt_sma, 'SMA 20')",
        "plot_parity(alm_sma, ta_sma, bt_sma, 'SMA 20 Parity', is_price=True)"
    ))
    
    # 2. EMA
    cells.append(make_md("### 2. EMA (Exponential Moving Average)", "Đường trung bình động lũy thừa."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [ema]')",
        "print('Pandas-TA returns: 1 column [EMA_20]')",
        "print('Backtrader returns: 1 line [ema]')",
        "alm_ema = alm.indicators.ema(close, period=20)",
        "ta_ema = ta.ema(df['c'], length=20)",
        "bt_ema = bt_results['ema20']",
        "print_parity_stats(alm_ema, ta_ema, bt_ema, 'EMA 20')",
        "plot_parity(alm_ema, ta_ema, bt_ema, 'EMA 20 Parity', is_price=True)"
    ))
    
    # 3. WMA
    cells.append(make_md("### 3. WMA (Weighted Moving Average)", "Đường trung bình động có trọng số."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [wma]')",
        "print('Pandas-TA returns: 1 column [WMA_20]')",
        "print('Backtrader returns: 1 line [wma]')",
        "alm_wma = alm.indicators.wma(close, period=20)",
        "ta_wma = ta.wma(df['c'], length=20)",
        "bt_wma = bt_results['wma20']",
        "print_parity_stats(alm_wma, ta_wma, bt_wma, 'WMA 20')",
        "plot_parity(alm_wma, ta_wma, bt_wma, 'WMA 20 Parity', is_price=True)"
    ))
    
    # 4. HMA
    cells.append(make_md("### 4. HMA (Hull Moving Average)", "Đường trung bình động Hull."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [hma]')",
        "print('Pandas-TA returns: 1 column [HMA_20]')",
        "print('Backtrader returns: 1 line [hma]')",
        "alm_hma = alm.indicators.hma(close, period=20)",
        "ta_hma = ta.hma(df['c'], length=20)",
        "bt_hma = bt_results['hma20']",
        "print_parity_stats(alm_hma, ta_hma, bt_hma, 'HMA 20')",
        "plot_parity(alm_hma, ta_hma, bt_hma, 'HMA 20 Parity', is_price=True)"
    ))

    # 5. DEMA
    cells.append(make_md("### 5. DEMA (Double Exponential Moving Average)", "Đường trung bình động lũy thừa kép."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [dema]')",
        "print('Pandas-TA returns: 1 column [DEMA_20]')",
        "print('Backtrader returns: 1 line [dema]')",
        "alm_dema = alm.indicators.dema(close, period=20)",
        "ta_dema = ta.dema(df['c'], length=20)",
        "bt_dema = bt_results['dema20']",
        "print_parity_stats(alm_dema, ta_dema, bt_dema, 'DEMA 20')",
        "plot_parity(alm_dema, ta_dema, bt_dema, 'DEMA 20 Parity', is_price=True)"
    ))

    # 6. TEMA
    cells.append(make_md("### 6. TEMA (Triple Exponential Moving Average)", "Đường trung bình động lũy thừa ba."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [tema]')",
        "print('Pandas-TA returns: 1 column [TEMA_20]')",
        "print('Backtrader returns: 1 line [tema]')",
        "alm_tema = alm.indicators.tema(close, period=20)",
        "ta_tema = ta.tema(df['c'], length=20)",
        "bt_tema = bt_results['tema20']",
        "print_parity_stats(alm_tema, ta_tema, bt_tema, 'TEMA 20')",
        "plot_parity(alm_tema, ta_tema, bt_tema, 'TEMA 20 Parity', is_price=True)"
    ))

    # 7. SMMA
    cells.append(make_md("### 7. SMMA (Smoothed Moving Average)", "Đường trung bình động làm mượt."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [smma]')",
        "print('Pandas-TA returns: 1 column [SMMA_20]')",
        "print('Backtrader returns: 1 line [smma]')",
        "alm_smma = alm.indicators.smma(close, period=20)",
        "ta_smma = ta.smma(df['c'], length=20)",
        "bt_smma = bt_results['smma20']",
        "print_parity_stats(alm_smma, ta_smma, bt_smma, 'SMMA 20')",
        "plot_parity(alm_smma, ta_smma, bt_smma, 'SMMA 20 Parity', is_price=True)"
    ))

    # 8. ALMA
    cells.append(make_md("### 8. ALMA (Arnaud Legoux Moving Average)", "Đường trung bình động Arnaud Legoux."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [alma]')",
        "print('Pandas-TA returns: 1 column [ALMA_9_6.0_0.85]')",
        "print('Backtrader: No equivalent')",
        "alm_alma = alm.indicators.alma(close, period=9, sigma=6.0, offset=0.85)",
        "ta_alma = ta.alma(df['c'], length=9, sigma=6.0, offset=0.85)",
        "print_parity_stats(alm_alma, ta_alma, None, 'ALMA')",
        "plot_parity(alm_alma, ta_alma, None, 'ALMA Parity', is_price=True)"
    ))

    # 9. McGinley Dynamic
    cells.append(make_md("### 9. McGinley Dynamic", "Đường trung bình động McGinley."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [mcginley]')",
        "print('Pandas-TA returns: 1 column [MCGD_14]')",
        "print('Backtrader: No equivalent')",
        "alm_mcg = alm.indicators.mcginley(close, period=14)",
        "ta_mcg = ta.mcgd(df['c'], length=14)",
        "print_parity_stats(alm_mcg, ta_mcg, None, 'McGinley Dynamic')",
        "plot_parity(alm_mcg, ta_mcg, None, 'McGinley Dynamic Parity', is_price=True)"
    ))

    # 10. LSMA
    cells.append(make_md("### 10. LSMA (Least Squares Moving Average)", "Đường trung bình động bình phương tối thiểu. Trả về cả giá trị khớp (value) và giao cắt (intercept)."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [val, intercept]')",
        "print('Pandas-TA returns: 1 column [LR_25] (only value)')",
        "print('Backtrader: No equivalent')",
        "alm_lval, alm_lint = alm.indicators.lsma(close, period=25)",
        "ta_lr = ta.linreg(df['c'], length=25)",
        "print_parity_stats(alm_lval, ta_lr, None, 'LSMA Value')",
        "plot_parity({'Value': alm_lval, 'Intercept': alm_lint}, {'Value': ta_lr}, None, 'LSMA Value & Intercept Parity', is_price=True)"
    ))

    # 11. LSMA Scalar
    cells.append(make_md("### 11. LSMA Scalar", "Chỉ trả về mảng giá trị LSMA."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [lsma_scalar]')",
        "print('Pandas-TA returns: 1 column [LR_25]')",
        "alm_lscal = alm.indicators.lsma_scalar(close, period=25)",
        "ta_lr = ta.linreg(df['c'], length=25)",
        "print_parity_stats(alm_lscal, ta_lr, None, 'LSMA Scalar')",
        "plot_parity(alm_lscal, ta_lr, None, 'LSMA Scalar Parity', is_price=True)"
    ))

    # 12. VWMA
    cells.append(make_md("### 12. VWMA (Volume Weighted Moving Average)", "Đường trung bình động gia trọng khối lượng."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [vwma]')",
        "print('Pandas-TA returns: 1 column [VWMA_20]')",
        "print('Backtrader: No equivalent')",
        "alm_vwma = alm.indicators.vwma(close, volume, period=20)",
        "ta_vwma = ta.vwma(df['c'], df['v'], length=20)",
        "print_parity_stats(alm_vwma, ta_vwma, None, 'VWMA 20')",
        "plot_parity(alm_vwma, ta_vwma, None, 'VWMA 20 Parity', is_price=True)"
    ))

    # 13. KAMA
    cells.append(make_md("### 13. KAMA (Kaufman Adaptive Moving Average)", "Đường trung bình động thích ứng Kaufman."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [kama]')",
        "print('Pandas-TA returns: 1 column [KAMA_10_2_30]')",
        "print('Backtrader returns: 1 line [kama]')",
        "alm_kama = alm.indicators.kama(close, er_period=10, fast=2, slow=30)",
        "ta_kama = ta.kama(df['c'], length=10, fast=2, slow=30)",
        "bt_kama = bt_results['kama']",
        "print_parity_stats(alm_kama, ta_kama, bt_kama, 'KAMA', warmup=250)",
        "plot_parity(alm_kama, ta_kama, bt_kama, 'KAMA Parity', is_price=True)"
    ))

    # 14. MACD
    cells.append(make_md("### 14. MACD (Moving Average Convergence Divergence)", "Đường trung bình động hội tụ phân kỳ."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [macd, signal, hist]')",
        "print('Pandas-TA returns: 3 columns starting with MACD_, MACDs_, MACDh_')",
        "print('Backtrader returns: 3 lines [macd, signal, histo]')",
        "alm_macd, alm_macd_sig, alm_macd_hist = alm.indicators.macd(close, fast=12, slow=26, signal=9)",
        "ta_macd_df = ta.macd(df['c'], fast=12, slow=26, signal=9)",
        "macd_col = [c for c in ta_macd_df.columns if c.startswith('MACD_')][0]",
        "macd_sig_col = [c for c in ta_macd_df.columns if c.startswith('MACDs_')][0]",
        "macd_hist_col = [c for c in ta_macd_df.columns if c.startswith('MACDh_')][0]",
        "ta_macd = ta_macd_df[macd_col]",
        "ta_sig = ta_macd_df[macd_sig_col]",
        "ta_hist = ta_macd_df[macd_hist_col]",
        "bt_macd = bt_results['macd_macd']",
        "bt_sig = bt_results['macd_signal']",
        "bt_hist = bt_results['macd_histo']",
        "print_parity_stats(alm_macd, ta_macd, bt_macd, 'MACD Line', warmup=100)",
        "print_parity_stats(alm_macd_sig, ta_sig, bt_sig, 'MACD Signal', warmup=100)",
        "print_parity_stats(alm_macd_hist, ta_hist, bt_hist, 'MACD Histogram', warmup=100)",
        "plot_parity({'MACD': alm_macd, 'Signal': alm_macd_sig, 'Hist': alm_macd_hist}, {'MACD': ta_macd, 'Signal': ta_sig, 'Hist': ta_hist}, {'MACD': bt_macd, 'Signal': bt_sig, 'Hist': bt_hist}, 'MACD Parity')"
    ))

    # 15. TRIX
    cells.append(make_md("### 15. TRIX", "Đường trung bình động lũy thừa ba mũ."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [trix, signal, hist]')",
        "print('Pandas-TA returns: 2 columns [TRIX_18_9, TRIXs_18_9]')",
        "print('Backtrader returns: 1 line [trix] (single line Trix)')",
        "alm_trix, alm_trix_sig, alm_trix_hist = alm.indicators.trix(close, period=18, signal=9)",
        "ta_trix_df = ta.trix(df['c'], length=18, signal=9)",
        "trix_col = [c for c in ta_trix_df.columns if c.startswith('TRIX_')][0]",
        "trix_sig_col = [c for c in ta_trix_df.columns if c.startswith('TRIXs_')][0]",
        "ta_trix = ta_trix_df[trix_col]",
        "ta_trix_sig = ta_trix_df[trix_sig_col]",
        "ta_trix_hist = ta_trix - ta_trix_sig",
        "bt_trix = bt_results['trix']",
        "print_parity_stats(alm_trix, ta_trix, bt_trix, 'TRIX Line', warmup=150)",
        "print_parity_stats(alm_trix_sig, ta_trix_sig, None, 'TRIX Signal', warmup=150)",
        "print_parity_stats(alm_trix_hist, ta_trix_hist, None, 'TRIX Histogram', warmup=150)",
        "plot_parity({'TRIX': alm_trix, 'Signal': alm_trix_sig, 'Hist': alm_trix_hist}, {'TRIX': ta_trix, 'Signal': ta_trix_sig, 'Hist': ta_trix_hist}, {'TRIX': bt_trix}, 'TRIX Parity')"
    ))

    # 16. ADX
    cells.append(make_md("### 16. ADX (Average Directional Index)", "Chỉ báo định hướng trung bình."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [adx, plus_di, minus_di]')",
        "print('Pandas-TA returns: 3 columns starting with ADX_, DMP_, DMN_')",
        "print('Backtrader returns: 1 line [adx]')",
        "alm_adx, alm_pdi, alm_mdi = alm.indicators.adx(high, low, close, period=14)",
        "ta_adx_df = ta.adx(df['h'], df['l'], df['c'], length=14)",
        "adx_col = [c for c in ta_adx_df.columns if c.startswith('ADX_')][0]",
        "pdi_col = [c for c in ta_adx_df.columns if c.startswith('DMP_')][0]",
        "mdi_col = [c for c in ta_adx_df.columns if c.startswith('DMN_')][0]",
        "ta_adx = ta_adx_df[adx_col]",
        "ta_pdi = ta_adx_df[pdi_col]",
        "ta_mdi = ta_adx_df[mdi_col]",
        "bt_adx = bt_results['adx']",
        "print_parity_stats(alm_adx, ta_adx, bt_adx, 'ADX', warmup=200)",
        "print_parity_stats(alm_pdi, ta_pdi, None, 'Plus DI', warmup=200)",
        "print_parity_stats(alm_mdi, ta_mdi, None, 'Minus DI', warmup=200)",
        "plot_parity({'ADX': alm_adx, '+DI': alm_pdi, '-DI': alm_mdi}, {'ADX': ta_adx, '+DI': ta_pdi, '-DI': ta_mdi}, {'ADX': bt_adx}, 'ADX Parity')"
    ))

    # 17. DMI
    cells.append(make_md("### 17. DMI (Directional Movement Index)", "Chỉ báo chuyển động định hướng."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [plus_di, minus_di]')",
        "print('Pandas-TA returns: 3 columns (contains DMP_, DMN_, ADX_)')",
        "print('Backtrader returns: 2 lines [plusDI, minusDI]')",
        "alm_pdi2, alm_mdi2 = alm.indicators.dmi(high, low, close, period=14)",
        "bt_pdi = bt_results['dmi_plusDI']",
        "bt_mdi = bt_results['dmi_minusDI']",
        "print_parity_stats(alm_pdi2, ta_pdi, bt_pdi, 'Plus DI (DMI)', warmup=200)",
        "print_parity_stats(alm_mdi2, ta_mdi, bt_mdi, 'Minus DI (DMI)', warmup=200)",
        "plot_parity({'+DI': alm_pdi2, '-DI': alm_mdi2}, {'+DI': ta_pdi, '-DI': ta_mdi}, {'+DI': bt_pdi, '-DI': bt_mdi}, 'DMI Parity')"
    ))

    # 18. Aroon
    cells.append(make_md("### 18. Aroon", "Chỉ báo Aroon."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [aroon_up, aroon_down]')",
        "print('Pandas-TA returns: 2 columns [AROONU_25, AROOND_25]')",
        "print('Backtrader returns: 2 lines [aroonup, aroondown]')",
        "alm_arup, alm_ardn = alm.indicators.aroon(high, low, period=25)",
        "ta_aroon = ta.aroon(df['h'], df['l'], length=25)",
        "arup_col = [c for c in ta_aroon.columns if c.startswith('AROONU')][0]",
        "ardn_col = [c for c in ta_aroon.columns if c.startswith('AROOND')][0]",
        "ta_arup = ta_aroon[arup_col]",
        "ta_ardn = ta_aroon[ardn_col]",
        "bt_arup = bt_results['aroon_aroonup']",
        "bt_ardn = bt_results['aroon_aroondown']",
        "print_parity_stats(alm_arup, ta_arup, bt_arup, 'Aroon Up', warmup=150)",
        "print_parity_stats(alm_ardn, ta_ardn, bt_ardn, 'Aroon Down', warmup=150)",
        "plot_parity({'Aroon Up': alm_arup, 'Aroon Down': alm_ardn}, {'Aroon Up': ta_arup, 'Aroon Down': ta_ardn}, {'Aroon Up': bt_arup, 'Aroon Down': bt_ardn}, 'Aroon Parity')"
    ))

    # 19. Aroon Oscillator
    cells.append(make_md("### 19. Aroon Oscillator", "Chỉ báo dao động Aroon."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [aroon_osc]')",
        "print('Pandas-TA returns: 1 column [AROONOSC_25]')",
        "print('Backtrader returns: 1 line [aroonosc]')",
        "alm_arosc = alm.indicators.aroon_osc(high, low, period=25)",
        "ta_arosc = ta_aroon[[c for c in ta_aroon.columns if c.startswith('AROONOSC')][0]]",
        "bt_arosc = bt_results['aroon_osc']",
        "print_parity_stats(alm_arosc, ta_arosc, bt_arosc, 'Aroon Oscillator', warmup=150)",
        "plot_parity(alm_arosc, ta_arosc, bt_arosc, 'Aroon Oscillator Parity')"
    ))

    # 20. Vortex
    cells.append(make_md("### 20. Vortex", "Chỉ báo Vortex."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [plus_vi, minus_vi]')",
        "print('Pandas-TA returns: 2 columns [VTXP_14, VTXM_14]')",
        "print('Backtrader returns: 2 lines [vortex_p, vortex_m]')",
        "alm_vtxp, alm_vtxm = alm.indicators.vortex(high, low, period=14)",
        "ta_vortex = ta.vortex(df['h'], df['l'], df['c'], length=14)",
        "vtxp_col = [c for c in ta_vortex.columns if c.startswith('VTXP')][0]",
        "vtxm_col = [c for c in ta_vortex.columns if c.startswith('VTXM')][0]",
        "ta_vtxp = ta_vortex[vtxp_col]",
        "ta_vtxm = ta_vortex[vtxm_col]",
        "bt_vtxp = bt_results['vortex_vi_plus']",
        "bt_vtxm = bt_results['vortex_vi_minus']",
        "print_parity_stats(alm_vtxp, ta_vtxp, bt_vtxp, 'Vortex VI+', warmup=100)",
        "print_parity_stats(alm_vtxm, ta_vtxm, bt_vtxm, 'Vortex VI-', warmup=100)",
        "plot_parity({'VI+': alm_vtxp, 'VI-': alm_vtxm}, {'VI+': ta_vtxp, 'VI-': ta_vtxm}, {'VI+': bt_vtxp, 'VI-': bt_vtxm}, 'Vortex Parity')"
    ))

    # 21. Alligator
    cells.append(make_md("### 21. Alligator", "Chỉ báo Alligator (Hàm răng Cá sấu) gồm Hàm (Jaw), Răng (Teeth), Môi (Lips)."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [jaw, teeth, lips]')",
        "print('Pandas-TA: No direct equivalent')",
        "print('Backtrader: No direct equivalent')",
        "alm_jaw, alm_teeth, alm_lips = alm.indicators.alligator(high, low, close, jaw=13, teeth=8, lips=5)",
        "hl2_val = (df['h'] + df['l']) / 2.0",
        "ta_jaw = ta.rma(hl2_val, length=13).shift(8)",
        "ta_teeth = ta.rma(hl2_val, length=8).shift(5)",
        "ta_lips = ta.rma(hl2_val, length=5).shift(3)",
        "print_parity_stats(alm_jaw, ta_jaw, None, 'Alligator Jaw')",
        "print_parity_stats(alm_teeth, ta_teeth, None, 'Alligator Teeth')",
        "print_parity_stats(alm_lips, ta_lips, None, 'Alligator Lips')",
        "plot_parity({'Jaw': alm_jaw, 'Teeth': alm_teeth, 'Lips': alm_lips}, {'Jaw': ta_jaw, 'Teeth': ta_teeth, 'Lips': ta_lips}, None, 'Alligator Parity', is_price=True)"
    ))

    # 22. GMMA
    cells.append(make_md("### 22. GMMA (Guppy Multiple Moving Average)", "Đường trung bình động đa lớp Guppy."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: Dictionary containing 14 keys [long_0..5, short_0..5, spread, bullish]')",
        "print('Pandas-TA: No built-in GMMA. Comparing short_0 to manual EMA 3.')",
        "print('Backtrader: No built-in GMMA.')",
        "alm_gmma_dict = alm.indicators.gmma(close)",
        "print('Almanac GMMA keys:', list(alm_gmma_dict.keys()))",
        "ta_ema3 = ta.ema(df['c'], length=3)",
        "print_parity_stats(alm_gmma_dict['short_0'], ta_ema3, None, 'GMMA Short 0 (EMA 3)')",
        "gmma_plots = {k: alm_gmma_dict[k] for k in alm_gmma_dict if k.startswith('short_') or k.startswith('long_')}",
        "plot_parity(gmma_plots, {'short_0': ta_ema3}, None, 'GMMA Parity', is_price=True)"
    ))

    # 23. KDJ
    cells.append(make_md("### 23. KDJ", "Chỉ báo KDJ."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [k, d, j]')",
        "print('Pandas-TA returns: 3 columns [K_9_3, D_9_3, J_9_3]')",
        "alm_k, alm_d, alm_j = alm.indicators.kdj(high, low, close, period=9, k_period=3, d_period=3)",
        "ta_kdj = ta.kdj(df['h'], df['l'], df['c'], length=9, signal=3)",
        "ta_k = ta_kdj[[c for c in ta_kdj.columns if c.startswith('K_')][0]]",
        "ta_d = ta_kdj[[c for c in ta_kdj.columns if c.startswith('D_')][0]]",
        "ta_j = ta_kdj[[c for c in ta_kdj.columns if c.startswith('J_')][0]]",
        "print('--- Pandas-TA default (RMA smoothed) ---')",
        "print_parity_stats(alm_k, ta_k, None, 'KDJ %K')",
        "print_parity_stats(alm_d, ta_d, None, 'KDJ %D')",
        "print_parity_stats(alm_j, ta_j, None, 'KDJ %J')",
        "highest_hh = df['h'].rolling(9).max()",
        "lowest_ll = df['l'].rolling(9).min()",
        "rsv_val = 100 * (df['c'] - lowest_ll) / (highest_hh - lowest_ll)",
        "ta_k_sma = rsv_val.rolling(3).mean()",
        "ta_d_sma = ta_k_sma.rolling(3).mean()",
        "ta_j_sma = 3 * ta_k_sma - 2 * ta_d_sma",
        "print('--- Manual SMA-based KDJ (Matches Almanac exactly) ---')",
        "print_parity_stats(alm_k, ta_k_sma, None, 'KDJ %K (SMA)')",
        "print_parity_stats(alm_d, ta_d_sma, None, 'KDJ %D (SMA)')",
        "print_parity_stats(alm_j, ta_j_sma, None, 'KDJ %J (SMA)')",
        "plot_parity({'K': alm_k, 'D': alm_d, 'J': alm_j}, {'K': ta_k_sma, 'D': ta_d_sma, 'J': ta_j_sma}, None, 'KDJ Parity')"
    ))

    # 24. Kalman Filter
    cells.append(make_md("### 24. Kalman Filter", "Bộ lọc Kalman thích ứng."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [value, velocity]')",
        "alm_kval, alm_kvel = alm.indicators.kalman(close)",
        "print(f'Kalman output lengths: value ({len(alm_kval)}), velocity ({len(alm_kvel)})')",
        "plot_parity({'Value': alm_kval}, None, None, 'Kalman Value', is_price=True)",
        "plot_parity({'Velocity': alm_kvel}, None, None, 'Kalman Velocity', is_price=False)"
    ))
    
    write_notebook("trend_indicators.ipynb", cells)

def generate_momentum_notebook():
    cells = get_common_setup(
        "Nhóm 2: Chỉ báo Động lượng & Dao động (Momentum & Oscillators)",
        "Notebook này đối soát các chỉ báo động lượng như RSI, Stochastic, Williams %R, CCI, AO, Coppock..."
    )
    
    # Run Backtrader specs for momentum
    cells.append(make_code(
        "# Chạy Backtrader specs cho nhóm Momentum",
        "bt_specs = [",
        "    ('rsi14', bt.indicators.RSI, ['close'], {'period': 14}),",
        "    ('cci20', bt.indicators.CCI, ['data'], {'period': 20}),",
        "    ('roc10', bt.indicators.ROC100, ['close'], {'period': 10}),",
        "    ('mom10', bt.indicators.Momentum, ['close'], {'period': 10}),",
        "    ('dpo20', AlignedDPO, ['close'], {'period': 20}),",
        "    ('wr14', bt.indicators.WilliamsR, ['data'], {'period': 14}),",
        "    ('stoch', bt.indicators.Stochastic, ['data'], {'period': 14, 'period_dfast': 3, 'period_dslow': 3}),",
        "    ('tsi25', bt.indicators.TSI, ['close'], {'period1': 25, 'period2': 13}),",
        "    ('kst', bt.indicators.KST, ['close'], {",
        "        'rp1': 10, 'rp2': 13, 'rp3': 14, 'rp4': 15,",
        "        'rma1': 10, 'rma2': 13, 'rma3': 14, 'rma4': 15,",
        "        'rsignal': 9",
        "    }),",
        "    ('ppo', bt.indicators.PPO, ['close'], {'period1': 12, 'period2': 26, 'period_signal': 9}),",
        "    ('uo', bt.indicators.UltimateOscillator, ['data'], {'p1': 7, 'p2': 14, 'p3': 28}),",
        "    ('ao', bt.indicators.AO, ['data'], {'fast': 5, 'slow': 34}),",
        "]",
        "bt_results = run_bt_indicators(df, bt_specs)",
        "print('Backtrader momentum indicators calculated successfully.')"
    ))
    
    # 1. RSI
    cells.append(make_md("### 1. RSI (Relative Strength Index)", "Chỉ số sức mạnh tương đối."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [rsi]')",
        "print('Pandas-TA returns: 1 column [RSI_14]')",
        "print('Backtrader returns: 1 line [rsi]')",
        "alm_rsi = alm.indicators.rsi(close, period=14)",
        "ta_rsi = ta.rsi(df['c'], length=14)",
        "bt_rsi = bt_results['rsi14']",
        "print_parity_stats(alm_rsi, ta_rsi, bt_rsi, 'RSI 14', warmup=200)",
        "plot_parity(alm_rsi, ta_rsi, bt_rsi, 'RSI 14 Parity')"
    ))

    # 2. CCI
    cells.append(make_md("### 2. CCI (Commodity Channel Index)", "Chỉ số kênh hàng hóa. Lưu ý lỗi toán học của Pandas-TA fallback và thuật toán MeanDeviation của Backtrader như đã báo cáo."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [cci]')",
        "print('Pandas-TA returns: 1 column [CCI_20_0.015]')",
        "print('Backtrader returns: 1 line [cci]')",
        "alm_cci = alm.indicators.cci(high, low, close, period=20)",
        "ta_cci = ta.cci(df['h'], df['l'], df['c'], length=20)",
        "bt_cci = bt_results['cci20']",
        "print_parity_stats(alm_cci, ta_cci, bt_cci, 'CCI 20')",
        "plot_parity(alm_cci, ta_cci, bt_cci, 'CCI 20 Parity')"
    ))

    # 3. ROC
    cells.append(make_md("### 3. ROC (Rate of Change)", "Tốc độ thay đổi giá."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [roc]')",
        "print('Pandas-TA returns: 1 column [ROC_10]')",
        "print('Backtrader returns: 1 line [roc100] (using ROC100 for correct % scale)')",
        "alm_roc = alm.indicators.roc(close, period=10)",
        "ta_roc = ta.roc(df['c'], length=10)",
        "bt_roc = bt_results['roc10']",
        "print_parity_stats(alm_roc, ta_roc, bt_roc, 'ROC 10')",
        "plot_parity(alm_roc, ta_roc, bt_roc, 'ROC 10 Parity')"
    ))

    # 4. MOM
    cells.append(make_md("### 4. MOM (Momentum)", "Chỉ báo động lượng."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [mom]')",
        "print('Pandas-TA returns: 1 column [MOM_10]')",
        "print('Backtrader returns: 1 line [momentum]')",
        "alm_mom = alm.indicators.mom(close, period=10)",
        "ta_mom = ta.mom(df['c'], length=10)",
        "bt_mom = bt_results['mom10']",
        "print_parity_stats(alm_mom, ta_mom, bt_mom, 'Momentum 10')",
        "plot_parity(alm_mom, ta_mom, bt_mom, 'Momentum 10 Parity')"
    ))

    # 5. CMO
    cells.append(make_md("### 5. CMO (Chande Momentum Oscillator)", "Chỉ báo dao động động lượng Chande."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [cmo]')",
        "print('Pandas-TA returns: 1 column [CMO_14]')",
        "alm_cmo = alm.indicators.cmo(close, period=14)",
        "ta_cmo = ta.cmo(df['c'], length=14)",
        "print_parity_stats(alm_cmo, ta_cmo, None, 'CMO 14')",
        "plot_parity(alm_cmo, ta_cmo, None, 'CMO 14 Parity')"
    ))

    # 6. DPO
    cells.append(make_md("### 6. DPO (Detrended Price Oscillator)", "Chỉ báo dao động loại bỏ xu hướng."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [dpo]')",
        "print('Pandas-TA returns: 1 column [DPO_20]')",
        "print('Backtrader returns: 1 line [dpo]')",
        "alm_dpo = alm.indicators.dpo(close, period=20)",
        "ta_dpo = ta.dpo(df['c'], length=20).shift(11)",
        "bt_dpo = bt_results['dpo20']",
        "print_parity_stats(alm_dpo, ta_dpo, bt_dpo, 'DPO 20')",
        "plot_parity(alm_dpo, ta_dpo, bt_dpo, 'DPO 20 Parity')"
    ))

    # 7. MFI
    cells.append(make_md("### 7. MFI (Money Flow Index)", "Chỉ số dòng tiền."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [mfi]')",
        "print('Pandas-TA returns: 1 column [MFI_14]')",
        "alm_mfi = alm.indicators.mfi(high, low, close, volume, period=14)",
        "ta_mfi = ta.mfi(df['h'], df['l'], df['c'], df['v'], length=14)",
        "print_parity_stats(alm_mfi, ta_mfi, None, 'MFI 14')",
        "plot_parity(alm_mfi, ta_mfi, None, 'MFI 14 Parity')"
    ))

    # 8. BOP
    cells.append(make_md("### 8. BOP (Balance of Power)", "Chỉ báo cân bằng lực mua bán."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [bop]')",
        "print('Pandas-TA returns: 1 column [BOP]')",
        "alm_bop = alm.indicators.bop(open_, high, low, close)",
        "ta_bop = ta.bop(df['o'], df['h'], df['l'], df['c'])",
        "print_parity_stats(alm_bop, ta_bop, None, 'BOP')",
        "plot_parity(alm_bop, ta_bop, None, 'BOP Parity')"
    ))

    # 9. Williams %R
    cells.append(make_md("### 9. Williams %R", "Chỉ báo dao động Williams %R."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [williams_r]')",
        "print('Pandas-TA returns: 1 column [WILLR_14]')",
        "print('Backtrader returns: 1 line [williamsr]')",
        "alm_wr = alm.indicators.williams_r(high, low, close, period=14)",
        "ta_wr = ta.willr(df['h'], df['l'], df['c'], length=14)",
        "bt_wr = bt_results['wr14']",
        "print_parity_stats(alm_wr, ta_wr, bt_wr, 'Williams %R')",
        "plot_parity(alm_wr, ta_wr, bt_wr, 'Williams %R Parity')"
    ))

    # 10. Stochastic
    cells.append(make_md("### 10. Stochastic Oscillator", "Chỉ báo dao động Stochastic."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [stoch_k, stoch_d]')",
        "print('Pandas-TA returns: 2 columns starting with STOCHk_, STOCHd_')",
        "print('Backtrader returns: 2 lines [percK, percD]')",
        "alm_sk, alm_sd = alm.indicators.stochastic(high, low, close, k_period=14, smooth_k=3, d_period=3)",
        "ta_stoch = ta.stoch(df['h'], df['l'], df['c'], k=14, d=3, smooth_k=3)",
        "sk_col = [c for c in ta_stoch.columns if c.startswith('STOCHk')][0]",
        "sd_col = [c for c in ta_stoch.columns if c.startswith('STOCHd')][0]",
        "ta_sk = ta_stoch[sk_col]",
        "ta_sd = ta_stoch[sd_col]",
        "bt_sk = bt_results['stoch_percK']",
        "bt_sd = bt_results['stoch_percD']",
        "print_parity_stats(alm_sk, ta_sk, bt_sk, 'Stochastic %K')",
        "print_parity_stats(alm_sd, ta_sd, bt_sd, 'Stochastic %D')",
        "plot_parity({'%K': alm_sk, '%D': alm_sd}, {'%K': ta_sk, '%D': ta_sd}, {'%K': bt_sk, '%D': bt_sd}, 'Stochastic Parity')"
    ))

    # 11. Stochastic RSI
    cells.append(make_md("### 11. Stochastic RSI", "Chỉ báo Stochastic áp dụng trên RSI."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [stoch_rsi_k, stoch_rsi_d]')",
        "print('Pandas-TA returns: 2 columns starting with STOCHRSIk_, STOCHRSId_')",
        "alm_srk, alm_srd = alm.indicators.stoch_rsi(close, rsi_period=14, smooth_d=3)",
        "ta_stochrsi = ta.stochrsi(df['c'], length=14, rsi_length=14, k=3, d=3)",
        "srk_col = [c for c in ta_stochrsi.columns if c.startswith('STOCHRSIk')][0]",
        "srd_col = [c for c in ta_stochrsi.columns if c.startswith('STOCHRSId')][0]",
        "ta_srk = ta_stochrsi[srk_col]",
        "ta_srd = ta_stochrsi[srd_col]",
        "alm_srk_scaled = [x * 100.0 if x is not None else None for x in alm_srk]",
        "alm_srd_scaled = [x * 100.0 if x is not None else None for x in alm_srd]",
        "print_parity_stats(alm_srk_scaled, ta_srk, None, 'StochRSI %K')",
        "print_parity_stats(alm_srd_scaled, ta_srd, None, 'StochRSI %D')",
        "plot_parity({'%K': alm_srk_scaled, '%D': alm_srd_scaled}, {'%K': ta_srk, '%D': ta_srd}, None, 'StochRSI Parity')"
    ))

    # 12. TSI (True Strength Index)
    cells.append(make_md("### 12. TSI (True Strength Index)", "Chỉ số sức mạnh thực."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [value] (nested in 3-tuple signature for compatibility)')",
        "print('Pandas-TA returns: 2 columns [TSI_25_13, TSIs_25_13] (main & signal)')",
        "print('Backtrader returns: 1 line [tsi]')",
        "alm_tsi, _, _ = alm.indicators.tsi(close, first=25, second=13)",
        "ta_tsi_df = ta.tsi(df['c'], fast=13, slow=25)", # note: pandas_ta labels first as slow, second as fast
        "tsi_col = [c for c in ta_tsi_df.columns if c.startswith('TSI_')][0]",
        "ta_tsi = ta_tsi_df[tsi_col]",
        "bt_tsi = bt_results['tsi25']",
        "print_parity_stats(alm_tsi, ta_tsi, bt_tsi, 'TSI Line')",
        "plot_parity(alm_tsi, ta_tsi, bt_tsi, 'TSI Line Parity')"
    ))

    # 13. RCI (Rank Correlation Index)
    cells.append(make_md("### 13. RCI (Rank Correlation Index)", "Chỉ số tương quan hạng."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [rci]')",
        "alm_rci = alm.indicators.rci(close, period=9)",
        "print(f'RCI output length: {len(alm_rci)}')",
        "plot_parity(alm_rci, None, None, 'RCI 9')"
    ))

    # 14. Bull/Bear Power
    cells.append(make_md("### 14. Bull / Bear Power", "Lực mua (Bull) và lực bán (Bear) của thị trường."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [bull, bear]')",
        "alm_bull, alm_bear = alm.indicators.bull_bear(high, low, close, period=13)",
        "print(f'Bull power len: {len(alm_bull)} | Bear power len: {len(alm_bear)}')",
        "plot_parity({'Bull Power': alm_bull, 'Bear Power': alm_bear}, None, None, 'Bull & Bear Power')"
    ))

    # 15. Fisher Transform
    cells.append(make_md("### 15. Fisher Transform", "Phép biến đổi Fisher."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [fisher, signal]')",
        "print('Pandas-TA returns: 2 columns [FISHERt_9_1, FISHERts_9_1]')",
        "alm_fish, alm_fishs = alm.indicators.fisher(high, low, close, period=9)",
        "ta_fisher = ta.fisher(df['h'], df['l'], length=9)",
        "fish_col = [c for c in ta_fisher.columns if 'FISHERT_' in c][0]",
        "fishs_col = [c for c in ta_fisher.columns if 'FISHERTs_' in c][0]",
        "ta_fish = ta_fisher[fish_col]",
        "ta_fishs = ta_fisher[fishs_col]",
        "print_parity_stats(alm_fish, ta_fish, None, 'Fisher Transform')",
        "print_parity_stats(alm_fishs, ta_fishs, None, 'Fisher Signal')",
        "plot_parity({'Fisher': alm_fish, 'Signal': alm_fishs}, {'Fisher': ta_fish, 'Signal': ta_fishs}, None, 'Fisher Transform Parity')"
    ))

    # 16. KST (Know Sure Thing)
    cells.append(make_md("### 16. KST (Know Sure Thing)", "Chỉ báo KST."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: Dictionary containing 3 keys [kst, signal, histogram]')",
        "print('Pandas-TA returns: 2 columns [KST_10_15_20_30, KSTs_9]')",
        "print('Backtrader returns: 2 lines [kst, kstsig]')",
        "alm_kst_dict = alm.indicators.kst(close)",
        "ta_kst = ta.kst(df['c'], roc1=10, roc2=13, roc3=14, roc4=15, sma1=10, sma2=13, sma3=14, sma4=15, signal=9)",
        "kst_col = [c for c in ta_kst.columns if c.startswith('KST_')][0]",
        "ksts_col = [c for c in ta_kst.columns if c.startswith('KSTs_')][0]",
        "ta_k = ta_kst[kst_col]",
        "ta_ks = ta_kst[ksts_col]",
        "bt_k = bt_results['kst_kst']",
        "bt_ks = bt_results['kst_signal']",
        "alm_k_scaled = [x * 100.0 if x is not None else None for x in alm_kst_dict['kst']]",
        "alm_ks_scaled = [x * 100.0 if x is not None else None for x in alm_kst_dict['signal']]",
        "alm_kh_scaled = [x * 100.0 if x is not None else None for x in alm_kst_dict['histogram']]",
        "bt_k_scaled = [x * 100.0 if x is not None else None for x in bt_k]",
        "bt_ks_scaled = [x * 100.0 if x is not None else None for x in bt_ks]",
        "print('Almanac KST keys:', list(alm_kst_dict.keys()))",
        "print_parity_stats(alm_k_scaled, ta_k, bt_k_scaled, 'KST Line')",
        "print_parity_stats(alm_ks_scaled, ta_ks, bt_ks_scaled, 'KST Signal Line')",
        "plot_parity({'KST': alm_k_scaled, 'Signal': alm_ks_scaled, 'Hist': alm_kh_scaled}, {'KST': ta_k, 'Signal': ta_ks}, {'KST': bt_k_scaled, 'Signal': bt_ks_scaled}, 'KST Parity')"
    ))

    # 17. PMO (Price Momentum Oscillator)
    cells.append(make_md("### 17. PMO (Price Momentum Oscillator)", "Chỉ báo dao động động lượng giá PMO."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [pmo, signal, hist]')",
        "alm_pmo, alm_pmos, alm_pmoh = alm.indicators.pmo(close, smooth1=35, smooth2=20, signal=10)",
        "print(f'PMO output lengths: pmo ({len(alm_pmo)}), signal ({len(alm_pmos)})')",
        "plot_parity({'PMO': alm_pmo, 'Signal': alm_pmos, 'Hist': alm_pmoh}, None, None, 'PMO Parity')"
    ))

    # 18. PPO (Percentage Price Oscillator)
    cells.append(make_md("### 18. PPO (Percentage Price Oscillator)", "Chỉ báo dao động giá phần trăm."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [ppo, signal, hist]')",
        "print('Pandas-TA returns: 3 columns starting with PPO_, PPOs_, PPOh_')",
        "print('Backtrader returns: 2 lines [ppo, pposig]')",
        "alm_ppo, alm_ppos, alm_ppoh = alm.indicators.ppo(close, fast=12, slow=26, signal=9)",
        "ta_ppo_df = ta.ppo(df['c'], fast=12, slow=26, signal=9)",
        "ppo_col = [c for c in ta_ppo_df.columns if c.startswith('PPO_')][0]",
        "ppos_col = [c for c in ta_ppo_df.columns if c.startswith('PPOs_')][0]",
        "ta_ppo = ta_ppo_df[ppo_col]",
        "ta_ppos = ta_ppo_df[ppos_col]",
        "bt_ppo = bt_results['ppo_ppo']",
        "bt_ppos = bt_results['ppo_signal']",
        "print_parity_stats(alm_ppo, ta_ppo, bt_ppo, 'PPO Line', warmup=100)",
        "print_parity_stats(alm_ppos, ta_ppos, bt_ppos, 'PPO Signal Line', warmup=100)",
        "plot_parity({'PPO': alm_ppo, 'Signal': alm_ppos, 'Hist': alm_ppoh}, {'PPO': ta_ppo, 'Signal': ta_ppos}, {'PPO': bt_ppo, 'Signal': bt_ppos}, 'PPO Parity')"
    ))

    # 19. RVI (Relative Vigor Index)
    cells.append(make_md("### 19. RVI (Relative Vigor Index)", "Chỉ số sinh lực tương đối."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [rvi, signal]')",
        "print('Pandas-TA returns: 2 columns [RVGI_10_4, RVGIs_10_4]')",
        "alm_rvi, alm_rvis = alm.indicators.rvi(open_, high, low, close, period=10)",
        "ta_rvi_df = ta.rvgi(df['o'], df['h'], df['l'], df['c'], length=10)",
        "rvi_col = [c for c in ta_rvi_df.columns if c.startswith('RVGI_')][0]",
        "rvis_col = [c for c in ta_rvi_df.columns if c.startswith('RVGIs_')][0]",
        "ta_rvi = ta_rvi_df[rvi_col]",
        "ta_rvis = ta_rvi_df[rvis_col]",
        "print_parity_stats(alm_rvi, ta_rvi, None, 'RVI Line')",
        "print_parity_stats(alm_rvis, ta_rvis, None, 'RVI Signal')",
        "plot_parity({'RVI': alm_rvi, 'Signal': alm_rvis}, {'RVI': ta_rvi, 'Signal': ta_rvis}, None, 'RVI Parity')"
    ))

    # 20. SMI (Stochastic Momentum Index)
    cells.append(make_md("### 20. SMI (Stochastic Momentum Index)", "Chỉ số động lượng Stochastic."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [smi, signal]')",
        "alm_smi, alm_smis = alm.indicators.smi(high, low, close, period=13, smooth1=25, smooth2=2, signal=9)",
        "print(f'SMI line len: {len(alm_smi)} | SMI signal len: {len(alm_smis)}')",
        "plot_parity({'SMI': alm_smi, 'Signal': alm_smis}, None, None, 'SMI Parity')"
    ))

    # 21. UO (Ultimate Oscillator)
    cells.append(make_md("### 21. UO (Ultimate Oscillator)", "Chỉ báo Ultimate Oscillator."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [uo]')",
        "print('Pandas-TA returns: 1 column [UO_7_14_28]')",
        "print('Backtrader returns: 1 line [uo]')",
        "alm_uo = alm.indicators.uo(high, low, close, fast=7, medium=14, slow=28)",
        "ta_uo = ta.uo(df['h'], df['l'], df['c'], fast=7, medium=14, slow=28)",
        "bt_uo = bt_results['uo']",
        "print_parity_stats(alm_uo, ta_uo, bt_uo, 'Ultimate Oscillator')",
        "plot_parity(alm_uo, ta_uo, bt_uo, 'Ultimate Oscillator Parity')"
    ))

    # 22. Connors RSI
    cells.append(make_md("### 22. Connors RSI", "Chỉ số RSI của Connors."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [connors_rsi]')",
        "alm_con = alm.indicators.connors_rsi(close, rsi_period=3, streak_period=2, rank_period=100)",
        "print(f'Connors RSI output length: {len(alm_con)}')",
        "plot_parity(alm_con, None, None, 'Connors RSI')"
    ))

    # 23. AO (Awesome Oscillator)
    cells.append(make_md("### 23. AO (Awesome Oscillator)", "Chỉ báo Awesome Oscillator."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [ao]')",
        "print('Pandas-TA returns: 1 column [AO_5_34]')",
        "print('Backtrader returns: 1 line [ao]')",
        "alm_ao = alm.indicators.ao(high, low, fast=5, slow=34)",
        "ta_ao = ta.ao(df['h'], df['l'], fast=5, slow=34)",
        "bt_ao = bt_results['ao']",
        "print_parity_stats(alm_ao, ta_ao, bt_ao, 'Awesome Oscillator')",
        "plot_parity(alm_ao, ta_ao, bt_ao, 'Awesome Oscillator Parity')"
    ))

    # 24. Coppock Curve
    cells.append(make_md("### 24. Coppock Curve", "Đường cong Coppock."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [coppock]')",
        "print('Pandas-TA returns: 1 column [COPC_11_14_10]')",
        "print('Backtrader: No built-in Coppock Curve')",
        "alm_cop = alm.indicators.coppock(close, short=11, long=14, wma=10)",
        "ta_cop = ta.coppock(df['c'], fast=11, slow=14, length=10)",
        "print_parity_stats(alm_cop, ta_cop, None, 'Coppock Curve')",
        "plot_parity(alm_cop, ta_cop, None, 'Coppock Curve Parity')"
    ))
    
    write_notebook("momentum_indicators.ipynb", cells)

def generate_volume_notebook():
    cells = get_common_setup(
        "Nhóm 3: Chỉ báo Khối lượng (Volume Indicators)",
        "Notebook này đối soát các chỉ báo khối lượng/dòng tiền như VWAP, OBV, CMF."
    )
    
    # Run Backtrader specs for volume
    cells.append(make_code(
        "# Backtrader lacks built-in VWAP, OBV, and CMF by default. We will run comparisons on Pandas-TA.",
        "print('Backtrader does not contain built-in VWAP, OBV, or CMF. Comparing with Pandas-TA.')"
    ))
    
    # 1. VWAP
    cells.append(make_md("### 1. VWAP (Volume Weighted Average Price)", "Giá trung bình gia trọng khối lượng."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [vwap]')",
        "print('Pandas-TA returns: 1 column [VWAP_D]')",
        "alm_vwap = alm.indicators.vwap(high, low, close, volume)",
        "# Note: We prepare a dataframe with DatetimeIndex for pandas_ta.vwap",
        "df_vwap = df.copy()",
        "df_vwap['datetime'] = pd.to_datetime(df_vwap['t'], unit='ms')",
        "df_vwap.set_index('datetime', inplace=True)",
        "ta_vwap = ta.vwap(df_vwap['h'], df_vwap['l'], df_vwap['c'], df_vwap['v'])",
        "print_parity_stats(alm_vwap, ta_vwap.values, None, 'VWAP')",
        "plot_parity(alm_vwap, ta_vwap.values, None, 'VWAP Parity', is_price=True)"
    ))

    # 2. OBV
    cells.append(make_md("### 2. OBV (On Balance Volume)", "Khối lượng cân bằng."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [obv]')",
        "print('Pandas-TA returns: 1 column [OBV]')",
        "alm_obv = alm.indicators.obv(close, volume)",
        "ta_obv = ta.obv(df['c'], df['v'])",
        "print_parity_stats(alm_obv, ta_obv, None, 'OBV')",
        "plot_parity(alm_obv, ta_obv, None, 'OBV Parity')"
    ))

    # 3. CMF
    cells.append(make_md("### 3. CMF (Chaikin Money Flow)", "Dòng tiền Chaikin."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [cmf]')",
        "print('Pandas-TA returns: 1 column [CMF_20]')",
        "alm_cmf = alm.indicators.cmf(high, low, close, volume, period=20)",
        "ta_cmf = ta.cmf(df['h'], df['l'], df['c'], df['v'], length=20)",
        "print_parity_stats(alm_cmf, ta_cmf, None, 'CMF 20')",
        "plot_parity(alm_cmf, ta_cmf, None, 'CMF 20 Parity')"
    ))
    
    write_notebook("volume_indicators.ipynb", cells)

def generate_channel_notebook():
    cells = get_common_setup(
        "Nhóm 4: Các Kênh giá & Biến động (Channel & Volatility Indicators)",
        "Notebook này đối soát các kênh giá Bollinger Bands, Keltner Channels, Donchian Channels."
    )
    
    # Run Backtrader specs for channels
    cells.append(make_code(
        "# Chạy Backtrader specs cho nhóm Channels",
        "bt_specs = [",
        "    ('bb', bt.indicators.BollingerBands, ['close'], {'period': 20, 'devfactor': 2.0}),",
        "]",
        "bt_results = run_bt_indicators(df, bt_specs)",
        "print('Backtrader channel indicators calculated successfully.')"
    ))
    
    # 1. Bollinger Bands
    cells.append(make_md("### 1. Bollinger Bands", "Dải Bollinger. Lưu ý Almanac trả về thêm 2 mảng: `bandwidth` và `percent` mà Backtrader không có mặc định."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 5 outputs [mid, up, low, bandwidth, percent]')",
        "print('Pandas-TA returns: 5 columns [BBL_, BBM_, BBU_, BBB_, BBP_]')",
        "print('Backtrader returns: 3 lines [mid, top, bot]')",
        "alm_mid, alm_up, alm_low, alm_pct, alm_bw = alm.indicators.bollinger_bands(close, period=20, k=2.0)",
        "ta_bb = ta.bbands(df['c'], length=20, std=2.0, ddof=0, talib=False)",
        "bb_mid_col = [c for c in ta_bb.columns if c.startswith('BBM_')][0]",
        "bb_up_col = [c for c in ta_bb.columns if c.startswith('BBU_')][0]",
        "bb_low_col = [c for c in ta_bb.columns if c.startswith('BBL_')][0]",
        "bb_bw_col = [c for c in ta_bb.columns if c.startswith('BBB_')][0]",
        "bb_pct_col = [c for c in ta_bb.columns if c.startswith('BBP_')][0]",
        "ta_mid = ta_bb[bb_mid_col]",
        "ta_up = ta_bb[bb_up_col]",
        "ta_low = ta_bb[bb_low_col]",
        "ta_bw = ta_bb[bb_bw_col]",
        "ta_pct = ta_bb[bb_pct_col]",
        "bt_mid = bt_results['bb_mid']",
        "bt_up = bt_results['bb_top']",
        "bt_low = bt_results['bb_bot']",
        "alm_bw_scaled = [x * 100.0 if x is not None else None for x in alm_bw]",
        "print_parity_stats(alm_mid, ta_mid, bt_mid, 'BB Mid')",
        "print_parity_stats(alm_up, ta_up, bt_up, 'BB Upper')",
        "print_parity_stats(alm_low, ta_low, bt_low, 'BB Lower')",
        "print_parity_stats(alm_bw_scaled, ta_bw, None, 'BB Bandwidth')",
        "print_parity_stats(alm_pct, ta_pct, None, 'BB Percent')",
        "plot_parity({'Mid': alm_mid, 'Upper': alm_up, 'Lower': alm_low}, {'Mid': ta_mid, 'Upper': ta_up, 'Lower': ta_low}, {'Mid': bt_mid, 'Upper': bt_up, 'Lower': bt_low}, 'Bollinger Bands Parity', is_price=True)",
        "plot_parity({'Bandwidth': alm_bw_scaled, 'Percent': alm_pct}, {'Bandwidth': ta_bw, 'Percent': ta_pct}, None, 'Bollinger Bands Bandwidth & Percent Parity', is_price=False)"
    ))

    # 2. Keltner Channels
    cells.append(make_md("### 2. Keltner Channels", "Kênh Keltner."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [mid, up, low]')",
        "print('Pandas-TA returns: 3 columns starting with KCMA_, KCU_, KCL_')",
        "alm_kmid, alm_kup, alm_klow = alm.indicators.keltner(high, low, close, period=20, multiplier=2.0, atr_period=10)",
        "ta_kc = ta.kc(df['h'], df['l'], df['c'], length=20, scalar=2.0, atr_length=10)",
        "kmid_col = [c for c in ta_kc.columns if c.startswith('KCBe') or c.startswith('KCMA')][0]",
        "kup_col = [c for c in ta_kc.columns if c.startswith('KCU')][0]",
        "klow_col = [c for c in ta_kc.columns if c.startswith('KCL')][0]",
        "ta_kmid = ta_kc[kmid_col]",
        "ta_kup = ta_kc[kup_col]",
        "ta_klow = ta_kc[klow_col]",
        "ta_atr_kc = ta.atr(df['h'], df['l'], df['c'], length=10)",
        "ta_kup_exact = ta_kmid + 2.0 * ta_atr_kc",
        "ta_klow_exact = ta_kmid - 2.0 * ta_atr_kc",
        "print('--- Pandas-TA default (EMA of True Range) ---')",
        "print_parity_stats(alm_kmid, ta_kmid, None, 'Keltner Mid')",
        "print_parity_stats(alm_kup, ta_kup, None, 'Keltner Upper')",
        "print_parity_stats(alm_klow, ta_klow, None, 'Keltner Lower')",
        "print('--- Custom Pandas-TA (Wilder\\'s ATR - Matches Almanac exactly) ---')",
        "print_parity_stats(alm_kup, ta_kup_exact, None, 'Keltner Upper (Exact)')",
        "print_parity_stats(alm_klow, ta_klow_exact, None, 'Keltner Lower (Exact)')",
        "plot_parity({'Mid': alm_kmid, 'Upper': alm_kup, 'Lower': alm_klow}, {'Mid': ta_kmid, 'Upper': ta_kup_exact, 'Lower': ta_klow_exact}, None, 'Keltner Channels Parity', is_price=True)"
    ))

    # 3. Donchian Channels
    cells.append(make_md("### 3. Donchian Channels", "Kênh Donchian."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [up, low, mid]')",
        "print('Pandas-TA returns: 3 columns starting with DCU_, DCL_, DCM_')",
        "alm_dup, alm_dlow, alm_dmid = alm.indicators.donchian(high, low, close, period=20)",
        "ta_don = ta.donchian(df['h'], df['l'], lower_length=20, upper_length=20)",
        "dup_col = [c for c in ta_don.columns if c.startswith('DCU_')][0]",
        "dlow_col = [c for c in ta_don.columns if c.startswith('DCL_')][0]",
        "dmid_col = [c for c in ta_don.columns if c.startswith('DCM_')][0]",
        "ta_dup = ta_don[dup_col]",
        "ta_dlow = ta_don[dlow_col]",
        "ta_dmid = ta_don[dmid_col]",
        "print_parity_stats(alm_dmid, ta_dmid, None, 'Donchian Mid')",
        "print_parity_stats(alm_dup, ta_dup, None, 'Donchian Upper')",
        "print_parity_stats(alm_dlow, ta_dlow, None, 'Donchian Lower')",
        "plot_parity({'Upper': alm_dup, 'Lower': alm_dlow, 'Mid': alm_dmid}, {'Upper': ta_dup, 'Lower': ta_dlow, 'Mid': ta_dmid}, None, 'Donchian Channels Parity', is_price=True)"
    ))
    
    write_notebook("channel_indicators.ipynb", cells)

def generate_other_notebook():
    cells = get_common_setup(
        "Nhóm 5: Các chỉ báo khác (Pattern, Regime, Risk & Other Indicators)",
        "Notebook này đối soát các chỉ báo như RWI, Elder Ray, Fractals, Ichimoku, Heiken Ashi, Chop, ATR, Supertrend, Chandelier Exit..."
    )
    
    # Run Backtrader specs for other indicators
    cells.append(make_code(
        "# Chạy Backtrader specs cho nhóm Other/Pattern",
        "bt_specs = [",
        "    ('ichimoku', bt.indicators.Ichimoku, ['data'], {'tenkan': 9, 'kijun': 26, 'senkou': 52}),", # senkou = senkou_b in BT
        "    ('ha', bt.indicators.HeikinAshi, ['data'], {}),",
        "    ('atr14', bt.indicators.ATR, ['data'], {'period': 14}),",
        "    ('psar', bt.indicators.PSAR, ['data'], {'af': 0.02, 'afmax': 0.2}),",
        "]",
        "bt_results = run_bt_indicators(df, bt_specs)",
        "print('Backtrader other indicators calculated successfully.')"
    ))
    
    # 1. RWI
    cells.append(make_md("### 1. RWI (Random Walk Index)", "Chỉ số bước đi ngẫu nhiên."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [rwi_high, rwi_low]')",
        "print('Backtrader returns: 2 lines [rwi_high, rwi_low]')",
        "alm_rwih, alm_rwil = alm.indicators.rwi(high, low, close, period=14)",
        "bt_rwih = None",
        "bt_rwil = None",
        "print_parity_stats(alm_rwih, None, bt_rwih, 'RWI High')",
        "print_parity_stats(alm_rwil, None, bt_rwil, 'RWI Low')",
        "plot_parity({'RWI High': alm_rwih, 'RWI Low': alm_rwil}, None, None, 'Random Walk Index Parity')"
    ))

    # 2. Elder Ray
    cells.append(make_md("### 2. Elder Ray", "Chỉ số Elder Ray."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [bull_power, bear_power, ema]')",
        "print('Backtrader returns: 2 lines [bullpower, bearpower]')",
        "alm_ebull, alm_ebear, alm_eema = alm.indicators.elder_ray(high, low, close, period=13)",
        "bt_ebull = None",
        "bt_ebear = None",
        "print_parity_stats(alm_ebull, None, bt_ebull, 'Elder Bull Power')",
        "print_parity_stats(alm_ebear, None, bt_ebear, 'Elder Bear Power')",
        "plot_parity({'Bull Power': alm_ebull, 'Bear Power': alm_ebear}, None, None, 'Elder Ray Power Parity')"
    ))

    # 3. Williams Fractal
    cells.append(make_md("### 3. Williams Fractal", "Chỉ báo Fractal Williams tìm đỉnh/đáy cục bộ."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 4 outputs [bullish, bearish, fractal_high, fractal_low]')",
        "alm_fbull, alm_fbear, alm_fhigh, alm_flow = alm.indicators.fractal(high, low)",
        "ta_bear = ((df['h'].shift(2) > df['h'].shift(4)) & (df['h'].shift(2) > df['h'].shift(3)) & (df['h'].shift(2) > df['h'].shift(1)) & (df['h'].shift(2) > df['h'])).astype(float)",
        "ta_bull = ((df['l'].shift(2) < df['l'].shift(4)) & (df['l'].shift(2) < df['l'].shift(3)) & (df['l'].shift(2) < df['l'].shift(1)) & (df['l'].shift(2) < df['l'])).astype(float)",
        "ta_fhigh = df['h'].shift(2)",
        "ta_flow = df['l'].shift(2)",
        "print_parity_stats(alm_fbull, ta_bull, None, 'Fractal Bullish Signal')",
        "print_parity_stats(alm_fbear, ta_bear, None, 'Fractal Bearish Signal')",
        "print_parity_stats(alm_fhigh, ta_fhigh, None, 'Fractal High Price')",
        "print_parity_stats(alm_flow, ta_flow, None, 'Fractal Low Price')",
        "plot_parity({'High': alm_fhigh, 'Low': alm_flow}, {'High': ta_fhigh, 'Low': ta_flow}, None, 'Williams Fractal Parity', is_price=True)"
    ))

    # 4. Ichimoku
    cells.append(make_md("### 4. Ichimoku Kinko Hyo", "Hệ thống chỉ báo đám mây Ichimoku."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 5 outputs [tenkan, kijun, senkou_a, senkou_b, chikou]')",
        "print('Pandas-TA returns: 2 DataFrames (lines & span) total 5 columns [ITS_9, IKS_26, ISA_9, ISB_26, ICS_26]')",
        "print('Backtrader returns: 5 lines [tenkan_sen, kijun_sen, senkou_span_a, senkou_span_b, chikou_span]')",
        "alm_ten, alm_kij, alm_sa, alm_sb, alm_ch = alm.indicators.ichimoku(high, low, close, tenkan=9, kijun=26, senkou_b=52)",
        "ta_ich, ta_span = ta.ichimoku(df['h'], df['l'], df['c'], tenkan=9, kijun=26, senkou=52)",
        "ta_ten = ta_ich['ITS_9']",
        "ta_kij = ta_ich['IKS_26']",
        "ta_sa = ta_ich['ISA_9'].shift(-25)",
        "ta_sb = ta_ich['ISB_26'].shift(-25)",
        "ta_ch = ta_ich['ICS_26'].shift(25)",
        "bt_ten = bt_results['ichimoku_tenkan_sen']",
        "bt_kij = bt_results['ichimoku_kijun_sen']",
        "bt_sa = pd.Series(bt_results['ichimoku_senkou_span_a']).shift(-26)",
        "bt_sb = pd.Series(bt_results['ichimoku_senkou_span_b']).shift(-26)",
        "bt_ch = pd.Series(bt_results['ichimoku_chikou_span']).shift(26)",
        "print_parity_stats(alm_ten, ta_ten, bt_ten, 'Ichimoku Tenkan')",
        "print_parity_stats(alm_kij, ta_kij, bt_kij, 'Ichimoku Kijun')",
        "print_parity_stats(alm_sa, ta_sa, bt_sa, 'Ichimoku Senkou A (Shifted)')",
        "print_parity_stats(alm_sb, ta_sb, bt_sb, 'Ichimoku Senkou B (Shifted)')",
        "print_parity_stats(alm_ch, ta_ch, bt_ch, 'Ichimoku Chikou (Shifted)')",
        "plot_parity({'Tenkan': alm_ten, 'Kijun': alm_kij, 'Senkou A': alm_sa, 'Senkou B': alm_sb, 'Chikou': alm_ch}, {'Tenkan': ta_ten, 'Kijun': ta_kij, 'Senkou A': ta_sa, 'Senkou B': ta_sb, 'Chikou': ta_ch}, {'Tenkan': bt_ten, 'Kijun': bt_kij, 'Senkou A': bt_sa, 'Senkou B': bt_sb, 'Chikou': bt_ch}, 'Ichimoku Cloud Parity', is_price=True)"
    ))

    # 5. Heiken Ashi
    cells.append(make_md("### 5. Heiken Ashi", "Nến Heiken Ashi làm mượt xu hướng."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 4 outputs [open, high, low, close]')",
        "print('Pandas-TA returns: 4 columns [HA_open, HA_high, HA_low, HA_close]')",
        "print('Backtrader returns: 4 lines [ha_open, ha_high, ha_low, ha_close]')",
        "alm_hao, alm_hah, alm_hal, alm_hac = alm.indicators.heiken_ashi(open_, high, low, close, smooth=1)",
        "ta_ha = ta.ha(df['o'], df['h'], df['l'], df['c'])",
        "bt_hao = bt_results['ha_ha_open']",
        "bt_hah = bt_results['ha_ha_high']",
        "bt_hal = bt_results['ha_ha_low']",
        "bt_hac = bt_results['ha_ha_close']",
        "print_parity_stats(alm_hao, ta_ha['HA_open'], bt_hao, 'Heiken Ashi Open')",
        "print_parity_stats(alm_hah, ta_ha['HA_high'], bt_hah, 'Heiken Ashi High')",
        "print_parity_stats(alm_hal, ta_ha['HA_low'], bt_hal, 'Heiken Ashi Low')",
        "print_parity_stats(alm_hac, ta_ha['HA_close'], bt_hac, 'Heiken Ashi Close')",
        "plot_parity({'Open': alm_hao, 'High': alm_hah, 'Low': alm_hal, 'Close': alm_hac}, {'Open': ta_ha['HA_open'], 'High': ta_ha['HA_high'], 'Low': ta_ha['HA_low'], 'Close': ta_ha['HA_close']}, {'Open': bt_hao, 'High': bt_hah, 'Low': bt_hal, 'Close': bt_hac}, 'Heiken Ashi Parity', is_price=True)"
    ))

    # 6. Chop (Choppiness Index)
    cells.append(make_md("### 6. Choppiness Index", "Chỉ số biến động ngang."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [chop]')",
        "print('Pandas-TA returns: 1 column [CHOP_14]')",
        "alm_chop = alm.indicators.chop(high, low, close, period=14)",
        "ta_chop = ta.chop(df['h'], df['l'], df['c'], length=14)",
        "print_parity_stats(alm_chop, ta_chop, None, 'Choppiness Index')",
        "plot_parity(alm_chop, ta_chop, None, 'Choppiness Index Parity')"
    ))

    # 7. Chop Zone
    cells.append(make_md("### 7. Chop Zone", "Vùng biến động ngang."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [angle, zone]')",
        "alm_cz_ang, alm_cz_zon = alm.indicators.chop_zone(high, low, close, ema_period=34, threshold=5.0)",
        "ema_cz = ta.ema(df['c'], length=34)",
        "tr_cz = ta.true_range(df['h'], df['l'], df['c'])",
        "ema_change = ema_cz.diff()",
        "ta_cz_ang = np.arctan(ema_change / tr_cz) * (180.0 / np.pi)",
        "ta_cz_ang = ta_cz_ang.fillna(0.0)",
        "ta_cz_zon = pd.Series(0.0, index=df.index)",
        "ta_cz_zon[ta_cz_ang > 5.0] = 1.0",
        "ta_cz_zon[ta_cz_ang < -5.0] = -1.0",
        "print_parity_stats(alm_cz_ang, ta_cz_ang, None, 'Chop Zone Angle')",
        "print_parity_stats(alm_cz_zon, ta_cz_zon, None, 'Chop Zone Zone')",
        "plot_parity({'Angle': alm_cz_ang, 'Zone': alm_cz_zon}, {'Angle': ta_cz_ang, 'Zone': ta_cz_zon}, None, 'Chop Zone Parity')"
    ))

    # 8. Volatility Ratio
    cells.append(make_md("### 8. Volatility Ratio", "Tỷ số biến động giá."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [volatility_ratio]')",
        "alm_vr = alm.indicators.volatility_ratio(high, low, close, period=14)",
        "print(f'Volatility ratio output length: {len(alm_vr)}')",
        "plot_parity(alm_vr, None, None, 'Volatility Ratio')"
    ))

    # 9. ATR (Average True Range)
    cells.append(make_md("### 9. ATR (Average True Range)", "Khoảng dao động thực tế trung bình."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 1 output [atr]')",
        "print('Pandas-TA returns: 1 column [ATRr_14]')",
        "print('Backtrader returns: 1 line [atr]')",
        "alm_atr = alm.indicators.atr(high, low, close, period=14)",
        "ta_atr = ta.atr(df['h'], df['l'], df['c'], length=14)",
        "bt_atr = bt_results['atr14']",
        "print_parity_stats(alm_atr, ta_atr, bt_atr, 'ATR 14')",
        "plot_parity(alm_atr, ta_atr, bt_atr, 'ATR 14 Parity')"
    ))

    # 10. Supertrend
    cells.append(make_md("### 10. Supertrend", "Đường xu hướng Supertrend."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [value, bullish, bearish]')",
        "print('Pandas-TA returns: 4 columns [SUPERT_10_3.0, SUPERTd_10_3.0, SUPERTl_10_3.0, SUPERTs_10_3.0]')",
        "alm_stval, alm_stbull, alm_stbear = alm.indicators.supertrend(high, low, close, period=10, multiplier=3.0)",
        "ta_st = ta.supertrend(df['h'], df['l'], df['c'], length=10, multiplier=3.0)",
        "st_val_col = [c for c in ta_st.columns if c.startswith('SUPERT_')][0]",
        "st_dir_col = [c for c in ta_st.columns if c.startswith('SUPERTd_')][0]",
        "ta_stval = ta_st[st_val_col]",
        "ta_stbull = (ta_st[st_dir_col] + 1) / 2.0",
        "ta_stbear = 1.0 - ta_stbull",
        "print_parity_stats(alm_stval, ta_stval, None, 'Supertrend Value')",
        "print_parity_stats(alm_stbull, ta_stbull, None, 'Supertrend Bullish Direction')",
        "print_parity_stats(alm_stbear, ta_stbear, None, 'Supertrend Bearish Direction')",
        "plot_parity({'Value': alm_stval}, {'Value': ta_stval}, None, 'Supertrend Value Parity', is_price=True)",
        "plot_parity({'Bullish': alm_stbull, 'Bearish': alm_stbear}, {'Bullish': ta_stbull, 'Bearish': ta_stbear}, None, 'Supertrend Direction Parity', is_price=False)"
    ))

    # 11. Chandelier Exit
    cells.append(make_md("### 11. Chandelier Exit / Chandelier Stop", "Điểm dừng lỗ theo ATR Chandelier."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 3 outputs [long_stop, short_stop, atr]')",
        "alm_clong, alm_cshort, alm_catr = alm.indicators.chandelier(high, low, close, period=22, multiplier=3.0)",
        "ta_catr = ta.atr(df['h'], df['l'], df['c'], length=22)",
        "ta_clong = df['h'].rolling(22).max() - 3.0 * ta_catr",
        "ta_cshort = df['l'].rolling(22).min() + 3.0 * ta_catr",
        "print_parity_stats(alm_clong, ta_clong, None, 'Chandelier Long Stop')",
        "print_parity_stats(alm_cshort, ta_cshort, None, 'Chandelier Short Stop')",
        "print_parity_stats(alm_catr, ta_catr, None, 'Chandelier ATR')",
        "plot_parity({'Long Stop': alm_clong, 'Short Stop': alm_cshort}, {'Long Stop': ta_clong, 'Short Stop': ta_cshort}, None, 'Chandelier Exit Stops Parity', is_price=True)"
    ))

    # 12. Chande Kroll Stop
    cells.append(make_md("### 12. Chande Kroll Stop", "Điểm dừng lỗ Chande Kroll."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [stop_long, stop_short]')",
        "alm_klong, alm_kshort = alm.indicators.chande_kroll(high, low, close, atr_period=10, factor=1.5, stop_period=9)",
        "ta_atr_ck = ta.atr(df['h'], df['l'], df['c'], length=10)",
        "first_long_ck = df['h'].rolling(10).max() - 1.5 * ta_atr_ck",
        "first_short_ck = df['l'].rolling(10).min() + 1.5 * ta_atr_ck",
        "ta_klong = first_long_ck.rolling(9).max()",
        "ta_kshort = first_short_ck.rolling(9).min()",
        "print_parity_stats(alm_klong, ta_klong, None, 'Chande Kroll Long Stop')",
        "print_parity_stats(alm_kshort, ta_kshort, None, 'Chande Kroll Short Stop')",
        "plot_parity({'Long Stop': alm_klong, 'Short Stop': alm_kshort}, {'Long Stop': ta_klong, 'Short Stop': ta_kshort}, None, 'Chande Kroll Stops Parity', is_price=True)"
    ))

    # 13. Parabolic SAR
    cells.append(make_md("### 13. Parabolic SAR", "Chỉ báo dừng và đảo chiều Parabolic SAR."))
    cells.append(make_code(
        "print('=== [Structural Parity] ===')",
        "print('Almanac returns: 2 outputs [sar, bullish]')",
        "print('Pandas-TA returns: 2 columns [PSARl_0.02_0.2, PSARs_0.02_0.2]')",
        "print('Backtrader returns: 1 line [psar]')",
        "alm_sar, alm_sbull = alm.indicators.parabolic_sar(high, low, close, step=0.02, max=0.2)",
        "ta_psar = ta.psar(df['h'], df['l'], df['c'], step=0.02, max=0.2)",
        "ta_psar_long = ta_psar[[c for c in ta_psar.columns if c.startswith('PSARl_')][0]]",
        "ta_psar_short = ta_psar[[c for c in ta_psar.columns if c.startswith('PSARs_')][0]]",
        "ta_psar_merged = ta_psar_long.fillna(ta_psar_short)",
        "ta_psarbull = (~ta_psar_long.isna()).astype(float)",
        "bt_sar = bt_results['psar']",
        "print_parity_stats(alm_sar, ta_psar_merged, bt_sar, 'Parabolic SAR Value')",
        "print_parity_stats(alm_sbull, ta_psarbull, None, 'Parabolic SAR Direction')",
        "plot_parity({'SAR': alm_sar}, {'SAR': ta_psar_merged}, {'SAR': bt_sar}, 'Parabolic SAR Value Parity', is_price=True)",
        "plot_parity({'Bullish': alm_sbull}, {'Bullish': ta_psarbull}, None, 'Parabolic SAR Direction Parity', is_price=False)"
    ))
    
    write_notebook("other_indicators.ipynb", cells)

if __name__ == "__main__":
    generate_trend_notebook()
    generate_momentum_notebook()
    generate_volume_notebook()
    generate_channel_notebook()
    generate_other_notebook()
