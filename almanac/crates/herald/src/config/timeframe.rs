use alm_core::Timeframe;

/// All timeframes with a native WebSocket kline stream on both Binance and OKX.
///
/// Excluded:
/// - `M10` — not available on either exchange's kline API.
/// - `H8`  — not available on OKX (only Binance has it); excluded for cross-exchange parity.
///
/// This is the **source of truth** for what TFs herald can subscribe to live.
/// [`super::symbols::SymbolEntry`] uses this list to validate `timeframes:` entries
/// in `symbols.yaml` at startup.
pub const SUBSCRIBE_TFS: &[Timeframe] = &[
    Timeframe::M1,  Timeframe::M3,  Timeframe::M5,
    Timeframe::M15, Timeframe::M30,
    Timeframe::H1,  Timeframe::H2,  Timeframe::H4,
    Timeframe::H6,  Timeframe::H12,
    Timeframe::D1,  Timeframe::W1,  Timeframe::MN,
];

/// Parse a timeframe string, accepting only TFs in [`SUBSCRIBE_TFS`].
/// Returns `None` for unknown strings or TFs without a native WS kline.
pub fn parse_tf(s: &str) -> Option<Timeframe> {
    let tf: Timeframe = s.to_ascii_uppercase().parse().ok()?;
    SUBSCRIBE_TFS.contains(&tf).then_some(tf)
}

/// Comma-separated list of valid TF strings — for use in error messages.
pub fn valid_tf_list() -> String {
    SUBSCRIBE_TFS
        .iter()
        .map(|tf| tf.to_string())
        .collect::<Vec<_>>()
        .join(", ")
}
