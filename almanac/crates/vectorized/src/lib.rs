pub mod atr_trailing;
pub mod bollinger_breakout;
pub mod grid_search;
pub mod ma_crossover;
pub mod macd_crossover;
pub mod result;
pub mod rsi_mean_rev;
pub mod stochastic;
mod utils;

pub use atr_trailing::backtest_atr_trailing;
pub use bollinger_breakout::backtest_bollinger_breakout;
pub use grid_search::{GridResult, GridSearch, SortBy};
pub use ma_crossover::backtest_ma_crossover;
pub use macd_crossover::backtest_macd_crossover;
pub use result::VectorizedResult;
pub use rsi_mean_rev::backtest_rsi_mean_rev;
pub use stochastic::backtest_stochastic;
