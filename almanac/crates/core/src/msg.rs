//! NATS message types — auto-generated from `proto/market.proto` via prost.
//!
//! Single source of truth: `proto/market.proto`
//! Rust codegen: `prost-build` in `build.rs`
//! Go codegen:   `protoc-gen-go`
//!
//! NATS subjects:
//!   bars.{symbol}       ← stream-data → herald
//!   signals.{symbol}    ← herald → orchestrator
//!   engine.configure    ← orchestrator → herald
//!   engine.reset        ← orchestrator → herald

// Include the prost-generated code
include!(concat!(env!("OUT_DIR"), "/market.rs"));

// ── Conversion helpers: prost types ↔ domain types ──────────────────

impl From<&crate::bar::Bar> for BarMsg {
    fn from(b: &crate::bar::Bar) -> Self {
        Self {
            t: b.timestamp,
            s: b.symbol.clone(),
            o: b.open,
            h: b.high,
            l: b.low,
            c: b.close,
            v: b.volume,
        }
    }
}

impl From<BarMsg> for crate::bar::Bar {
    fn from(m: BarMsg) -> Self {
        crate::bar::Bar::new(m.t, m.s, m.o, m.h, m.l, m.c, m.v)
    }
}

impl From<&crate::signal::Signal> for SignalMsg {
    fn from(sig: &crate::signal::Signal) -> Self {
        let dir = match sig.direction {
            crate::signal::Direction::Long => "long",
            crate::signal::Direction::Short => "short",
            crate::signal::Direction::Close => "close",
        };
        let (target_price, stop_price, pattern_kind, confidence) =
            if let Some(ref p) = sig.pattern {
                // Pattern fields take priority; fall back to signal top-level fields.
                (
                    p.target_price.or(sig.target_price),
                    p.stop_price.or(sig.stop_price),
                    Some(p.pattern_kind.clone()),
                    Some(p.confidence),
                )
            } else {
                (sig.target_price, sig.stop_price, None, None)
            };
        Self {
            t: sig.timestamp,
            s: sig.symbol.clone(),
            dir: dir.into(),
            strength: sig.strength,
            price: sig.price,
            target_price,
            stop_price,
            pattern_kind,
            confidence,
            is_offset: if sig.is_offset { Some(true) } else { None },
        }
    }
}
