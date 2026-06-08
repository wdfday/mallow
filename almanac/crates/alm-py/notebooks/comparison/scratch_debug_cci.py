import sys
import pathlib
import json

curr_dir = pathlib.Path(__file__).resolve().parent
sys.path.append(str(curr_dir))

from _shared import load_parquet
import alm_py as alm
import pandas as pd
import numpy as np
import backtrader as bt

# Load real BTCUSDT M1 testdata
alm_bars, bt_df = load_parquet("BTCUSDT", "M1", "testdata", n=20000)

# 1. Run Almanac script backtest
script = """
let cci20 = ind.cci(20);
"""
res_alm = alm.run_script_backtest("BTCUSDT", script, alm_bars)
alm_cci_t = res_alm["indicator_series"]["cci20"]["t"]
alm_cci_v = res_alm["indicator_series"]["cci20"]["v"]

alm_df = pd.DataFrame({
    "alm_cci": alm_cci_v
}, index=pd.to_datetime(alm_cci_t, unit="ms", utc=True))

# 2. Run Backtrader with custom StandardCCI
class StandardCCI(bt.Indicator):
    lines = ('cci',)
    params = (('period', 20), ('factor', 0.015),)
    def __init__(self):
        self.tp = (self.data.high + self.data.low + self.data.close) / 3.0
        self.sma = bt.indicators.SMA(self.tp, period=self.p.period)
    def next(self):
        # We need at least self.p.period bars of tp
        tp_window = [self.tp[-i] for i in range(self.p.period)]
        sma_curr = self.sma[0]
        mean_dev = sum(abs(x - sma_curr) for x in tp_window) / self.p.period
        if mean_dev > 1e-10:
            self.lines.cci[0] = (self.tp[0] - sma_curr) / (self.p.factor * mean_dev)
        else:
            self.lines.cci[0] = 0.0

class BtDebugStrat(bt.Strategy):
    def __init__(self):
        self.cci = StandardCCI(self.data, period=20)
        self.records = []
    def next(self):
        dt = self.data.datetime.datetime(0)
        dt_utc = pd.Timestamp(dt).tz_localize("UTC")
        self.records.append({
            "datetime": dt_utc,
            "bt_cci": self.cci[0]
        })

cerebro = bt.Cerebro()
data = bt.feeds.PandasData(dataname=bt_df, timeframe=bt.TimeFrame.Minutes, compression=1)
cerebro.adddata(data)
cerebro.addstrategy(BtDebugStrat)
res_bt = cerebro.run()
bt_records = res_bt[0].records
bt_rec_df = pd.DataFrame(bt_records).set_index("datetime")

# Merge
merged = alm_df.join(bt_rec_df, how="inner")

print("Compare Almanac's CCI with StandardCCI in Backtrader:")
print(merged.dropna().head(20))
