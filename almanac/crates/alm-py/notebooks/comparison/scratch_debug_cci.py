import numpy as np
import pandas as pd
import pandas_ta as ta
import alm_py
import backtrader as bt
from _shared import load_parquet, make_bars

# CCI custom indicator as defined in comparison
class CCIInd(bt.Indicator):
    lines = ('cci',)
    params = (('period', 20),)
    def __init__(self):
        self.tp = (self.data.high + self.data.low + self.data.close) / 3.0
        self.sma = bt.indicators.SMA(self.tp, period=self.p.period)
    def next(self):
        tp_sum = 0.0
        for i in range(self.p.period):
            tp_sum += abs(self.tp[-i] - self.sma[0])
        mean_dev = tp_sum / self.p.period
        if mean_dev == 0:
            self.lines.cci[0] = 0.0
        else:
            self.lines.cci[0] = (self.tp[0] - self.sma[0]) / (0.015 * mean_dev)

class BtStrat(bt.Strategy):
    def __init__(self):
        self.cci = CCIInd(self.data, period=20)
        self.trades = []
        self.entry_ts = None
        self.in_position = False
    def next(self):
        if len(self) < 21:
            return
        cross_up = self.cci[-1] <= -100.0 and self.cci[0] > -100.0
        cross_dn = self.cci[-1] <= 100.0 and self.cci[0] > 100.0
        
        if not self.in_position and cross_up:
            self.in_position = True
            self.entry_ts = self.data.datetime.datetime(0)
        elif self.in_position and cross_dn:
            self.in_position = False
            exit_ts = self.data.datetime.datetime(0)
            self.trades.append((self.entry_ts, exit_ts))

def debug_cci():
    bars, df = load_parquet('BTCUSDT', 'H1', 'BinanceFlat', n=1000)
    
    # Run alm_py
    params = {"period": 20, "entry_level": -100.0, "exit_level": 100.0}
    alm_res = alm_py.run_backtest(
        "BTCUSDT", "cci_reversal", params, bars,
        initial_capital=10000.0, commission_pct=0.001, slippage_pct=0.0005,
        lot_size=0.0, strength_sizing=False
    )
    
    # Run Backtrader
    cerebro = bt.Cerebro()
    class ParquetData(bt.feeds.PandasData):
        params = (
            ('datetime', None),
            ('open', 'open'),
            ('high', 'high'),
            ('low', 'low'),
            ('close', 'close'),
            ('volume', 'volume'),
            ('openinterest', None),
        )
    df_bt = df.copy()
    if 'timestamp' not in df_bt.columns:
        df_bt.index = pd.to_datetime(df_bt.index)
    else:
        df_bt.index = pd.to_datetime(df_bt['timestamp'], unit='ms')
    data = ParquetData(dataname=df_bt)
    cerebro.adddata(data)
    cerebro.addstrategy(BtStrat)
    strat = cerebro.run()[0]
    
    # Get trades from alm_py
    alm_trades = alm_res.get("trades", [])
    print(f"Alm Trades count: {len(alm_trades)}")
    print(f"Backtrader Trades count: {len(strat.trades)}")
    
    print("\n--- TRADE COMPARISON (ALM vs BT) ---")
    for i in range(max(len(alm_trades), len(strat.trades))):
        alm_str = "N/A"
        bt_str = "N/A"
        if i < len(alm_trades):
            t = alm_trades[i]
            entry_dt = pd.to_datetime(t["entry_ts"], unit="ms", utc=True)
            exit_dt = pd.to_datetime(t["exit_ts"], unit="ms", utc=True)
            # convert to local timezone or naive to match bt
            entry_naive = entry_dt.tz_convert(None)
            exit_naive = exit_dt.tz_convert(None)
            alm_str = f"ALM: Enter={entry_naive}, Exit={exit_naive}"
        if i < len(strat.trades):
            t = strat.trades[i]
            bt_str = f"BT:  Enter={t[0]}, Exit={t[1]}"
        print(f"Trade {i}: {alm_str} | {bt_str}")

if __name__ == "__main__":
    debug_cci()
