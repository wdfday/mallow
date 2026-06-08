import pytest
import alm_py as alm

def test_sma_ema():
    close = [10.0, 11.0, 12.0, 13.0, 14.0]
    
    # SMA 3
    sma_vals = alm.indicators.sma(close, period=3)
    assert len(sma_vals) == 5
    assert sma_vals[0] is None
    assert sma_vals[1] is None
    assert abs(sma_vals[2] - 11.0) < 1e-9  # (10+11+12)/3
    assert abs(sma_vals[3] - 12.0) < 1e-9  # (11+12+13)/3
    assert abs(sma_vals[4] - 13.0) < 1e-9  # (12+13+14)/3

    # EMA 3
    ema_vals = alm.indicators.ema(close, period=3)
    assert len(ema_vals) == 5
    assert ema_vals[0] is None
    assert ema_vals[1] is None
    # seed is SMA: ema_vals[2] = 11.0
    assert abs(ema_vals[2] - 11.0) < 1e-9
    # alpha = 2 / (3 + 1) = 0.5
    # ema_vals[3] = 13.0 * 0.5 + 11.0 * 0.5 = 12.0
    assert abs(ema_vals[3] - 12.0) < 1e-9
    # ema_vals[4] = 14.0 * 0.5 + 12.0 * 0.5 = 13.0
    assert abs(ema_vals[4] - 13.0) < 1e-9

def test_wma_hma_dema_tema_smma_vwma():
    close = [10.0] * 10
    volume = [1.0] * 10
    
    assert len(alm.indicators.wma(close, period=5)) == 10
    assert len(alm.indicators.hma(close, period=4)) == 10
    assert len(alm.indicators.dema(close, period=5)) == 10
    assert len(alm.indicators.tema(close, period=5)) == 10
    assert len(alm.indicators.smma(close, period=5)) == 10
    
    vwma_vals = alm.indicators.vwma(close, volume, period=5)
    assert len(vwma_vals) == 10
    assert vwma_vals[3] is None
    assert abs(vwma_vals[4] - 10.0) < 1e-9

def test_rsi():
    close = [10.0, 11.0, 12.0, 13.0, 14.0, 15.0]
    rsi_vals = alm.indicators.rsi(close, period=3)
    assert len(rsi_vals) == 6
    assert rsi_vals[0] is None
    assert rsi_vals[1] is None
    assert rsi_vals[2] is None  # RSI 3 needs 3 changes (period + 1 bars) → starts at index 3
    assert rsi_vals[3] == 100.0  # pure gains

def test_atr():
    high = [10.0, 11.0, 12.0]
    low = [9.0, 9.5, 9.8]
    close = [9.5, 9.8, 10.2]
    atr_vals = alm.indicators.atr(high, low, close, period=2)
    assert len(atr_vals) == 3
    assert atr_vals[0] is None
    assert atr_vals[1] is not None
    assert atr_vals[2] is not None
    assert atr_vals[2] > 0.0


def test_macd():
    close = [100.0 + i for i in range(40)]
    macd, signal, hist = alm.indicators.macd(close, fast=12, slow=26, signal=9)
    assert len(macd) == 40
    assert len(signal) == 40
    assert len(hist) == 40
    # Warmup period check
    assert macd[0] is None
    assert signal[0] is None
    assert hist[0] is None
    assert macd[-1] is not None
    assert signal[-1] is not None
    assert hist[-1] is not None

def test_bollinger_bands():
    close = [10.0, 11.0, 12.0, 13.0, 14.0]
    middle, upper, lower, percent_b, bandwidth = alm.indicators.bollinger_bands(close, period=3, k=2.0)
    assert len(middle) == 5
    assert len(upper) == 5
    assert len(lower) == 5
    assert len(percent_b) == 5
    assert len(bandwidth) == 5
    assert middle[0] is None
    assert upper[0] is None
    assert lower[0] is None
    assert middle[2] == 11.0
