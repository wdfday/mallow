//! Bar-channel stress test — feed → bar_tx (bounded) → Handler::run's
//! `handle_bar_event` path (real `Ledger::advance`, simulated NATS publish).
//!
//! Investigates a specific risk: `handle_bar_event` runs fully sequentially
//! inside `Handler::run`'s select loop (no spawn) — for a *closed* bar it does
//! `ledger.advance()` then `client.publish(...)` awaited inline, bounded by a
//! 2s timeout (`CLOSED_BAR_PUBLISH_TIMEOUT`, handler.rs:87). If many symbols'
//! bars close in the same instant (realistic — WS feeds align to minute
//! boundaries) and NATS publish isn't instant, the handler can fall behind
//! draining `bar_tx`. Unlike the signal channel (N8, `try_send` — drops),
//! `bar_tx.send()` is `.await`ed by the feed task (blocking, no drop) — the
//! documented failure mode is worse: feed task blocks → WS read loop starved
//! → exchange ping/pong lapses → reconnect storm (see handler.rs:357-361,
//! TOKIO_CHANNELS.md).
//!
//! This tool replicates `handle_bar_event`'s shape (not its exact code, which
//! is private to the `alm-herald` binary) against the real, public
//! `alm_herald::feed::{BarEvent, BAR_CHANNEL_CAP}` and a real `Ledger`, with a
//! configurable simulated NATS publish delay — same methodology as
//! `signal_stress`'s `--consumer-delay-us` for N8.
//!
//! Usage:
//!   cargo run --release --bin wss-stress -- --symbols 100 --publish-delay-us 0
//!   cargo run --release --bin wss-stress -- --symbols 100 --publish-delay-us 50000

use std::sync::Arc;
use std::time::{Duration, Instant};

use alm_core::{Bar, Timeframe};
use alm_herald::feed::{BarEvent, BAR_CHANNEL_CAP};
use alm_ledger::{Ledger, LedgerConfig};
use tokio::sync::mpsc;
use tokio::time::timeout;

/// Mirrors handler.rs's real constant exactly — kept as a literal here since
/// the constant itself is private to the `alm-herald` binary crate.
const CLOSED_BAR_PUBLISH_TIMEOUT: Duration = Duration::from_secs(2);

struct Args {
    symbols: usize,
    channel_capacity: usize,
    publish_delay_us: u64,
}

fn parse_args() -> Args {
    let raw: Vec<String> = std::env::args().collect();
    let get = |flag: &str, default: &str| -> String {
        raw.windows(2)
            .find(|w| w[0] == flag)
            .map(|w| w[1].clone())
            .unwrap_or_else(|| default.to_string())
    };
    Args {
        symbols: get("--symbols", "100").parse().expect("--symbols must be a number"),
        channel_capacity: get("--channel-capacity", &BAR_CHANNEL_CAP.to_string())
            .parse()
            .expect("--channel-capacity must be a number"),
        publish_delay_us: get("--publish-delay-us", "0").parse().expect("--publish-delay-us must be a number"),
    }
}

