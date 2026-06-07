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
        print("Không thể load BinanceFlat, thử Binance:", e)
        alm_bars, df = load_parquet('BTCUSDT', 'H1', 'Binance', n=2000)
        
    # Chạy alm_py
    alm_result = alm_py.run_backtest(
        'BTCUSDT', 'williams_r_ma', 
        {"wr_period": 14, "ema_period": 50, "oversold": -80.0, "overbought": -20.0}, 
        alm_bars,
        initial_capital=10000.0, commission_pct=0.001, slippage_pct=0.0005, lot_size=0.0, strength_sizing=False
    )
    print(f"alm_py trades: {len(alm_result['trades'])}")
    
    # Chạy backtrader
    class TestStrat(bt.Strategy):
        def __init__(self):
            self.wr = bt.indicators.WilliamsR(self.data, period=14)
            self.ema = bt.indicators.EMA(self.data.close, period=50)
        def next(self):
            cross_up = self.wr[-1] <= -80.0 and self.wr[0] > -80.0
            cross_dn = self.wr[-1] >= -20.0 and self.wr[0] < -20.0
            
            if not self.position and cross_up and self.data.close[0] > self.ema[0]:
                self.buy()
            elif self.position and (cross_dn or self.data.close[0] < self.ema[0]):
                self.close()

    bt_result = bt_run(TestStrat, df, capital=10000.0, commission=0.001, slippage=0.0005)
    print(f"backtrader trades: {bt_result['total_trades']}")

if __name__ == '__main__':
    main()
