import sys
import os
import pandas as pd
import numpy as np
import backtrader as bt
import alm_py

sys.path.append(os.path.dirname(__file__))
from _shared import load_parquet, bt_run

def main():
    try:
        alm_bars, df = load_parquet('BTCUSDT', 'H1', 'BinanceFlat', n=2000)
    except Exception as e:
        alm_bars, df = load_parquet('BTCUSDT', 'H1', 'Binance', n=2000)
        
    # Chạy alm_py
    alm_result = alm_py.run_backtest(
        'BTCUSDT', 'keltner_breakout', 
        {"period": 20, "atr_period": 10, "multiplier": 2.0}, 
        alm_bars,
        initial_capital=10000.0, commission_pct=0.001, slippage_pct=0.0005, lot_size=0.0, strength_sizing=False
    )
    print("=== ALM_PY TRADES ===")
    ts_list = alm_bars['t']
    for t in alm_result['trades']:
        entry_idx = ts_list.index(t['entry_ts'])
        exit_idx = ts_list.index(t['exit_ts'])
        print(f"Entry idx: {entry_idx}, Exit idx: {exit_idx}, PnL: {t['pnl']:.2f}")
    
    # Chạy backtrader
    class TestStrat(bt.Strategy):
        def __init__(self):
            self.ema = bt.indicators.EMA(self.data.close, period=20)
            self.atr = bt.indicators.ATR(self.data, period=10)
        def next(self):
            kc_upper = self.ema[0] + 2.0 * self.atr[0]
            # Print first few valid lines to compare values
            if len(self) == 30:
                print(f"DEBUG BT index 29 (bar 30): close={self.data.close[0]:.2f}, ema={self.ema[0]:.2f}, atr={self.atr[0]:.2f}, upper={kc_upper:.2f}")
            if not self.position and self.data.close[0] > kc_upper:
                self.buy()
            elif self.position and self.data.close[0] < self.ema[0]:
                self.close()

    bt_result = bt_run(TestStrat, df, capital=10000.0, commission=0.001, slippage=0.0005)
    print("\n=== BACKTRADER TRADES ===")
    for t in bt_result['trades']:
        entry_idx = df.index.get_indexer([t['entry_dt']])[0]
        exit_idx = df.index.get_indexer([t['exit_dt']])[0]
        print(f"Entry idx: {entry_idx}, Exit idx: {exit_idx}, PnL: {t['pnl']:.2f}")

    # Chạy thêm một bản in từ alm_py indicators để đối chứng
    print("\n=== ALM_PY INDICATOR VALUES ===")
    # we can extract from indicator_series of alm_result if available
    # let's see what keys are in indicator_series
    if 'indicator_series' in alm_result and alm_result['indicator_series']:
        print(alm_result['indicator_series'].keys())
        # Let's print index 29 values
        # The structure is usually {"keltner_breakout_keltner_upper": [...], "keltner_breakout_keltner_middle": [...]}
        keys = list(alm_result['indicator_series'].keys())
        print("Available keys in indicator_series:", keys)
        upper_key = [k for k in keys if 'upper' in k][0]
        middle_key = [k for k in keys if 'middle' in k][0]
        print(f"DEBUG ALM index 29: upper={alm_result['indicator_series'][upper_key][29]:.2f}, middle={alm_result['indicator_series'][middle_key][29]:.2f}")

if __name__ == '__main__':
    main()
