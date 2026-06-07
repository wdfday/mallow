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
        'BTCUSDT', 'alligator', 
        {"jaw": 13, "teeth": 8, "lips": 5}, 
        alm_bars,
        initial_capital=10000.0, commission_pct=0.001, slippage_pct=0.0005, lot_size=0.0, strength_sizing=False
    )
    print(f"alm_py trades: {len(alm_result['trades'])}")
    
    # Chạy backtrader
    class AlligatorInd(bt.Indicator):
        lines = ('jaw', 'teeth', 'lips',)
        params = (('jaw', 13), ('teeth', 8), ('lips', 5),)
        def __init__(self):
            hl2 = (self.data.high + self.data.low) / 2.0
            self.lines.jaw   = bt.indicators.WeightedMovingAverage(hl2, period=self.p.jaw)
            self.lines.teeth = bt.indicators.WeightedMovingAverage(hl2, period=self.p.teeth)
            self.lines.lips  = bt.indicators.WeightedMovingAverage(hl2, period=self.p.lips)

    class TestStrat(bt.Strategy):
        def __init__(self):
            self.alg = AlligatorInd(self.data)
        def next(self):
            # Warmup theo logic cascade của Rust: jaw + teeth + lips - 2 = 24
            if len(self) < 25:
                return
            bull_now  = self.alg.lips[0]  > self.alg.teeth[0]  > self.alg.jaw[0]
            bull_prev = self.alg.lips[-1] > self.alg.teeth[-1] > self.alg.jaw[-1]
            if not self.position and not bull_prev and bull_now:
                self.buy()
            elif self.position and bull_prev and not bull_now:
                self.close()

    bt_result = bt_run(TestStrat, df, capital=10000.0, commission=0.001, slippage=0.0005)
    print(f"backtrader trades: {bt_result['total_trades']}")

if __name__ == '__main__':
    main()
