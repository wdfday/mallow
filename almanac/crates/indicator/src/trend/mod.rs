// MA family
pub mod alligator;
pub mod alma;
pub mod dema;
pub mod ema;
pub mod gmma;
pub mod hma;
pub mod lsma;
pub mod mcginley;
pub mod sma;
pub mod smma;
pub mod tema;
pub mod vwma;
pub mod wma;

// Trend
pub mod adx;
pub mod aroon;
pub mod dmi;
pub mod kama;
pub mod kdj;
pub mod macd;
pub mod trix;
pub mod vortex;

pub use alligator::{Alligator, AlligatorValue};
pub use alma::Alma;
pub use dema::Dema;
pub use ema::Ema;
pub use gmma::{Gmma, GmmaValue};
pub use hma::Hma;
pub use lsma::{Lsma, LsmaValue};
pub use mcginley::McGinleyDynamic;
pub use sma::Sma;
pub use smma::Smma;
pub use tema::Tema;
pub use vwma::Vwma;
pub use wma::Wma;

pub use adx::{Adx, AdxValue};
pub use aroon::{Aroon, AroonValue};
pub use dmi::{Dmi, DmiValue};
pub use kama::Kama;
pub use kdj::{Kdj, KdjValue};
pub use macd::{Macd, MacdValue};
pub use trix::{Trix, TrixValue};
pub use vortex::{Vortex, VortexValue};
