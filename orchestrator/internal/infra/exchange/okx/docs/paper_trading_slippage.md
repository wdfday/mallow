# OKX Paper Trading — Slippage & Order Book Findings

## TL;DR

OKX paper trading fills against the **demo order book**, not the real market book.
To accurately simulate slippage we must implement our own VWAP sweep using the **real book**.

---

## Evidence (TestOKX_LargeOrderImpact, TRX-USDT, 2026-04-09)

| qty | %depth | exp_vwap_real | exp_vwap_demo | fill_avg | diff_real | diff_demo |
|---|---|---|---|---|---|---|
| 1,736 | 1.1% | 0.318430 | 0.318430 | 0.318430 | +0.00 bps | **0.00 bps** |
| 8,678 | 4.8% | 0.318430 | 0.318430 | 0.318430 | +0.00 bps | **0.00 bps** |
| 26,034 | 14.3% | 0.318432 | 0.318450 | 0.318450 | +0.57 bps | **0.00 bps** |
| 52,069 | 28.6% | 0.318442 | 0.318999 | 0.319253 | +25.45 bps | +7.95 bps* |
| 104,138 | 57.9% | 0.318471 | 0.318450 | 0.318450 | −0.65 bps | **0.00 bps** |

\* order 4: demo book was shallow (~30k TRX available), OKX walked into sparse levels to fill.
Order 5: OKX injected ~42M TRX of artificial liquidity into the demo book at a single level.

**fill_avg tracks exp_vwap_demo exactly on orders 1–3 and 5.**
The real book VWAP is irrelevant to how OKX simulates fills.

### Book comparison (BTC-USDT, $1000 probe, 2026-04-09)

| level | real_price | real_size | demo_price | demo_size |
|---|---|---|---|---|
| [1] | 70748.4 | 0.1997 | 70748.4 | **0.0003** |
| [2] | 70748.5 | 0.0289 | 70748.5 | 0.0289 |

Demo level-1 size is ~667× thinner for BTC. Price feed is shared; liquidity is not.

### Demo price feed can diverge significantly from real market

Observed 2026-04-08: BTC demo mid-price deviated **~$1,000** from real market mid.
This means the demo environment is **not just a liquidity simulation** — the price itself
can drift, likely because the demo matching engine replays a delayed or synthetic feed
rather than live ticks.

**Implication:** strategy signals generated against real prices (from `stream-data` or
signal-engine) may trigger entries/exits at prices that don't exist in demo. A bot trained
and validated purely in paper mode can show P&L that is impossible to reproduce live.

---

## Implications

### What OKX paper trading does

- Uses the real price feed (same mid/spread).
- Maintains a **separate simulated order book** with different (usually thinner, sometimes artificially deep) liquidity.
- Fill engine sweeps the **demo book** to compute VWAP. Book-walk slippage is modelled, but against synthetic depth.
- At high depth consumption (>30%) the demo book can be exhausted, causing fills at unrealistic prices, or OKX injects a large synthetic level to absorb the order.

### What this means for us

| Scenario | Problem |
|---|---|
| Use paper fill_avg as ground truth | Misleading — reflects demo book, not real market impact |
| Use real book VWAP as expected price | Also wrong when comparing against paper fills |
| Use demo book VWAP as expected price | Correct for validating paper fill behaviour, useless for production sizing |

---

## Decision: self-simulate slippage using real book

OKX paper trading is **not a reliable simulation environment** for two independent reasons:

1. **Liquidity** — demo book depth is synthetic and can be arbitrarily thin or deep.
2. **Price** — demo mid can drift >$1,000 from real market on BTC (observed 2026-04-08).

For production order sizing and pre-trade risk, implement a local VWAP sweep
against the **real** market book:

```
expectedFill(side, qty, realBook) → (vwap, filledQty, partialFill bool)
```

Inputs: live real-market L2 snapshot (books5 or REST `/api/v5/market/books`).
Output: expected average fill price assuming full book-walk with no queue priority.

This gives a **conservative, real-market-based** slippage estimate independent of OKX's demo behaviour.

### Integration points

- **Pre-trade check** in `Orchestrator.ProcessTrade`: compute `expectedFill` before approving, reject if slippage > threshold.
- **Post-trade audit**: compare `expectedFill(real book at order time)` vs actual `fill_avg` to measure market impact over time.
- The `exchange.L2Snapshot` type (top-5) already flows into the runtime via `UpdateL2`. For deep orders, fetch deeper book via REST before placing.

---

## Notes

- `ex.Client.AddBookHandler` (books5, 100ms) provides real-time top-5 for small orders.
- **books5 is sufficient for this use case.** Paper account capital is retail-scale (~$75k);
  orders of this size do not pierce L2 level 2–3 on blue-chip pairs (BTC, ETH, SOL, TRX…).
  No need to fetch deeper REST book at order time.
- Deeper book fetch (`sz=20/400`) is deferred — only relevant if account capital grows to
  institutional size or strategy targets thin mid-cap pairs.

## Deferred

- Implement `SweptVWAP(side, qty, L2Snapshot)` + integrate into `Orchestrator.ProcessTrade`
  as a pre-trade slippage gate.
- **Priority now: connectivity and correctness** (WS lifecycle, fill streaming, order state
  reconciliation) before adding slippage simulation logic.
