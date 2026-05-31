//! NATS message types — auto-generated from `proto/market.proto` via prost.
//!
//! Single source of truth: `proto/market.proto`
//! Rust codegen: `prost-build` in `build.rs`
//! Go codegen:   `protoc-gen-go`

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
            crate::signal::Direction::Exit => "exit",
        };
        Self {
            t: sig.timestamp,
            s: sig.symbol.clone(),
            dir: dir.into(),
            strength: sig.strength,
            price: sig.price,
            target_price: sig.target_price,
            stop_price: sig.stop_price,
            is_offset: if sig.is_offset { Some(true) } else { None },
            reason: sig.reason.clone(),
            atr: sig.atr,
            trailing_stop_pct: sig.trailing_stop_pct,
            max_bars_held: sig.max_bars_held.map(|v| v as i64),
        }
    }
}
