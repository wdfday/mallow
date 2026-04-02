use alm_core::bus::EventBus;
use alm_core::event::Event;
use std::cell::UnsafeCell;
use tokio::sync::mpsc;

/// Async-ready event bus backed by `tokio::mpsc::unbounded_channel`.
///
/// Multi-producer capable: clone the inner sender to feed events
/// from multiple sources (WebSocket, NATS, timers) in live mode.
pub struct TokioBus {
    tx: mpsc::UnboundedSender<Event>,
    rx: UnsafeCell<mpsc::UnboundedReceiver<Event>>,
}

impl TokioBus {
    pub fn new() -> Self {
        let (tx, rx) = mpsc::unbounded_channel();
        Self {
            tx,
            rx: UnsafeCell::new(rx),
        }
    }

    /// Get a clone of the sender — use this to feed events from external sources.
    pub fn sender(&self) -> mpsc::UnboundedSender<Event> {
        self.tx.clone()
    }
}

impl Default for TokioBus {
    fn default() -> Self {
        Self::new()
    }
}

/// SAFETY: TokioBus is exclusively owned and accessed by the Engine.
/// The receiver is never shared across threads — only the engine loop calls try_recv.
unsafe impl Send for TokioBus {}

impl EventBus for TokioBus {
    fn send(&self, event: Event) {
        let _ = self.tx.send(event);
    }

    fn try_recv(&self) -> Option<Event> {
        // SAFETY: rx is exclusively accessed by the engine loop.
        // No concurrent access — UnsafeCell provides interior mutability.
        let rx = unsafe { &mut *self.rx.get() };
        rx.try_recv().ok()
    }
}
