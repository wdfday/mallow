use alm_core::bus::EventBus;
use alm_core::event::Event;
use std::cell::RefCell;
use std::collections::VecDeque;

/// Zero-cost sync event bus backed by `VecDeque`.
///
/// Single-threaded only. Uses `RefCell` for interior mutability.
/// Maximum throughput — no atomic operations, no channel overhead.
pub struct SyncBus {
    queue: RefCell<VecDeque<Event>>,
}

impl SyncBus {
    pub fn new() -> Self {
        Self {
            queue: RefCell::new(VecDeque::new()),
        }
    }
}

impl Default for SyncBus {
    fn default() -> Self {
        Self::new()
    }
}

/// SAFETY: SyncBus is only used in single-threaded backtest contexts.
/// The Engine owns it exclusively — no concurrent access occurs.
unsafe impl Send for SyncBus {}

impl EventBus for SyncBus {
    fn send(&self, event: Event) {
        self.queue.borrow_mut().push_back(event);
    }

    fn try_recv(&self) -> Option<Event> {
        self.queue.borrow_mut().pop_front()
    }
}
