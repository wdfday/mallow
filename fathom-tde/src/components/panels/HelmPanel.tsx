import { useEffect, useState } from 'react'
import {
  Ban,
  ChevronRight,
  Loader2,
  Pause,
  Play,
  Power,
  RotateCcw,
  Square,
  Unplug,
} from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAuth } from '@/lib/auth-context'
import { useHandView } from '@/lib/hand-view-context'

// Viewing tabs (Portfolio/Trades/Orders) below verified against
// helm/internal/module/helm/dto/{portfolio,order}.go directly:
//   - PortfolioResp is a flat snapshot (not paginated), embeds Positions[] itself — no separate
//     /positions call needed, Portfolio tab covers both.
//   - TradesResp IS cursor-paginated ({trades, next, has_more, limit} — `next` is an opaque
//     string cursor, RFC3339 exit_at when Postgres-backed).
//   - OrdersResp (GET /orders, live in-memory) is a flat array, NOT paginated, unlike order
//     *history* (a separate endpoint this pass doesn't cover).

// Real business console, not local-first: this panel is fathom's window into the same helm
// service mallow-client's dashboard manages, called directly through the gateway (REST, Bearer
// token from useAuth().apiFetch) — no herald/alm-engine involved. Shapes verified against
// helm/internal/module/{helm,hand}/{handler,dto,domain}/*.go, not guessed:
//   - helm actions: enable/disable/pause/resume + halt/reset — there is NO `kill` action for
//     helms (only hands have kill). Halting itself only ever happens automatically (risk
//     circuit-breaker) — the only manual lever on a halted helm is halt/reset.
//   - hands are nested under a helm (`/api/v1/hands/{helmId}/{id}/...`), not flat by hand id.
//   - decimal fields (allocated_capital, total_pnl, …) serialize as JSON strings — parse before
//     formatting, never treat as number directly.

interface RiskConfigDTO {
  max_positions?: number
  daily_loss_limit_pct?: number
  max_drawdown_pct?: number
  max_gross_exposure_pct?: number
}

type HelmStatus = 'active' | 'paused' | 'halted' | 'disabled' | 'error'

interface HelmResp {
  id: string
  account_id: string
  name: string
  broker_type: string
  account_type: string
  risk: RiskConfigDTO
  status: HelmStatus
  created_at: string
  updated_at: string
}

type HandStatus = 'stopped' | 'running' | 'killed' | 'released'

interface HandMetricsView {
  signals_received: number
  signals_filtered: number
  signals_dropped: number
  trades_approved: number
  orders_placed: number
  orders_filled: number
  orders_failed: number
  total_pnl: string
  win_count: number
  loss_count: number
}

interface HandHealthView {
  status: string
  last_error?: string
  uptime?: string
}

interface HandSummary {
  id: string
  name: string
  type: string
  market: string
  helm_id: string
  strategy: { script: string; timeframe: string; min_strength?: number }
  symbols: string[]
  status: HandStatus
  running: boolean
  order_count: number
  health: HandHealthView
  metrics: HandMetricsView
  created_at: string
  allocated_capital: string
  deployed_capital: string
  available_cash: string
}

interface HelmDetailResp extends HelmResp {
  hands: HandSummary[]
  running: boolean
  paused: boolean
  halted: boolean
  last_sync_at?: string
}

const HELM_STATUS_TONE: Record<HelmStatus, string> = {
  active: 'text-emerald-500 bg-emerald-500/10',
  paused: 'text-amber-500 bg-amber-500/10',
  halted: 'text-destructive bg-destructive/10',
  disabled: 'text-muted-foreground bg-muted/30',
  error: 'text-destructive bg-destructive/10',
}

const HAND_STATUS_TONE: Record<HandStatus, string> = {
  running: 'text-emerald-500 bg-emerald-500/10',
  stopped: 'text-muted-foreground bg-muted/30',
  killed: 'text-destructive bg-destructive/10',
  released: 'text-amber-500 bg-amber-500/10',
}

function StatusBadge({ status, tone }: { status: string; tone: string }) {
  return <span className={cn('rounded px-1.5 py-0.5 text-[10px] font-medium capitalize', tone)}>{status}</span>
}

function fmtUsd(v: string | undefined): string {
  const n = v === undefined ? NaN : parseFloat(v)
  if (Number.isNaN(n)) return '—'
  return n.toLocaleString('en-US', { maximumFractionDigits: 2 })
}

