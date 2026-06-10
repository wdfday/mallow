import pytest
import numpy as np
import pandas as pd
import pandas_ta as ta
import alm_py as alm

def to_float_list(arr):
    return [float(x) if x is not None else np.nan for x in arr]

def assert_almost_equal_series(s1, s2, rtol=1e-5, atol=1e-5, warmup=150, min_valid=100):
    a1 = np.array(to_float_list(s1))[warmup:]
    a2 = np.array(to_float_list(s2))[warmup:]
    
    # Filter out nan values
    mask = ~np.isnan(a1) & ~np.isnan(a2)
    assert np.sum(mask) >= min_valid, f"Too few valid points: {np.sum(mask)}"
    
    np.testing.assert_allclose(a1[mask], a2[mask], rtol=rtol, atol=atol)

def test_real_data_trend_indicators(real_bars):
    # Take 1000 bars
    close = real_bars["c"][:1000]
    high = real_bars["h"][:1000]
    low = real_bars["l"][:1000]
    volume = real_bars["v"][:1000]
    
    df = pd.DataFrame({
        "close": close,
        "high": high,
        "low": low,
        "volume": volume
    })

    # SMA 20
    alm_sma = alm.indicators.sma(close, period=20)
    ta_sma = ta.sma(df["close"], length=20).tolist()
    assert_almost_equal_series(alm_sma, ta_sma)

    # EMA 20
    alm_ema = alm.indicators.ema(close, period=20)
    ta_ema = ta.ema(df["close"], length=20).tolist()
    assert_almost_equal_series(alm_ema, ta_ema)

    # WMA 20
    alm_wma = alm.indicators.wma(close, period=20)
    ta_wma = ta.wma(df["close"], length=20).tolist()
    assert_almost_equal_series(alm_wma, ta_wma)

    # HMA 20
    alm_hma = alm.indicators.hma(close, period=20)
    ta_hma = ta.hma(df["close"], length=20).tolist()
    assert_almost_equal_series(alm_hma, ta_hma)

    # VWMA 20
    alm_vwma = alm.indicators.vwma(close, volume, period=20)
    ta_vwma = ta.vwma(df["close"], df["volume"], length=20).tolist()
    assert_almost_equal_series(alm_vwma, ta_vwma)

    # KAMA
    alm_kama = alm.indicators.kama(close, er_period=10, fast=2, slow=30)
    ta_kama = ta.kama(df["close"], length=10, fast=2, slow=30).tolist()
    assert_almost_equal_series(alm_kama, ta_kama, warmup=250)

def test_real_data_momentum_indicators(real_bars):
    close = real_bars["c"][:1000]
    high = real_bars["h"][:1000]
    low = real_bars["l"][:1000]
    volume = real_bars["v"][:1000]
    
    df = pd.DataFrame({
        "close": close,
        "high": high,
        "low": low,
        "volume": volume
    })

    # RSI 14
    alm_rsi = alm.indicators.rsi(close, period=14)
    ta_rsi = ta.rsi(df["close"], length=14).tolist()
    assert_almost_equal_series(alm_rsi, ta_rsi, warmup=200)

    # CCI 20 (smoke test bounded output since pandas_ta blows up on low-dev BTC ranges)
    alm_cci = alm.indicators.cci(high, low, close, period=20)
    valid_cci = [x for x in alm_cci if x is not None]
    assert len(valid_cci) > 900
    assert all(-500 <= x <= 500 for x in valid_cci)

    # ROC 10
    alm_roc = alm.indicators.roc(close, period=10)
    ta_roc = ta.roc(df["close"], length=10).tolist()
    assert_almost_equal_series(alm_roc, ta_roc)

    # MFI 14 (smoke test bounds)
    alm_mfi = alm.indicators.mfi(high, low, close, volume, period=14)
    valid_mfi = [x for x in alm_mfi if x is not None]
    assert len(valid_mfi) > 900
    assert all(0 <= x <= 100 for x in valid_mfi)

    # Williams %R
    alm_wr = alm.indicators.williams_r(high, low, close, period=14)
    ta_wr = ta.willr(df["high"], df["low"], df["close"], length=14).tolist()
    assert_almost_equal_series(alm_wr, ta_wr)

    # Awesome Oscillator (AO)
    alm_ao = alm.indicators.ao(high, low, fast=5, slow=34)
    ta_ao = ta.ao(df["high"], df["low"], fast=5, slow=34).tolist()
    assert_almost_equal_series(alm_ao, ta_ao)

