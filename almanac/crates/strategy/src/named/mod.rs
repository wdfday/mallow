//! Concrete named strategies — 58 ready-to-use implementations.

// ── Original ──────────────────────────────────────────────────────────────────
pub mod atr_trailing;
pub mod ma_crossover;
pub mod risk;
pub mod rsi_mean_rev;
pub mod stochastic;

// ── MA / Trend ────────────────────────────────────────────────────────────────
pub mod aroon_strategy;
pub mod chandelier_exit;
pub mod dema_crossover;
pub mod donchian_breakout;
pub mod gmma_crossover;
pub mod hma_crossover;
pub mod ichimoku_strategy;
pub mod ma_pullback;
pub mod tema_crossover;
pub mod triple_ema;
pub mod trend_follower;
pub mod trend_transition;

// ── Momentum / Oscillator ─────────────────────────────────────────────────────
pub mod cci_reversal;
pub mod chop_filter_strategy;
pub mod connors_rsi_strategy;
pub mod kama_strategy;
pub mod macd_crossover;
pub mod macd_ma;
pub mod mfi_strategy;
pub mod momentum_roc;
pub mod roc_strategy;
pub mod stoch_rsi_strategy;
pub mod stochastic_dk;
pub mod tsi_strategy;
pub mod trix_strategy;

// ── Volatility ────────────────────────────────────────────────────────────────
pub mod bb_squeeze;
pub mod bollinger_macd;
pub mod volatility_breakout;
pub mod volatility_squeezer;
pub mod volatility_vanguard;

// ── Volume ────────────────────────────────────────────────────────────────────
pub mod ao_strategy;
pub mod kdj_strategy;
pub mod vwap_strategy;

// ── Composite / Multi-indicator ───────────────────────────────────────────────
pub mod alligator_strategy;
pub mod dmi_adx;
pub mod elder_ray_strategy;
pub mod equilibrium_explorer;
pub mod mean_reversion;
pub mod oscillator_overlord;
pub mod range_rover;
pub mod reversal_catcher;
pub mod rwi_strategy;
pub mod supertrend_strategy;
pub mod swing_trader;
pub mod waddah_attar;
pub mod wolfstein;

// ── Price action / Pattern ────────────────────────────────────────────────────
pub mod heiken_ashi_strategy;
pub mod highest_breakout;
pub mod pattern_breakout;
pub mod price_action;
pub mod sar_strategy;

// ── Kitchen Sink ─────────────────────────────────────────────────────────────
pub mod kitchen_sink;

// ── Session / Special ────────────────────────────────────────────────────────
pub mod orb_breakout;
pub mod pixel_3;
pub mod scalping_ema;

// ── New: lesser-used indicator strategies ────────────────────────────────────
pub mod adx_ema_cross;
pub mod alma_cross;
pub mod bb_rsi_reversal;
pub mod cmf_ema_trend;
pub mod cmo_zero_cross;
pub mod fisher_crossover;
pub mod lsma_cross;
pub mod obv_ema_trend;
pub mod ppo_histogram;
pub mod rsi_ma_cross;
pub mod smi_reversal;
pub mod uo_reversal;
pub mod vortex_trend;
pub mod vwma_rsi;
pub mod williams_r_ma;

// ── Re-exports ────────────────────────────────────────────────────────────────

pub use alligator_strategy::AlligatorStrategy;
pub use ao_strategy::AoStrategy;
pub use aroon_strategy::AroonTrend;
pub use atr_trailing::AtrTrailingStop;
pub use bb_squeeze::BbSqueeze;
pub use bollinger_macd::BollingerMacd;
pub use cci_reversal::CciReversal;
pub use chandelier_exit::ChandelierExit;
pub use chop_filter_strategy::ChopFilterStrategy;
pub use connors_rsi_strategy::ConnorsRsiStrategy;
pub use dema_crossover::DemaCrossover;
pub use dmi_adx::DmiAdx;
pub use donchian_breakout::DonchianBreakout;
pub use elder_ray_strategy::ElderRayStrategy;
pub use equilibrium_explorer::EquilibriumExplorer;
pub use gmma_crossover::GmmaCrossover;
pub use heiken_ashi_strategy::{HaBreakout, HaColor, HaHarmonizer};
pub use highest_breakout::HighestBreakout;
pub use kitchen_sink::KitchenSinkStrategy;
pub use hma_crossover::HmaCrossover;
pub use ichimoku_strategy::{IchimokuCloud, IchimokuCross};
pub use kama_strategy::KamaStrategy;
pub use kdj_strategy::KdjStrategy;
pub use ma_crossover::MaCrossover;
pub use ma_pullback::MaPullback;
pub use macd_crossover::MacdCrossover;
pub use macd_ma::MacdMa;
pub use mean_reversion::MeanReversion;
pub use mfi_strategy::{MfiRevert, MfiTrend};
pub use momentum_roc::{DualMomentum, MomentumRoc};
pub use orb_breakout::OrbBreakout;
pub use oscillator_overlord::OscillatorOverlord;
pub use pattern_breakout::PatternBreakoutStrategy;
pub use pixel_3::Pixel3;
pub use price_action::PriceActionSwing;
pub use range_rover::RangeRover;
pub use reversal_catcher::ReversalCatcher;
pub use risk::{AnySizer, AtrSizing, EqualWeight, FixedFractional, FixedQuantity, FixedUsd, KellySizing};
pub use roc_strategy::RocCrossover;
pub use rsi_mean_rev::RsiMeanRev;
pub use rwi_strategy::RwiStrategy;
pub use sar_strategy::SarStrategy;
pub use scalping_ema::ScalpingEma;
pub use stoch_rsi_strategy::StochRsiStrategy;
pub use stochastic::StochasticCrossover;
pub use stochastic_dk::StochasticDk;
pub use supertrend_strategy::{SupertrendMacd, SupertrendStrategy};
pub use swing_trader::SwingTrader;
pub use tema_crossover::TemaCrossover;
pub use trend_follower::TrendFollower;
pub use trend_transition::TrendTransition;
pub use triple_ema::TripleEma;
pub use trix_strategy::TrixStrategy;
pub use tsi_strategy::TsiStrategy;
pub use volatility_breakout::{BbKeltnerSqueeze, KeltnerBreakout, VolatilityRatioBreakout};
pub use volatility_squeezer::VolatilitySqueezer;
pub use volatility_vanguard::VolatilityVanguard;
pub use vwap_strategy::{VwapBounce, VwapTrend};
pub use waddah_attar::WaddahAttar;
pub use wolfstein::Wolfstein;

pub use adx_ema_cross::AdxEmaCross;
pub use alma_cross::AlmaCross;
pub use bb_rsi_reversal::BbRsiReversal;
pub use cmf_ema_trend::CmfEmaTrend;
pub use cmo_zero_cross::CmoZeroCross;
pub use fisher_crossover::FisherCrossover;
pub use lsma_cross::LsmaCross;
pub use obv_ema_trend::ObvEmaTrend;
pub use ppo_histogram::PpoHistogram;
pub use rsi_ma_cross::RsiMaCross;
pub use smi_reversal::SmiReversal;
pub use uo_reversal::UoReversal;
pub use vortex_trend::VortexTrend;
pub use vwma_rsi::VwmaRsi;
pub use williams_r_ma::WilliamsRMa;
