# Orchestrator Runtime Event Bus

## Overview

Mỗi `Orchestrator` instance có một **event channel** duy nhất. Tất cả runtime events
(order placed, filled, canceled, bot lifecycle, risk) đều được enqueue vào channel này.
Một drain goroutine duy nhất đọc channel và fan-out sang hai sink:

```
runtime event producers
  PlaceOrder()  ──┐
  applyFill()   ──┤
  bot.Start()   ──┤──► eventCh (buffered)
  bot.Stop()    ──┤        │
  RiskHalt()    ──┘        │
                      drain goroutine
                      ├─ slog        (developer observability)
                      └─ JetStream   (behavioral audit)
```

Producer không biết sink — chỉ enqueue và tiếp tục. Nếu channel đầy, event bị drop
và một slog.Warn được ghi (không block critical path).

---

## Event Types

```go
type EventType string

const (
    // Order lifecycle
    EventOrderPlaced   EventType = "order.placed"
    EventOrderFilled   EventType = "order.filled"
    EventOrderCanceled EventType = "order.canceled"
    EventOrderFailed   EventType = "order.failed"   // exchange rejected

    // Bot lifecycle
    EventBotStarted EventType = "bot.started"
    EventBotStopped EventType = "bot.stopped"
    EventBotPaused  EventType = "bot.paused"
    EventBotResumed EventType = "bot.resumed"
    EventBotKilled  EventType = "bot.killed"

    // Risk
    EventRiskHalted  EventType = "risk.halted"
    EventRiskResumed EventType = "risk.resumed"

    // Sync
    EventPortfolioSynced EventType = "portfolio.synced"
)

type RuntimeEvent struct {
    Type           EventType
    OrchestratorID string
    AccountID      string
    BotID          string    // empty for orchestrator-level events
    Symbol         string
    OccurredAt     time.Time
    Payload        any       // typed per EventType, see below
}
```

### Payloads

```go
type OrderPlacedPayload struct {
    OrderID  string
    Side     string          // "buy" | "sell"
    OrdType  string          // "market" | "limit"
    Qty      decimal.Decimal
    Price    decimal.Decimal // 0 for market orders
}

type OrderFilledPayload struct {
    OrderID   string
    Side      string
    Qty       decimal.Decimal
    AvgPrice  decimal.Decimal
    Fee       decimal.Decimal
    Source    string          // "ws" | "poll"
}

type OrderCanceledPayload struct {
    OrderID string
    Reason  string
}

type OrderFailedPayload struct {
    OrderID string
    Reason  string
    Code    string // exchange error code
}

type BotLifecyclePayload struct {
    Strategy string
    Symbols  []string
}

type RiskPayload struct {
    Reason string
}

type PortfolioSyncedPayload struct {
    Cash      decimal.Decimal
    Equity    decimal.Decimal
    Positions int
    NewFills  int
}
```

---

## slog Routing

Drain goroutine maps EventType → slog level + message:

| EventType | Level | Notes |
|---|---|---|
| `order.placed` | Info | symbol, side, qty, price |
| `order.filled` | Info | avg_price, fee, source |
| `order.canceled` | Warn | reason |
| `order.failed` | Error | reason, exchange code |
| `bot.started` | Info | strategy, symbols |
| `bot.stopped` | Info | — |
| `bot.paused` | Info | — |
| `bot.resumed` | Info | — |
| `bot.killed` | Warn | — |
| `risk.halted` | Warn | reason |
| `risk.resumed` | Info | — |
| `portfolio.synced` | Debug | cash, equity, positions, new_fills |

Tick/L2 events **không** di qua event bus — quá nhiều (10/s per symbol), chỉ log ở
`slog.Debug` trực tiếp tại điểm phát sinh khi cần debug.

---

## JetStream Routing

Chỉ **business events** được push lên JetStream — không push noise debug.

### Subjects

```
orch.events.{orchestratorID}.order.placed
orch.events.{orchestratorID}.order.filled
orch.events.{orchestratorID}.order.canceled
orch.events.{orchestratorID}.order.failed
orch.events.{orchestratorID}.bot.started
orch.events.{orchestratorID}.bot.stopped
orch.events.{orchestratorID}.bot.killed
orch.events.{orchestratorID}.risk.halted
orch.events.{orchestratorID}.risk.resumed
```

`portfolio.synced` và `bot.paused/resumed` không push JetStream — operational noise,
không bernilai audit.

### Stream config (gợi ý)

```
Stream name : ORCH_EVENTS
Subjects    : orch.events.>
Retention   : limits (30 days hoặc max size)
Storage     : file
Replicas    : 1 (dev) / 3 (prod)
Dedup window: 2 minutes (Nats-Msg-Id = "{orchID}-{eventType}-{orderID}")
```

### Consumers hiện tại / tương lai

| Consumer | Subject filter | Mục đích |
|---|---|---|
| investment service | `orch.events.*.order.filled` | Thay `investment.transactions.{accountID}` hiện tại |
| strategist | `orch.events.{orchID}.>` | Telegram notifications, AI context |
| order log | `orch.events.*.order.*` | Persist order audit trail |
| risk monitor | `orch.events.*.risk.*` | Alert khi risk halted |

---

## Migration từ fillCh

Hiện tại `Orchestrator.fillCh` làm việc tương tự nhưng chỉ cho fills. Kế hoạch:

1. Đổi tên `fillCh` → `eventCh chan RuntimeEvent`
2. `EnqueueFill()` → `EnqueueEvent(RuntimeEvent{Type: EventOrderFilled, ...})`
3. `runFillProcessor()` → `runEventProcessor()` với fan-out logic
4. Các nơi gọi `slog.Info("order placed", ...)` trực tiếp → enqueue event thay thế

---

## Resilience & Durability

Event bus **không phải WAL** — in-memory channel mất khi crash. Trade-off chấp nhận được vì:

- **Exchange là source of truth.** Mọi fill và order state đều query lại được qua REST.
- **Startup reconciliation** (đã có một phần, cần hoàn thiện):
  - `SyncAccount()` → recover fills từ exchange sau crash
  - `GetOrders()` → reconcile pending orders (TODO)
- **Miss window nhỏ** — chỉ events trong channel lúc crash bị mất, thường vài giây.
- JetStream audit trail vẫn đủ cho business reporting, chỉ thiếu window crash nhỏ.

Để làm WAL thật sự cần publish JetStream synchronous trước action → thêm latency vào
hot path, không worth it ở scale này.

**Kết luận:** eventual consistency + startup reconciliation, không cần strong durability.

---

## Non-goals

- **Không replace slog hoàn toàn** — slog vẫn dùng cho errors, warnings, startup logs,
  những thứ không phải runtime events.
- **Không buffer lại nếu NATS down** — JetStream publish là best-effort, nếu fail thì
  slog.Warn. Critical audit (fills) đã có dedup qua `investment.transactions` riêng.
- **Không dùng cho tick/L2** — quá high-frequency, không có business value khi audit.