#[tokio::main(flavor = "multi_thread")]
async fn main() {
    let args = parse_args();
    println!(
        "wss-stress: symbols={} channel_capacity={} (real BAR_CHANNEL_CAP={}) publish_delay={}µs",
        args.symbols, args.channel_capacity, BAR_CHANNEL_CAP, args.publish_delay_us
    );

    let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
    let (bar_tx, mut bar_rx) = mpsc::channel::<BarEvent>(args.channel_capacity);

    // ── Consumer: replicates handle_bar_event's shape ───────────────────────
    // Sequential, no spawn for closed bars — exactly like the real select
    // loop in Handler::run, which is precisely what's under test here.
    let consumer_ledger = ledger.clone();
    let publish_delay_us = args.publish_delay_us;
    let mut advance_times: Vec<Duration> = Vec::new();
    let mut publish_times: Vec<Duration> = Vec::new();
    let mut timed_out = 0usize;
    let consumer = tokio::spawn(async move {
        let mut n = 0usize;
        while let Some(event) = bar_rx.recv().await {
            n += 1;
            let advance_start = Instant::now();
            let _ = consumer_ledger.advance(event.tf, event.bar);
            advance_times.push(advance_start.elapsed());

            // Simulated NATS closed-bar publish — real code: `self.client
            // .publish(...)` awaited inline, bounded by CLOSED_BAR_PUBLISH_TIMEOUT.
            let publish_start = Instant::now();
            let sim_publish = async {
                if publish_delay_us > 0 {
                    tokio::time::sleep(Duration::from_micros(publish_delay_us)).await;
                }
            };
            match timeout(CLOSED_BAR_PUBLISH_TIMEOUT, sim_publish).await {
                Ok(()) => {}
                Err(_) => timed_out += 1,
            }
            publish_times.push(publish_start.elapsed());
        }
        (n, advance_times, publish_times, timed_out)
    });

    // ── Producers: one task per symbol, all racing to send their closed M1
    // bar at (as close as tokio scheduling allows to) the same instant —
    // simulates every symbol's minute boundary landing together, which is
    // exactly how real WS feeds behave (exchanges timestamp-align klines).
    let symbol_names: Vec<String> = (0..args.symbols).map(|i| format!("STRESS{i}")).collect();
    let ts = 1_700_000_000_000i64;
    let mut producer_handles = Vec::with_capacity(args.symbols);
    let burst_start = Instant::now();
    for sym in symbol_names {
        let bar_tx = bar_tx.clone();
        producer_handles.push(tokio::spawn(async move {
            let bar = Bar::new(ts, &sym, 100.0, 100.5, 99.5, 100.0, 1.0);
            let event = BarEvent { tf: Timeframe::M1, bar, closed: true, received_at_ms: alm_herald::feed::now_ms() };
            let send_start = Instant::now();
            let ok = bar_tx.send(event).await.is_ok();
            (send_start.elapsed(), ok)
        }));
    }
    drop(bar_tx); // let the consumer's rx.recv() return None once everything drains

    let mut send_times: Vec<Duration> = Vec::with_capacity(producer_handles.len());
    for h in producer_handles {
        let (elapsed, ok) = h.await.expect("producer task panicked");
        assert!(ok, "bar_tx closed mid-burst — consumer panicked?");
        send_times.push(elapsed);
    }
    let burst_wall = burst_start.elapsed();

    let (n, advance_times, publish_times, timed_out) = consumer.await.expect("consumer task panicked");

    send_times.sort();
    let mut adv_sorted = advance_times;
    adv_sorted.sort();
    let mut pub_sorted = publish_times;
    pub_sorted.sort();

    println!("bars processed: {n}/{}", args.symbols);
    println!(
        "producer send() block time  p50={:.3}ms  p99={:.3}ms  max={:.3}ms  (blocked = waited for channel space)",
        pctl(&send_times, 0.50).as_secs_f64() * 1000.0,
        pctl(&send_times, 0.99).as_secs_f64() * 1000.0,
        send_times.last().map(|d| d.as_secs_f64() * 1000.0).unwrap_or(0.0),
    );
    println!(
        "ledger.advance() time       p50={:.3}ms  p99={:.3}ms  max={:.3}ms",
        pctl(&adv_sorted, 0.50).as_secs_f64() * 1000.0,
        pctl(&adv_sorted, 0.99).as_secs_f64() * 1000.0,
        adv_sorted.last().map(|d| d.as_secs_f64() * 1000.0).unwrap_or(0.0),
    );
    println!(
        "simulated publish (timeout-bounded)  p50={:.3}ms  p99={:.3}ms  max={:.3}ms  timed_out={timed_out}",
        pctl(&pub_sorted, 0.50).as_secs_f64() * 1000.0,
        pctl(&pub_sorted, 0.99).as_secs_f64() * 1000.0,
        pub_sorted.last().map(|d| d.as_secs_f64() * 1000.0).unwrap_or(0.0),
    );
    println!("total burst wall time: {:.3}ms", burst_wall.as_secs_f64() * 1000.0);

    let worst_send_ms = send_times.last().map(|d| d.as_secs_f64() * 1000.0).unwrap_or(0.0);
    if worst_send_ms > 1000.0 {
        println!(
            "\n⚠ worst-case producer send() blocked {worst_send_ms:.0}ms — a real WS feed task blocked this long \
             cannot read from its socket; if this approaches the exchange's ping/pong interval, expect disconnects."
        );
    } else {
        println!("\nOK: producer send() never blocked long enough to be a real WS-disconnect risk in this run.");
    }
}

fn pctl(sorted: &[Duration], p: f64) -> Duration {
    if sorted.is_empty() {
        return Duration::ZERO;
    }
    let idx = ((sorted.len() - 1) as f64 * p).round() as usize;
    sorted[idx]
}
