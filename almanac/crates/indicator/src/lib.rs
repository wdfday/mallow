pub mod regime;
pub mod risk;
pub mod signal;
pub mod viewing;
pub mod util;

mod box_;
pub use box_::{field_kind, FieldKind, IndicatorBox, BOOL_FIELDS};

pub use regime::RegimeDetector;
pub use util::{RollingMax, RollingMin};

// signal: trend + MA family
pub use signal::trend::{
    Adx, AdxValue, Alma, Aroon, AroonOscillator, AroonValue, Dema, Dmi, DmiValue, Ema, Hma, Kama, KalmanFilter,
    KalmanValue, Kdj, KdjValue, Lsma, LsmaValue, Macd, MacdValue, McGinleyDynamic, Sma, Smma,
    Tema, Trix, TrixValue, Vortex, VortexValue, Vwma, Wma,
};

// signal: momentum / oscillators
pub use signal::momentum::{
    AwesomeOscillator, Bop, BullBearPower, BullBearValue, Cci, Cmo, ConnorsRsi, Coppock, Dpo,
    Fisher, FisherValue, Kst, KstValue, Mfi, Mom, Pmo, PmoValue, Ppo, PpoValue, Rci, RciRibbon,
    RciRibbonValue, Roc, Rsi, Rvi, RviValue, Smi, SmiValue, Stochastic, StochasticValue, Tsi, Uo,
    WilliamsR,
};

// signal: volume
pub use signal::volume::{Cmf, Obv, Vwap};

// signal: channels
pub use signal::channel::{BBands, BBandsValue, Donchian, DonchianValue, Keltner, KeltnerValue};

// signal: pattern
pub use signal::pattern::{
    ElderRay, ElderRayValue, FractalValue, Ichimoku, IchimokuValue, Rwi, RwiValue, StochRsiValue,
    StochasticRsi, WilliamsFractal,
};

// viewing
pub use viewing::{Alligator, AlligatorValue, Gmma, GmmaValue, HaBar, HeikenAshi};

// regime
pub use regime::{Chop, ChopZone, ChopZoneClass, ChopZoneValue, VolatilityRatio};

// risk
pub use risk::{
    Atr, AtrValue, ChandeKrollStop, ChandeKrollValue, ChandelierExit, ChandelierValue, ParabolicSar,
    SarValue, SuperTrend, SuperTrendValue,
};
