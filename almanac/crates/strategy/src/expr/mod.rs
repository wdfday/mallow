//! Expression / template strategies — define entry/exit logic as text.
//!
//! | Module  | Engine          | Syntax              |
//! |---------|-----------------|---------------------|
//! | `cel`   | cel-interpreter | `ema(9)`, `rsi(14)` |
//!
//! For declarative JSON-based strategies see [`crate::dynamic`].

pub mod cel;
pub use cel::CelStrategy;
