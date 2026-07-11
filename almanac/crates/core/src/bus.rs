use crate::event::Event;

/// Abstraction over event queue (DIP).
///
/// implementations:
/// - `SyncBus` — `VecDeque<Event>` for max throughput backtesting
pub trait EventBus: Send {
    /// Push an event into the bus.
    fn send(&self, event: Event);

    /// Try to receive the next event. Returns `None` if the queue is empty.
    fn try_recv(&self) -> Option<Event>;
}
