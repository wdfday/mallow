import numpy as np
import pandas as pd
import vectorbt as vbt
from _shared import load_parquet, make_bars, vbt_run

try:
    ALM_BARS, DF = load_parquet('BTCUSDT', 'H1', 'BinanceFlat', n=2000)
except FileNotFoundError:
    ALM_BARS, DF = make_bars(n=2000, trend=0.0008, noise=0.006, seed=42)

close = DF['close']
C = close.values

# Generate mock boolean entries/exits of length 2000
entries = np.zeros(2000, dtype=bool)
exits = np.zeros(2000, dtype=bool)
entries[100] = True
exits[150] = True

print(f"close shape: {C.shape}, dtype: {C.dtype}")
print(f"entries shape: {entries.shape}, dtype: {entries.dtype}")
print(f"exits shape: {exits.shape}, dtype: {exits.dtype}")

try:
    vbt_result = vbt_run(entries, exits, C, capital=10000.0, commission=0.001, slippage=0.0005, freq='1h')
    print("Success! Return:", vbt_result['total_return_pct'])
except Exception as e:
    import traceback
    traceback.print_exc()