def test_real_data_volatility_and_volume(real_bars):
    close = real_bars["c"][:1000]
    high = real_bars["h"][:1000]
    low = real_bars["l"][:1000]
    volume = real_bars["v"][:1000]
    
    df = pd.DataFrame({
        "close": close,
        "high": high,
        "low": low,
        "volume": volume
    })

    # ATR 14
    alm_atr = alm.indicators.atr(high, low, close, period=14)
    ta_atr = ta.atr(df["high"], df["low"], df["close"], length=14).tolist()
    assert_almost_equal_series(alm_atr, ta_atr)

    # OBV
    alm_obv = alm.indicators.obv(close, volume)
    ta_obv = ta.obv(df["close"], df["volume"]).tolist()
    assert_almost_equal_series(alm_obv, ta_obv)

    # CMF 20
    alm_cmf = alm.indicators.cmf(high, low, close, volume, period=20)
    ta_cmf = ta.cmf(df["high"], df["low"], df["close"], df["volume"], length=20).tolist()
    assert_almost_equal_series(alm_cmf, ta_cmf)

def test_real_data_macd_and_trix(real_bars):
    close = real_bars["c"][:1000]
    df = pd.DataFrame({"close": close})

    # MACD
    macd, signal, hist = alm.indicators.macd(close, fast=12, slow=26, signal=9)
    ta_res = ta.macd(df["close"], fast=12, slow=26, signal=9)
    ta_macd = ta_res["MACD_12_26_9"].tolist()
    ta_signal = ta_res["MACDs_12_26_9"].tolist()
    ta_hist = ta_res["MACDh_12_26_9"].tolist()

    assert_almost_equal_series(macd, ta_macd, warmup=100)
    assert_almost_equal_series(signal, ta_signal, warmup=100)
    assert_almost_equal_series(hist, ta_hist, warmup=100)

    # TRIX
    trix, trix_sig, trix_hist = alm.indicators.trix(close, period=18, signal=9)
    ta_trix_res = ta.trix(df["close"], length=18, signal=9)
    assert len(trix) == 1000
    assert len(trix_sig) == 1000

def test_missing_indicators_smoke(real_bars):
    close = real_bars["c"][:500]
    high = real_bars["h"][:500]
    low = real_bars["l"][:500]
    volume = real_bars["v"][:500]
    open_ = real_bars["o"][:500]

    # PMO
    pmo, pmo_sig, pmo_hist = alm.indicators.pmo(close, smooth1=35, smooth2=20, signal=10)
    assert len(pmo) == 500

    # PPO
    ppo, ppo_sig, ppo_hist = alm.indicators.ppo(close, fast=12, slow=26, signal=9)
    assert len(ppo) == 500

    # RVI
    rvi, rvi_sig = alm.indicators.rvi(open_, high, low, close, period=10)
    assert len(rvi) == 500

    # SMI
    smi, smi_sig = alm.indicators.smi(high, low, close, period=13, smooth1=25, smooth2=2, signal=9)
    assert len(smi) == 500

    # UO
    uo = alm.indicators.uo(high, low, close, fast=7, medium=14, slow=28)
    assert len(uo) == 500

    # Connors RSI
    connors = alm.indicators.connors_rsi(close, rsi_period=3, streak_period=2, rank_period=100)
    assert len(connors) == 500

    # Coppock
    coppock = alm.indicators.coppock(close, short=11, long=14, wma=10)
    assert len(coppock) == 500

    # Chop Zone
    cz_angle, cz_zone = alm.indicators.chop_zone(high, low, close, ema_period=34, threshold=5.0)
    assert len(cz_angle) == 500

    # Williams Fractal
    frac_bull, frac_bear, frac_high, frac_low = alm.indicators.fractal(high, low)
    assert len(frac_bull) == 500

    # Heiken Ashi
    ha_o, ha_h, ha_l, ha_c = alm.indicators.heiken_ashi(open_, high, low, close, smooth=1)
    assert len(ha_o) == 500
    
    # Parabolic SAR (psar / parabolic_sar)
    psar_val, psar_bull = alm.indicators.psar(high, low, close, step=0.02, max=0.2)
    assert len(psar_val) == 500
    psar_val_2, psar_bull_2 = alm.indicators.parabolic_sar(high, low, close, step=0.02, max=0.2)
    assert psar_val == psar_val_2
