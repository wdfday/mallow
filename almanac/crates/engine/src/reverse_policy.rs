/// Controls engine behaviour when a directional signal arrives while the
/// opposite position is already open.
///
/// | Variant | What happens |
/// |---------|-------------|
/// | `Exit`  | Close the existing position; **do not** open a new one. |
/// | `Flip`  | Close the existing position, then immediately open a new one in the signalled direction using the normal `risk.size()` path. |
///
/// When no policy is set (default), the engine's original net-qty behaviour
/// is preserved: the directional order is sent as-is and the portfolio nets
/// the quantities, which may leave a residual or an unintended exposure when
/// the two legs are sized differently.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ReversePolicy {
    /// Treat the reverse signal as a plain Exit: close the existing opposite
    /// position and do not open a new one in the signalled direction.
    Exit,
    /// Close the existing opposite position, then open a new position in the
    /// signalled direction sized by `risk.size()` (applied after the close
    /// order is emitted, on the current portfolio state).
    Flip,
}
