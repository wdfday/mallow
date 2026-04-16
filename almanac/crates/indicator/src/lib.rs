pub mod momentum;
pub mod pattern;
pub mod regime;
pub mod trend;
pub mod util;
pub mod volatility;
pub mod volume;

pub use regime::RegimeDetector;

pub use util::{RollingMax, RollingMin};

// Trend + MA family
pub use trend::{
    Adx, AdxValue, Alligator, AlligatorValue, Alma, Aroon, AroonValue, Dema, Dmi, DmiValue, Ema,
    Gmma, GmmaValue, Hma, Kama, KalmanFilter, KalmanValue, Kdj, KdjValue, Lsma, LsmaValue,
    Macd, MacdValue, McGinleyDynamic, Sma, Smma, Tema, Trix, TrixValue, Vortex, VortexValue,
    Vwma, Wma,
};

// Momentum + Oscillators
pub use momentum::{
    AwesomeOscillator, Bop, BullBearPower, BullBearValue, Cci, Cmo, ConnorsRsi, Coppock, Dpo,
    Fisher, FisherValue, Kst, KstValue, Mfi, Mom, Pmo, PmoValue, Ppo, PpoValue, Rci, RciRibbon,
    RciRibbonValue, Roc, Rsi, Rvi, RviValue, Smi, SmiValue, Stochastic, StochasticValue, Tsi,
    Uo, WilliamsR,
};

// Volatility
pub use volatility::{
    Atr, AtrValue, BBands, BBandsValue, ChandeKrollStop, ChandeKrollValue, ChandelierExit,
    ChandelierValue, Chop, ChopZone, ChopZoneClass, ChopZoneValue, Donchian, DonchianValue,
    Keltner, KeltnerValue, SuperTrend, SuperTrendValue, VolatilityRatio,
};

// Volume
pub use volume::{Cmf, Obv, Vwap};

// Pattern
pub use pattern::{
    ElderRay, ElderRayValue, FractalValue, HaBar, HeikenAshi, Ichimoku, IchimokuValue, ParabolicSar,
    Rwi, RwiValue, SarValue, StochRsiValue, StochasticRsi, WilliamsFractal,
};