function ActionBtn({
  icon: Icon,
  label,
  onClick,
  busy,
  destructive,
}: {
  icon: typeof Play
  label: string
  onClick: () => void
  busy: boolean
  destructive?: boolean
}) {
  return (
    <button
      onClick={onClick}
      disabled={busy}
      className={cn(
        'flex items-center gap-1 rounded-md border px-1.5 py-1 text-[10px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-40',
        destructive
          ? 'border-destructive/30 text-destructive hover:bg-destructive/10'
          : 'border-border text-foreground/70 hover:border-secondary/50 hover:text-secondary',
      )}
    >
      {busy ? <Loader2 className="h-3 w-3 animate-spin" /> : <Icon className="h-3 w-3" />}
      {label}
    </button>
  )
}

function AllocateCapital({ onSubmit, busy }: { onSubmit: (delta: number) => void; busy: boolean }) {
  const [value, setValue] = useState('')
  return (
    <div className="flex items-center gap-1">
      <input
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder="+/- amount"
        className="h-6 w-20 rounded border border-border bg-background px-1 text-[10px] outline-none focus:border-secondary/50"
      />
      <button
        onClick={() => {
          const n = parseFloat(value)
          if (!Number.isNaN(n) && n !== 0) { onSubmit(n); setValue('') }
        }}
        disabled={busy || !value.trim()}
        className="h-6 rounded border border-border px-1.5 text-[10px] text-foreground/70 hover:border-secondary/50 hover:text-secondary disabled:opacity-40"
      >
        Allocate
      </button>
    </div>
  )
}

function HandRow({
  hand,
  onAction,
  onAllocate,
  onOpen,
  busyAction,
}: {
  hand: HandSummary
  onAction: (handId: string, action: 'start' | 'stop' | 'kill' | 'release') => void
  onAllocate: (handId: string, delta: number) => void
  /** Opens the dedicated Hand dock panel (Overview/Activity/actions) + syncs Chart to this
   *  hand's market — see hand-view-context.tsx. */
  onOpen: () => void
  busyAction: string | null
}) {
  const terminal = hand.status === 'killed' || hand.status === 'released'
  const pnl = parseFloat(hand.metrics.total_pnl)
  return (
    <div className="border-b border-border/60 px-2 py-2">
      <button onClick={onOpen} className="flex w-full items-center gap-1.5 text-left" title="Open hand view">
        <span className="min-w-0 flex-1 truncate text-[12px] font-medium hover:text-secondary">{hand.name}</span>
        <StatusBadge status={hand.status} tone={HAND_STATUS_TONE[hand.status]} />
      </button>
      <div className="mt-0.5 flex flex-wrap gap-1">
        {hand.symbols.map((s) => (
          <span key={s} className="rounded bg-muted/40 px-1 py-0.5 text-[9px] text-muted-foreground">{s}</span>
        ))}
        <span className="rounded bg-muted/40 px-1 py-0.5 text-[9px] text-muted-foreground">{hand.strategy.timeframe}</span>
      </div>
      <div className="mt-1 grid grid-cols-3 gap-1 text-[10px] text-muted-foreground">
        <span>Alloc: <span className="text-foreground">{fmtUsd(hand.allocated_capital)}</span></span>
        <span>Deployed: <span className="text-foreground">{fmtUsd(hand.deployed_capital)}</span></span>
        <span className={cn(!Number.isNaN(pnl) && (pnl >= 0 ? 'text-emerald-500' : 'text-red-500'))}>
          PnL: {Number.isNaN(pnl) ? '—' : fmtUsd(hand.metrics.total_pnl)}
        </span>
      </div>
      {hand.health.last_error && (
        <p className="mt-1 truncate text-[10px] text-destructive" title={hand.health.last_error}>
          {hand.health.last_error}
        </p>
      )}
      {!terminal && (
        <div className="mt-1.5 flex flex-wrap items-center gap-1">
          {hand.status === 'stopped' ? (
            <ActionBtn icon={Play} label="Start" busy={busyAction === `${hand.id}:start`} onClick={() => onAction(hand.id, 'start')} />
          ) : (
            <ActionBtn icon={Square} label="Stop" busy={busyAction === `${hand.id}:stop`} onClick={() => onAction(hand.id, 'stop')} />
          )}
          <ActionBtn icon={Ban} label="Kill" destructive busy={busyAction === `${hand.id}:kill`} onClick={() => onAction(hand.id, 'kill')} />
          <ActionBtn icon={Unplug} label="Release" destructive busy={busyAction === `${hand.id}:release`} onClick={() => onAction(hand.id, 'release')} />
          <AllocateCapital busy={busyAction === `${hand.id}:allocate`} onSubmit={(delta) => onAllocate(hand.id, delta)} />
        </div>
      )}
    </div>
  )
}

