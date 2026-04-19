//! `IndicatorHandle` — RAII subscription to a live indicator in the
//! ledger. As long as at least one handle is alive, the underlying
//! [`IndicatorCell`](crate::state::IndicatorCell) is retained and
//! updated every advance. When the last handle drops, the cell enters a
//! grace period (see [`LedgerConfig::drop_grace_bars`](crate::ledger::LedgerConfig));
//! if nothing re-acquires it before the grace expires, the cell is
//! evicted on a subsequent [`Ledger::advance`](crate::ledger::Ledger::advance).
//!
//! "Pinned" handles (produced by [`Ledger::pin_indicator`](crate::ledger::Ledger::pin_indicator))
//! bypass the refcount entirely: their drop is a no-op and the cell
//! lives for the lifetime of the ledger. The warm-set uses this pattern
//! so popular overlays are never evicted.

use std::sync::{Arc, Weak};

use alm_core::Timeframe;
use tracing::trace;

use crate::ledger::Ledger;
use crate::spec::IndicatorSpec;

/// Subscription handle returned by [`Ledger::acquire_indicator`].
///
/// Cheap to clone (backed by `Arc`) — every clone keeps the refcount
/// alive. The cell is released only when *all* clones go out of scope.
#[derive(Clone)]
pub struct IndicatorHandle {
    inner: Arc<HandleInner>,
}

/// Inner reference kept by every clone of an `IndicatorHandle`. Drops
/// exactly once, when the last clone is released.
struct HandleInner {
    ledger: Weak<Ledger>,
    symbol: Arc<str>,
    tf: Timeframe,
    spec: IndicatorSpec,
    /// Pinned handles never touch the refcount on drop — used by the
    /// warm-set to keep popular indicators immortal.
    pinned: bool,
}

impl IndicatorHandle {
    pub(crate) fn new(
        ledger: &Arc<Ledger>,
        symbol: Arc<str>,
        tf: Timeframe,
        spec: IndicatorSpec,
        pinned: bool,
    ) -> Self {
        Self {
            inner: Arc::new(HandleInner {
                ledger: Arc::downgrade(ledger),
                symbol,
                tf,
                spec,
                pinned,
            }),
        }
    }

    pub fn symbol(&self) -> &str {
        &self.inner.symbol
    }

    pub fn timeframe(&self) -> Timeframe {
        self.inner.tf
    }

    pub fn spec(&self) -> &IndicatorSpec {
        &self.inner.spec
    }

    pub fn is_pinned(&self) -> bool {
        self.inner.pinned
    }
}

impl std::fmt::Debug for IndicatorHandle {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("IndicatorHandle")
            .field("symbol", &self.inner.symbol)
            .field("tf", &self.inner.tf)
            .field("spec", &self.inner.spec.canonical_key())
            .field("pinned", &self.inner.pinned)
            .finish()
    }
}

impl Drop for HandleInner {
    fn drop(&mut self) {
        if self.pinned {
            trace!(
                symbol = %self.symbol,
                tf = ?self.tf,
                spec = %self.spec,
                "pinned handle dropped — refcount left unchanged",
            );
            return;
        }
        match self.ledger.upgrade() {
            Some(led) => led.release_indicator(&self.symbol, self.tf, &self.spec),
            None => trace!(
                symbol = %self.symbol,
                tf = ?self.tf,
                spec = %self.spec,
                "ledger gone; skipping release",
            ),
        }
    }
}
