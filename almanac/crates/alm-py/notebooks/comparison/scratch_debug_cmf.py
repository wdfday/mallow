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
        'BTCUSDT', 'cmf_ema_trend', 
        {"cmf_period": 20, "ema_period": 50, "bull_threshold": 0.1, "bear_threshold": 0.1}, 
        alm_bars,
        initial_capital=10000.0, commission_pct=0.001, slippage_pct=0.0005, lot_size=0.0, strength_sizing=False
    )
    print(f"alm_py trades: {len(alm_result['trades'])}")
    
    # Chạy backtrader
    # Copy implementation of CMFInd from _generate_cmp.py
    class CMFInd(bt.Indicator):
        lines = ('cmf',)
        params = (('period', 20),)
        def __init__(self):
            mfv = ((self.data.close - self.data.low) - (self.data.high - self.data.close)) / (self.data.high - self.data.low + 1e-10) * self.data.volume
            self._mfv_sum = bt.indicators.SumN(mfv, period=self.p.period)
            self._vol_sum = bt.indicators.SumN(self.data.volume, period=self.p.period)
        def next(self):
            self.lines.cmf[0] = self._mfv_sum[0] / self._vol_sum[0] if self._vol_sum[0] != 0 else 0.0

    class TestStrat(bt.Strategy):
        def __init__(self):
            self.cmf = CMFInd(self.data, period=20)
            self.ema = bt.indicators.EMA(self.data.close, period=50)
        def next(self):
            if not self.position and self.cmf.lines.cmf[0] > 0.1 and self.data.close[0] > self.ema[0]:
                self.buy()
            elif self.position and (self.cmf.lines.cmf[0] < -0.1 or self.data.close[0] < self.ema[0]):
                self.close()

    bt_result = bt_run(TestStrat, df, capital=10000.0, commission=0.001, slippage=0.0005)
    print(f"backtrader trades: {bt_result['total_trades']}")

if __name__ == '__main__':
    main()