interface PositionResp {
  symbol: string
  qty: string
  avg_price: string
  current_price: string
  unrealized_pnl: string
  market_value: string
  entry_time: string
}

interface PortfolioResp {
  initial_capital: string
  cash: string
  equity: string
  deployed_capital: string
  unrealized_pnl: string
  realized_pnl: string
  allocated_to_hands: string
  unallocated_capital: string
  total_return_pct: number
  current_drawdown_pct: number
  max_drawdown_pct: number
  win_rate_pct: number
  total_trades: number
  open_positions: number
  daily_pnl: string
  positions: PositionResp[]
}

interface TradeResp {
  id?: string
  hand_id?: string
  symbol: string
  side: string
  qty: string
  entry_price: string
  exit_price: string
  entry_time: string
  exit_time: string
  net_pnl: string
  pnl_pct: string
  exit_reason?: string
  strategy?: string
}

interface TradesPageResp {
  trades: TradeResp[]
  next?: string
  has_more: boolean
  limit: number
}

interface OrderResp {
  id: string
  hand_id: string
  symbol: string
  side: string
  qty: string
  type: string
  status: string
  filled_qty: string
  filled_avg_price: string
  submitted_at: string
}

function fmtPct(v: number | undefined): string {
  return typeof v === 'number' ? `${v >= 0 ? '+' : ''}${v.toFixed(2)}%` : '—'
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'pos' | 'neg' }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-md border border-border bg-muted/20 px-2 py-1.5">
      <span className="text-[9px] uppercase tracking-wider text-muted-foreground">{label}</span>
      <span className={cn('font-mono text-[12px] font-semibold tabular-nums', tone === 'pos' && 'text-emerald-500', tone === 'neg' && 'text-red-500')}>
        {value}
      </span>
    </div>
  )
}

