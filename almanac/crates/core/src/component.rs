use crate::event::Event;

/// Trait for event-driven components.
/// Each component receives events and may produce new events in response.
pub trait Component: Send {
    /// Process an event and return zero or more new events.
    fn on_event(&mut self, event: &Event) -> Vec<Event>;

    /// Component name (for logging/debugging).
    fn name(&self) -> &str;

    /// Reset component state (for batch optimization).
    fn reset(&mut self);
}
