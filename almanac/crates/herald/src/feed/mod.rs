pub mod binance;
pub mod okx;

use tokio::sync::mpsc;
use alm_core::Bar;

pub type BarTx = mpsc::UnboundedSender<Bar>;
pub type BarRx = mpsc::UnboundedReceiver<Bar>;