function PortfolioTab({ helmId }: { helmId: string }) {
  const { apiFetch } = useAuth()
  const [data, setData] = useState<PortfolioResp | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    apiFetch<PortfolioResp>(`/api/v1/helms/${helmId}/portfolio`)
      .then(setData)
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
  }, [helmId]) // eslint-disable-line react-hooks/exhaustive-deps

  if (error) return <p className="p-3 text-center text-[11px] text-destructive">{error}</p>
  if (!data) return <div className="flex flex-1 items-center justify-center p-4"><Loader2 className="h-4 w-4 animate-spin text-muted-foreground" /></div>

  const unrealized = parseFloat(data.unrealized_pnl)
  const realized = parseFloat(data.realized_pnl)

  return (
    <div className="min-h-0 flex-1 overflow-y-auto p-2.5">
      <div className="grid grid-cols-2 gap-1.5">
        <Stat label="Equity" value={fmtUsd(data.equity)} />
        <Stat label="Cash" value={fmtUsd(data.cash)} />
        <Stat label="Unrealized PnL" value={fmtUsd(data.unrealized_pnl)} tone={unrealized >= 0 ? 'pos' : 'neg'} />
        <Stat label="Realized PnL" value={fmtUsd(data.realized_pnl)} tone={realized >= 0 ? 'pos' : 'neg'} />
        <Stat label="Return" value={fmtPct(data.total_return_pct)} tone={data.total_return_pct >= 0 ? 'pos' : 'neg'} />
        <Stat label="Max DD" value={fmtPct(-Math.abs(data.max_drawdown_pct))} tone="neg" />
        <Stat label="Win Rate" value={fmtPct(data.win_rate_pct)} />
        <Stat label="Trades" value={String(data.total_trades)} />
      </div>
      <p className="mb-1.5 mt-3 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
        Positions ({data.positions.length})
      </p>
      {data.positions.length === 0 ? (
        <p className="text-center text-[11px] italic text-muted-foreground">No open positions</p>
      ) : (
        <table className="w-full text-left text-[10px]">
          <thead>
            <tr className="text-muted-foreground">
              <th className="pb-1 font-medium">Symbol</th>
              <th className="pb-1 font-medium">Qty</th>
              <th className="pb-1 font-medium">Avg</th>
              <th className="pb-1 font-medium">Current</th>
              <th className="pb-1 text-right font-medium">PnL</th>
            </tr>
          </thead>
          <tbody>
            {data.positions.map((p) => {
              const pnl = parseFloat(p.unrealized_pnl)
              return (
                <tr key={p.symbol} className="border-t border-border/40">
                  <td className="py-1 font-mono">{p.symbol}</td>
                  <td className="py-1 font-mono">{p.qty}</td>
                  <td className="py-1 font-mono">{fmtUsd(p.avg_price)}</td>
                  <td className="py-1 font-mono">{fmtUsd(p.current_price)}</td>
                  <td className={cn('py-1 text-right font-mono', pnl >= 0 ? 'text-emerald-500' : 'text-red-500')}>{fmtUsd(p.unrealized_pnl)}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
    </div>
  )
}

function TradesTab({ helmId }: { helmId: string }) {
  const { apiFetch } = useAuth()
  const [trades, setTrades] = useState<TradeResp[]>([])
  const [cursor, setCursor] = useState<string | undefined>(undefined)
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  function load(before?: string) {
    setLoading(true)
    const qs = new URLSearchParams({ limit: '50', ...(before ? { before } : {}) })
    apiFetch<TradesPageResp>(`/api/v1/helms/${helmId}/trades?${qs}`)
      .then((r) => {
        setTrades((prev) => (before ? [...prev, ...r.trades] : r.trades))
        setCursor(r.next)
        setHasMore(r.has_more)
      })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [helmId]) // eslint-disable-line react-hooks/exhaustive-deps

  if (error) return <p className="p-3 text-center text-[11px] text-destructive">{error}</p>
  if (loading && trades.length === 0) {
    return <div className="flex flex-1 items-center justify-center p-4"><Loader2 className="h-4 w-4 animate-spin text-muted-foreground" /></div>
  }
  if (trades.length === 0) return <p className="p-3 text-center text-[11px] italic text-muted-foreground">No closed trades yet</p>

  return (
    <div className="min-h-0 flex-1 overflow-y-auto p-2.5">
      <table className="w-full text-left text-[10px]">
        <thead>
          <tr className="text-muted-foreground">
            <th className="pb-1 font-medium">Symbol</th>
            <th className="pb-1 font-medium">Side</th>
            <th className="pb-1 font-medium">Entry</th>
            <th className="pb-1 font-medium">Exit</th>
            <th className="pb-1 text-right font-medium">PnL</th>
            <th className="pb-1 text-right font-medium">PnL%</th>
          </tr>
        </thead>
        <tbody>
          {trades.map((t, i) => {
            const pnl = parseFloat(t.net_pnl)
            const pnlPct = parseFloat(t.pnl_pct)
            return (
              <tr key={t.id ?? i} className="border-t border-border/40">
                <td className="py-1 font-mono">{t.symbol}</td>
                <td className="py-1 font-mono capitalize">{t.side}</td>
                <td className="py-1 font-mono">{fmtUsd(t.entry_price)}</td>
                <td className="py-1 font-mono">{fmtUsd(t.exit_price)}</td>
                <td className={cn('py-1 text-right font-mono', pnl >= 0 ? 'text-emerald-500' : 'text-red-500')}>{fmtUsd(t.net_pnl)}</td>
                <td className={cn('py-1 text-right font-mono', pnlPct >= 0 ? 'text-emerald-500' : 'text-red-500')}>
                  {Number.isNaN(pnlPct) ? '—' : `${pnlPct >= 0 ? '+' : ''}${pnlPct.toFixed(2)}%`}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      {hasMore && (
        <button
          onClick={() => load(cursor)}
          disabled={loading}
          className="mt-2 w-full rounded-md border border-border py-1 text-[10px] text-muted-foreground hover:border-secondary/50 hover:text-secondary disabled:opacity-40"
        >
          {loading ? 'Loading…' : 'Load more'}
        </button>
      )}
    </div>
  )
}

function OrdersTab({ helmId }: { helmId: string }) {
  const { apiFetch } = useAuth()
  const [orders, setOrders] = useState<OrderResp[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    apiFetch<OrderResp[]>(`/api/v1/helms/${helmId}/orders`)
      // Confirmed live: the envelope's `data` key is omitted entirely (not `[]`) when the backend
      // array is empty — Go's `omitempty` on a bare-slice `Data` field treats zero-length as
      // "empty" and drops it, unlike a struct-wrapped list (e.g. trades/broker-connections),
      // which always serializes. `?? []` covers the "no orders right now" case, the common one.
      .then((r) => setOrders(r ?? []))
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
  }, [helmId]) // eslint-disable-line react-hooks/exhaustive-deps

  if (error) return <p className="p-3 text-center text-[11px] text-destructive">{error}</p>
  if (!orders) return <div className="flex flex-1 items-center justify-center p-4"><Loader2 className="h-4 w-4 animate-spin text-muted-foreground" /></div>
  if (orders.length === 0) return <p className="p-3 text-center text-[11px] italic text-muted-foreground">No open or recent orders</p>

  return (
    <div className="min-h-0 flex-1 overflow-y-auto p-2.5">
      <table className="w-full text-left text-[10px]">
        <thead>
          <tr className="text-muted-foreground">
            <th className="pb-1 font-medium">Symbol</th>
            <th className="pb-1 font-medium">Side</th>
            <th className="pb-1 font-medium">Type</th>
            <th className="pb-1 font-medium">Status</th>
            <th className="pb-1 text-right font-medium">Filled</th>
          </tr>
        </thead>
        <tbody>
          {orders.map((o) => (
            <tr key={o.id} className="border-t border-border/40">
              <td className="py-1 font-mono">{o.symbol}</td>
              <td className="py-1 font-mono capitalize">{o.side}</td>
              <td className="py-1 font-mono">{o.type}</td>
              <td className="py-1 font-mono">{o.status}</td>
              <td className="py-1 text-right font-mono">{o.filled_qty} @ {fmtUsd(o.filled_avg_price)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

type HelmTab = 'hands' | 'portfolio' | 'trades' | 'orders'
const HELM_TABS: { id: HelmTab; label: string }[] = [
  { id: 'hands', label: 'Hands' },
  { id: 'portfolio', label: 'Portfolio' },
  { id: 'trades', label: 'Trades' },
  { id: 'orders', label: 'Orders' },
]

function HelmDetail({ helmId }: { helmId: string }) {
  const { apiFetch } = useAuth()
  const { openHand } = useHandView()
  const [detail, setDetail] = useState<HelmDetailResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busyAction, setBusyAction] = useState<string | null>(null)
  const [tab, setTab] = useState<HelmTab>('hands')

  function refresh() {
    setLoading(true)
    apiFetch<HelmDetailResp>(`/api/v1/helms/${helmId}`)
      .then((d) => { setDetail(d); setError(null) })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setLoading(false))
  }

  useEffect(refresh, [helmId])

  async function runHelmAction(action: 'enable' | 'disable' | 'pause' | 'resume' | 'halt/reset') {
    setBusyAction(`helm:${action}`)
    try {
      await apiFetch(`/api/v1/helms/${helmId}/${action}`, { method: 'POST' })
      refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusyAction(null)
    }
  }

  async function runHandAction(handId: string, action: 'start' | 'stop' | 'kill' | 'release') {
    setBusyAction(`${handId}:${action}`)
    try {
      await apiFetch(`/api/v1/hands/${helmId}/${handId}/${action}`, { method: 'POST' })
      refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusyAction(null)
    }
  }

  async function allocateCapital(handId: string, delta: number) {
    setBusyAction(`${handId}:allocate`)
    try {
      await apiFetch(`/api/v1/hands/${helmId}/${handId}/allocate-capital`, {
        method: 'POST',
        body: JSON.stringify({ amount: delta }),
      })
      refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusyAction(null)
    }
  }

  if (loading && !detail) {
    return <div className="flex flex-1 items-center justify-center"><Loader2 className="h-4 w-4 animate-spin text-muted-foreground" /></div>
  }
  if (!detail) {
    return <div className="flex flex-1 items-center justify-center p-3 text-center text-xs text-destructive">{error ?? 'Failed to load helm'}</div>
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <div className="shrink-0 border-b border-border p-2.5">
        <div className="flex items-center gap-1.5">
          <span className="min-w-0 flex-1 truncate text-[13px] font-semibold">{detail.name}</span>
          <StatusBadge status={detail.status} tone={HELM_STATUS_TONE[detail.status]} />
        </div>
        <p className="mt-0.5 text-[10px] text-muted-foreground">{detail.broker_type} · {detail.account_type}</p>
        {error && <p className="mt-1 text-[10px] text-destructive">{error}</p>}
        <div className="mt-1.5 flex flex-wrap gap-1">
          {detail.status === 'active' && (
            <>
              <ActionBtn icon={Pause} label="Pause" busy={busyAction === 'helm:pause'} onClick={() => runHelmAction('pause')} />
              <ActionBtn icon={Power} label="Disable" destructive busy={busyAction === 'helm:disable'} onClick={() => runHelmAction('disable')} />
            </>
          )}
          {detail.status === 'paused' && (
            <>
              <ActionBtn icon={Play} label="Resume" busy={busyAction === 'helm:resume'} onClick={() => runHelmAction('resume')} />
              <ActionBtn icon={Power} label="Disable" destructive busy={busyAction === 'helm:disable'} onClick={() => runHelmAction('disable')} />
            </>
          )}
          {detail.status === 'disabled' && (
            <ActionBtn icon={Power} label="Enable" busy={busyAction === 'helm:enable'} onClick={() => runHelmAction('enable')} />
          )}
          {detail.status === 'halted' && (
            <ActionBtn icon={RotateCcw} label="Reset halt" busy={busyAction === 'helm:halt/reset'} onClick={() => runHelmAction('halt/reset')} />
          )}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1 border-b border-border px-2 py-1">
        {HELM_TABS.map((t) => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            className={cn(
              'rounded-md px-1.5 py-0.5 text-[10px] font-medium',
              tab === t.id ? 'bg-secondary/15 text-secondary' : 'text-muted-foreground hover:bg-muted/40',
            )}
          >
            {t.label}
          </button>
        ))}
      </div>
      {tab === 'hands' && (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <p className="border-b border-border/60 px-2.5 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
            Hands ({detail.hands.length})
          </p>
          {detail.hands.length === 0 ? (
            <p className="p-3 text-center text-[11px] italic text-muted-foreground">No hands yet</p>
          ) : (
            detail.hands.map((h) => (
              <HandRow
                key={h.id}
                hand={h}
                onAction={runHandAction}
                onAllocate={allocateCapital}
                busyAction={busyAction}
                onOpen={() => openHand({
                  helmId,
                  handId: h.id,
                  handName: h.name,
                  helmName: detail.name,
                  symbol: h.symbols[0],
                  timeframe: h.strategy.timeframe,
                })}
              />
            ))
          )}
        </div>
      )}
      {tab === 'portfolio' && <PortfolioTab helmId={helmId} />}
      {tab === 'trades' && <TradesTab helmId={helmId} />}
      {tab === 'orders' && <OrdersTab helmId={helmId} />}
    </div>
  )
}

export function HelmPanel() {
  const { apiFetch } = useAuth()
  const [helms, setHelms] = useState<HelmResp[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [selectedId, setSelectedId] = useState<string | null>(null)

  useEffect(() => {
    apiFetch<HelmResp[]>('/api/v1/helms')
      // Same omitted-`data`-on-empty-array quirk as OrdersTab below — a user with zero helms
      // gets no `data` key at all, not `[]`.
      .then((raw) => {
        const list = raw ?? []
        setHelms(list)
        if (list.length > 0) setSelectedId((prev) => prev ?? list[0].id)
      })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
    // eslint-disable-next-line react-hooks/exhaustive-deps -- fetch once on mount; apiFetch is stable enough per-session
  }, [])

  return (
    <div className="flex h-full min-w-0 overflow-hidden bg-background">
      <div className="flex w-40 shrink-0 flex-col overflow-y-auto border-r border-border">
        {helms === null && !error && (
          <div className="flex flex-1 items-center justify-center"><Loader2 className="h-4 w-4 animate-spin text-muted-foreground" /></div>
        )}
        {error && !helms && <p className="p-2 text-[11px] text-destructive">{error}</p>}
        {helms?.length === 0 && <p className="p-2 text-[11px] italic text-muted-foreground">No helms yet</p>}
        {helms?.map((h) => (
          <button
            key={h.id}
            onClick={() => setSelectedId(h.id)}
            className={cn(
              'flex items-center gap-1 border-b border-border/40 px-2 py-2 text-left transition-colors hover:bg-muted/30',
              selectedId === h.id && 'bg-secondary/10',
            )}
          >
            <ChevronRight className={cn('h-3 w-3 shrink-0 text-muted-foreground transition-transform', selectedId === h.id && 'rotate-90')} />
            <div className="min-w-0 flex-1">
              <p className="truncate text-[11px] font-medium">{h.name}</p>
              <StatusBadge status={h.status} tone={HELM_STATUS_TONE[h.status]} />
            </div>
          </button>
        ))}
      </div>
      {selectedId ? (
        <HelmDetail key={selectedId} helmId={selectedId} />
      ) : (
        <div className="flex flex-1 items-center justify-center p-3 text-center text-xs text-muted-foreground">
          {helms?.length === 0 ? 'No helms to manage.' : 'Select a helm on the left.'}
        </div>
      )}
    </div>
  )
}
