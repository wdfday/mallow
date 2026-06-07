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
        'BTCUSDT', 'ao', 
        {"fast": 5, "slow": 34}, 
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
            hl2 = (self.data.high + self.data.low) / 2.0
            self.fast_sma = bt.indicators.SMA(hl2, period=5)
            self.slow_sma = bt.indicators.SMA(hl2, period=34)
            self.ao = self.fast_sma(-(34 - 5)) - self.slow_sma
        def next(self):
            if not self.position and self.ao[-1] <= 0 and self.ao[0] > 0:
                self.buy()
            elif self.position and self.ao[-1] >= 0 and self.ao[0] < 0:
                self.close()

    bt_result = bt_run(TestStrat, df, capital=10000.0, commission=0.001, slippage=0.0005)
    print("\n=== BACKTRADER TRADES ===")
    for t in bt_result['trades']:
        entry_idx = df.index.get_indexer([t['entry_dt']])[0]
        exit_idx = df.index.get_indexer([t['exit_dt']])[0]
        print(f"Entry idx: {entry_idx}, Exit idx: {exit_idx}, PnL: {t['pnl']:.2f}")

if __name__ == '__main__':
    main()
