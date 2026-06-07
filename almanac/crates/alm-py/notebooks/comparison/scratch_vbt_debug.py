import numpy as np
import pandas as pd
import pandas_ta as ta
import vectorbt as vbt
from _shared import load_parquet, vbt_run

try:
    ALM_BARS, DF = load_parquet('BTCUSDT', 'H1', 'BinanceFlat', n=2000)
    print("Loaded data successfully.")
except FileNotFoundError:
    from _shared import make_bars
    ALM_BARS, DF = make_bars(n=2000, trend=0.0008, noise=0.006, seed=42)
    print("BinanceFlat parquet not found — using synthetic data")

close = DF['close']
C = close.values

e1 = ta.ema(close, length=10)
e2 = ta.ema(close, length=20)
e3 = ta.ema(close, length=50)

def cross_above(a, b):
    idx = getattr(a, 'index', getattr(b, 'index', None))
    a_val = a.values if hasattr(a, 'values') else a
    b_val = b.values if hasattr(b, 'values') else b
    a_series = pd.Series(a_val, dtype=float)
    b_series = pd.Series(b_val, dtype=float)
    res = ((a_series.shift(1) <= b_series.shift(1)) & (a_series > b_series)).fillna(False)
    if idx is not None:
        res.index = idx
    return res

def cross_below(a, b):
    idx = getattr(a, 'index', getattr(b, 'index', None))
    a_val = a.values if hasattr(a, 'values') else a
    b_val = b.values if hasattr(b, 'values') else b
    a_series = pd.Series(a_val, dtype=float)
    b_series = pd.Series(b_val, dtype=float)
    res = ((a_series.shift(1) >= b_series.shift(1)) & (a_series < b_series)).fillna(False)
    if idx is not None:
        res.index = idx
    return res

entries = cross_above(e1, e2) & (e2 > e3)
exits   = cross_below(e1, e2)

print("DF close length:", len(DF))
print("C shape:", C.shape)
print("entries shape:", entries.shape)
print("exits shape:", exits.shape)
print("entries type:", type(entries))
print("entries index length:", len(entries.index) if hasattr(entries, 'index') else "no index")

try:
    vbt_run(entries, exits, C)
    print("vbt_run succeeded")
except Exception as e:
    import traceback
    traceback.print_exc()
