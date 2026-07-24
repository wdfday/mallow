import { useEffect, useRef, useState } from 'react'
import { confirm } from '@tauri-apps/plugin-dialog'
import type { IDockviewPanelProps } from 'dockview-react'
import { Loader2, LogOut, Play, RefreshCw, Square, Zap } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useAuth } from '@/lib/auth-context'
import {
  allocateHandCapital,
  getHandActivity,
  getHandEquity,
  getHandTrades,
  handAction,
  updateHand,
  getHand,
  type ActivityEntry,
  type HandDetail,
  type HandTradeResp,
} from '@/lib/hand-api'
import { EquityCurve } from '@/components/ui/EquityCurve'

// Dock panel opened by clicking a hand in HelmPanel (see hand-view-context.tsx + DockArea's
// HandViewBridge) — the "bundle" mallow-client opens as a bottom tab on /strategy
// (components/helm/HandBottomView.tsx): live stats, equity curve, activity log, trades, and
// lifecycle actions for ONE hand. Ported to fathom's plainer style (no shadcn Tabs/AlertDialog —
// this app uses tauri's `confirm()` for destructive confirmations, matching Sidebar.tsx's delete
// flow) and scoped down in two ways from the original:
//   - No live WS tail — fathom has no realtime client yet (mallow-client's is gateway
//     `/api/v1/stream`, not built here). Activity/trades are fetch-once + manual "Load more".
//   - Sizing (`position`) is read-only. The response's `position` embeds two decimal-as-string
//     fields (`fixed_qty`/`fixed_quote_qty`) that the PUT request expects as plain numbers, and
//     the endpoint replaces the whole sub-object per request — spreading the response back in
//     would risk sending the wrong JSON type AND zeroing fields we don't understand. `risk` has
//     no such trap (small, fully-known, all-plain-number object) so only that + `name` are
//     editable here; capital changes go through the dedicated /allocate-capital endpoint.

interface HandViewPanelParams {
  helmId: string
  handId: string
  helmName: string
}

const STATUS_TONE: Record<string, string> = {
  running: 'text-emerald-500 bg-emerald-500/10',
  stopped: 'text-muted-foreground bg-muted/30',
  killed: 'text-destructive bg-destructive/10',
  released: 'text-amber-500 bg-amber-500/10',
}

// Numeric event codes verified against mallow-client's service/helm/types.ts (ActivityCode enum)
// + components/helm/shared.ts (ACTIVITY_CODE_META) — same wire format, ported to Tailwind classes
// already used elsewhere in fathom instead of that file's raw hex colors.
const ACTIVITY_CODE_META: Record<number, { label: string; tone: string }> = {
  10000: { label: 'Signal', tone: 'text-blue-400' },
  10001: { label: 'Stale', tone: 'text-muted-foreground' },
  10002: { label: 'Helm paused', tone: 'text-amber-500' },
  10004: { label: 'Rate limited', tone: 'text-amber-400' },
  10005: { label: 'No action', tone: 'text-muted-foreground' },
  10006: { label: 'Max units', tone: 'text-amber-400' },
  10007: { label: 'Rejected', tone: 'text-red-400' },
  10008: { label: 'No position', tone: 'text-muted-foreground' },
  10009: { label: 'Trade approved', tone: 'text-emerald-400' },
  10010: { label: 'Signal dropped', tone: 'text-muted-foreground' },
  10100: { label: 'Order placed', tone: 'text-emerald-400' },
  10101: { label: 'Order filled', tone: 'text-emerald-500' },
  10102: { label: 'Order failed', tone: 'text-red-500' },
  10103: { label: 'Partial cancel', tone: 'text-amber-400' },
  10104: { label: 'Limit timeout', tone: 'text-amber-400' },
  10106: { label: 'Limit → market', tone: 'text-amber-300' },
  10107: { label: 'Cancelled', tone: 'text-muted-foreground' },
  10108: { label: 'SL/TP triggered', tone: 'text-orange-400' },
  10109: { label: 'SL/TP placed', tone: 'text-amber-300' },
  10110: { label: 'Dust exit', tone: 'text-amber-400' },
  10111: { label: 'Exit failed', tone: 'text-red-500' },
  10200: { label: 'Auto stopped', tone: 'text-amber-400' },
  10201: { label: 'Started', tone: 'text-emerald-400' },
  10202: { label: 'Stopped', tone: 'text-muted-foreground' },
  10205: { label: 'Killed', tone: 'text-red-500' },
  10206: { label: 'Released', tone: 'text-orange-400' },
  10208: { label: 'Leverage set', tone: 'text-blue-400' },
  10209: { label: 'Ext. closed', tone: 'text-orange-500' },
  10300: { label: 'Helm paused', tone: 'text-amber-500' },
  10301: { label: 'Helm resumed', tone: 'text-emerald-400' },
  10302: { label: 'Synced', tone: 'text-blue-400' },
  10303: { label: 'Helm halted', tone: 'text-red-500' },
  10304: { label: 'Halt cleared', tone: 'text-emerald-400' },
  10305: { label: 'Cred error', tone: 'text-red-500' },
  10400: { label: 'Recon restored', tone: 'text-blue-400' },
  10401: { label: 'Recon fill applied', tone: 'text-emerald-400' },
  10402: { label: 'Recon cancelled', tone: 'text-muted-foreground' },
  10403: { label: 'Recon ext. close', tone: 'text-orange-400' },
  10404: { label: 'Recon failed', tone: 'text-red-500' },
  10405: { label: 'Recon complete', tone: 'text-blue-400' },
  10406: { label: 'Equity drift', tone: 'text-red-400' },
  10500: { label: 'Position opened', tone: 'text-emerald-500' },
  10501: { label: 'Position added', tone: 'text-emerald-500' },
  10502: { label: 'Position closed', tone: 'text-orange-500' },
  10503: { label: 'Entering', tone: 'text-amber-300' },
  10504: { label: 'Adding', tone: 'text-amber-300' },
  10505: { label: 'Enter cancelled', tone: 'text-muted-foreground' },
  10506: { label: 'Add cancelled', tone: 'text-muted-foreground' },
}

function fmtUsd(v: string | number | undefined): string {
  const n = typeof v === 'number' ? v : v === undefined ? NaN : parseFloat(v)
  if (Number.isNaN(n)) return '—'
  return `$${n.toLocaleString('en-US', { maximumFractionDigits: 2 })}`
}

function fmtPct(v: number | undefined): string {
  return typeof v === 'number' ? `${v >= 0 ? '+' : ''}${v.toFixed(2)}%` : '—'
}

function fmtTime(s: string | undefined): string {
  if (!s) return '—'
  const ms = Date.parse(s)
  return Number.isFinite(ms) ? new Date(ms).toLocaleString() : '—'
}

function StatusBadge({ status }: { status: string }) {
  return (
    <span className={cn('rounded px-1.5 py-0.5 text-[10px] font-medium capitalize', STATUS_TONE[status] ?? STATUS_TONE.stopped)}>
      {status}
    </span>
  )
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
        'flex items-center gap-1 rounded-md border px-2 py-1 text-[11px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-40',
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

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'pos' | 'neg' }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-md border border-border bg-muted/20 px-2.5 py-1.5">
      <span className="text-[9px] uppercase tracking-wider text-muted-foreground">{label}</span>
      <span className={cn('font-mono text-[13px] font-semibold tabular-nums', tone === 'pos' && 'text-emerald-500', tone === 'neg' && 'text-red-500')}>
        {value}
      </span>
    </div>
  )
}

function Row({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return (
    <div className="flex items-center gap-2 py-[3px]">
      <span className="w-28 shrink-0 text-[11px] text-muted-foreground">{label}</span>
      <span className={cn('text-[12px] font-semibold tabular-nums', tone ?? 'text-foreground')}>{value}</span>
    </div>
  )
}

// ── Overview tab ─────────────────────────────────────────────────────────────

function ConfigForm({ hand, onSaved }: { hand: HandDetail; onSaved: () => void }) {
  const { apiFetch } = useAuth()
  const [name, setName] = useState(hand.name)
  const [windowTrades, setWindowTrades] = useState(String(hand.risk?.window_trades ?? ''))
  const [maxConsecLoss, setMaxConsecLoss] = useState(String(hand.risk?.max_consec_loss ?? ''))
  const [maxTotalLoss, setMaxTotalLoss] = useState(hand.risk?.max_total_loss_pct !== undefined ? String(hand.risk.max_total_loss_pct * 100) : '')
  const [maxAvgLoss, setMaxAvgLoss] = useState(hand.risk?.max_avg_loss_pct !== undefined ? String(hand.risk.max_avg_loss_pct * 100) : '')
  const [maxSingleLoss, setMaxSingleLoss] = useState(hand.risk?.max_single_loss_pct !== undefined ? String(hand.risk.max_single_loss_pct * 100) : '')
  const [saving, setSaving] = useState(false)
  const [adjustAmt, setAdjustAmt] = useState('')
  const [adjusting, setAdjusting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleSave() {
    setSaving(true)
    setError(null)
    try {
      await updateHand(apiFetch, hand.helm_id, hand.id, {
        name: name || undefined,
        // Spread the CURRENT risk object first — this endpoint replaces the whole sub-object,
        // so an omitted field resets to zero, not "unchanged" (see hand-api.ts's updateHand doc).
        risk: {
          ...(hand.risk ?? {}),
          ...(windowTrades ? { window_trades: parseInt(windowTrades, 10) } : {}),
          ...(maxConsecLoss ? { max_consec_loss: parseInt(maxConsecLoss, 10) } : {}),
          ...(maxTotalLoss ? { max_total_loss_pct: parseFloat(maxTotalLoss) / 100 } : {}),
          ...(maxAvgLoss ? { max_avg_loss_pct: parseFloat(maxAvgLoss) / 100 } : {}),
          ...(maxSingleLoss ? { max_single_loss_pct: parseFloat(maxSingleLoss) / 100 } : {}),
        },
      })
      onSaved()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  async function handleAdjust() {
    const amt = parseFloat(adjustAmt)
    if (!Number.isFinite(amt) || amt === 0) return
    setAdjusting(true)
    setError(null)
    try {
      await allocateHandCapital(apiFetch, hand.helm_id, hand.id, amt)
      setAdjustAmt('')
      onSaved()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setAdjusting(false)
    }
  }

  const inputCls = 'h-6 w-full rounded border border-border bg-background px-1.5 text-[11px] outline-none focus:border-secondary/50'

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-2.5">
        <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Name</p>
        <input value={name} onChange={(e) => setName(e.target.value)} className={cn(inputCls, 'mb-3')} />

        <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Capital</p>
        <div className="mb-3 flex items-center gap-1.5">
          <span className="flex-1 text-[11px] text-muted-foreground">Allocated: <span className="text-foreground">{fmtUsd(hand.allocated_capital)}</span></span>
          <input value={adjustAmt} onChange={(e) => setAdjustAmt(e.target.value)} placeholder="+/- amount" className="h-6 w-24 rounded border border-border bg-background px-1.5 text-[11px] outline-none focus:border-secondary/50" />
          <button onClick={() => void handleAdjust()} disabled={adjusting || !adjustAmt.trim()} className="h-6 shrink-0 rounded border border-border px-1.5 text-[11px] text-foreground/70 hover:border-secondary/50 hover:text-secondary disabled:opacity-40">
            {adjusting ? '…' : 'Adjust'}
          </button>
        </div>

        <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Sizing (read-only)</p>
        <div className="mb-3 flex gap-1.5 text-[11px] text-muted-foreground">
          <span>Mode: <span className="text-foreground">{hand.position?.size_mode ?? '—'}</span></span>
          <span>Max units: <span className="text-foreground">{hand.position?.max_units ?? '—'}</span></span>
        </div>

        <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Risk</p>
        <div className="grid grid-cols-2 gap-1.5">
          <label className="flex flex-col gap-0.5">
            <span className="text-[10px] text-muted-foreground">Window trades</span>
            <input value={windowTrades} onChange={(e) => setWindowTrades(e.target.value)} placeholder="e.g. 20" className={inputCls} />
          </label>
          <label className="flex flex-col gap-0.5">
            <span className="text-[10px] text-muted-foreground">Max consec. loss</span>
            <input value={maxConsecLoss} onChange={(e) => setMaxConsecLoss(e.target.value)} placeholder="e.g. 5" className={inputCls} />
          </label>
          <label className="flex flex-col gap-0.5">
            <span className="text-[10px] text-muted-foreground">Max total %</span>
            <input value={maxTotalLoss} onChange={(e) => setMaxTotalLoss(e.target.value)} placeholder="e.g. 10" className={inputCls} />
          </label>
          <label className="flex flex-col gap-0.5">
            <span className="text-[10px] text-muted-foreground">Max avg %</span>
            <input value={maxAvgLoss} onChange={(e) => setMaxAvgLoss(e.target.value)} placeholder="e.g. 3" className={inputCls} />
          </label>
          <label className="flex flex-col gap-0.5">
            <span className="text-[10px] text-muted-foreground">Max single %</span>
            <input value={maxSingleLoss} onChange={(e) => setMaxSingleLoss(e.target.value)} placeholder="e.g. 5" className={inputCls} />
          </label>
        </div>
        {error && <p className="mt-2 text-[10px] text-destructive">{error}</p>}
      </div>
      <div className="shrink-0 border-t border-border px-3 py-2">
        <button onClick={() => void handleSave()} disabled={saving} className="h-7 rounded-md bg-secondary/15 px-3 text-[11px] font-medium text-secondary hover:bg-secondary/25 disabled:opacity-40">
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  )
}

function OverviewTab({ hand, onReload }: { hand: HandDetail; onReload: () => void }) {
  const { apiFetch } = useAuth()
  const [equity, setEquity] = useState<{ t: number; v: number }[]>([])

  useEffect(() => {
    getHandEquity(apiFetch, hand.helm_id, hand.id)
      .then((points) => {
        const sorted = [...points].sort((a, b) => a.ts.localeCompare(b.ts))
        setEquity(sorted.map((p) => ({ t: Date.parse(p.ts), v: parseFloat(p.cum_net_pnl) })))
      })
      .catch(() => setEquity([]))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hand.helm_id, hand.id])

  const grossPnl = parseFloat(hand.metrics.total_pnl)
  const commission = parseFloat(hand.metrics.total_commission ?? '0')
  const netPnl = grossPnl - commission
  const wins = hand.metrics.win_count
  const losses = hand.metrics.loss_count
  const tradeCount = wins + losses
  const winRate = tradeCount > 0 ? (wins / tradeCount) * 100 : 0
  const allocated = parseFloat(hand.allocated_capital)
  const deployed = parseFloat(hand.deployed_capital)
  const availCash = parseFloat(hand.available_cash)
  const handEquity = allocated + netPnl

  return (
    <div className="flex h-full overflow-hidden">
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden border-r border-border">
        <div className="grid shrink-0 grid-cols-3 gap-1.5 border-b border-border p-2">
          <Stat label="Net P&L" value={fmtUsd(netPnl)} tone={netPnl >= 0 ? 'pos' : 'neg'} />
          <Stat label="Win Rate" value={tradeCount ? fmtPct(winRate) : '—'} tone={winRate >= 50 ? 'pos' : 'neg'} />
          <Stat label="Equity" value={fmtUsd(handEquity)} />
        </div>
        <div className="grid min-h-0 flex-1 grid-cols-2 divide-x divide-border overflow-y-auto">
          <div className="px-3 py-2.5">
            <p className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60">Capital</p>
            <Row label="Allocated" value={fmtUsd(allocated)} />
            <Row label="Available cash" value={fmtUsd(availCash)} />
            <Row label="Deployed" value={fmtUsd(deployed)} />
            {commission > 0 && <Row label="Gross P&L" value={fmtUsd(grossPnl)} tone={grossPnl >= 0 ? 'text-emerald-500' : 'text-red-500'} />}
            {commission > 0 && <Row label="Fees" value={fmtUsd(commission)} tone="text-muted-foreground" />}
            <p className="mb-1 mt-3 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60">Trades</p>
            <Row label="Total" value={String(tradeCount)} />
            <Row label="Wins" value={String(wins)} tone="text-emerald-500" />
            <Row label="Losses" value={String(losses)} tone={losses > 0 ? 'text-red-400' : undefined} />
          </div>
          <div className="px-3 py-2.5">
            <p className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60">Signals</p>
            <Row label="Received" value={String(hand.metrics.signals_received)} />
            {(hand.metrics.signals_filtered ?? 0) > 0 && <Row label="Filtered" value={String(hand.metrics.signals_filtered)} tone="text-muted-foreground" />}
            <Row label="Executed" value={String(tradeCount)} tone={tradeCount > 0 ? 'text-emerald-500' : undefined} />
            <p className="mb-1 mt-3 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60">Cumulative net P&L</p>
            <div className="h-[110px]">
              {equity.length > 0 ? <EquityCurve points={equity} /> : (
                <div className="flex h-full items-center justify-center text-[11px] text-muted-foreground">No trades yet</div>
              )}
            </div>
          </div>
        </div>
      </div>
      <div className="w-[320px] shrink-0 overflow-hidden">
        <ConfigForm hand={hand} onSaved={onReload} />
      </div>
    </div>
  )
}

// ── Activity tab ─────────────────────────────────────────────────────────────

function ActivityTab({ helmId, handId }: { helmId: string; handId: string }) {
  const { apiFetch } = useAuth()
  const [entries, setEntries] = useState<ActivityEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [hasMore, setHasMore] = useState(false)
  const [trades, setTrades] = useState<HandTradeResp[]>([])
  const [tradesCursor, setTradesCursor] = useState<string | undefined>(undefined)
  const [tradesHasMore, setTradesHasMore] = useState(false)
  const [tradesLoading, setTradesLoading] = useState(true)
  const logRef = useRef<HTMLDivElement>(null)
  const hasAutoScrolled = useRef(false)
  const LIMIT = 100

  // Top-down chronological order (oldest at top, newest at bottom) — the API returns
  // newest-first, so this is a display-only sort; `entries` itself stays in fetch order so
  // "oldest loaded" for the next page is just entries[entries.length - 1] regardless.
  const sortedEntries = [...entries].sort((a, b) => a.at.localeCompare(b.at))

  useEffect(() => {
    setLoading(true)
    hasAutoScrolled.current = false
    getHandActivity(apiFetch, helmId, handId, { limit: LIMIT })
      .then((page) => { setEntries(page); setHasMore(page.length === LIMIT) })
      .finally(() => setLoading(false))
    setTradesLoading(true)
    getHandTrades(apiFetch, helmId, handId, { limit: 50 })
      .then((r) => { setTrades(r.trades ?? []); setTradesCursor(r.next); setTradesHasMore(r.has_more) })
      .finally(() => setTradesLoading(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [helmId, handId])

  // Reveal the newest entry (bottom of the top-down list) on first load only — loading older
  // history afterwards must not yank the view back down while the user is scrolled up reading it.
  useEffect(() => {
    if (!loading && sortedEntries.length > 0 && !hasAutoScrolled.current) {
      hasAutoScrolled.current = true
      logRef.current?.scrollTo({ top: logRef.current.scrollHeight })
    }
  }, [loading, sortedEntries.length])

  async function loadMoreActivity() {
    const oldest = entries[entries.length - 1]?.at
    if (!oldest) return
    setLoadingMore(true)
    const el = logRef.current
    const prevHeight = el?.scrollHeight ?? 0
    try {
      const page = await getHandActivity(apiFetch, helmId, handId, { before: oldest, limit: LIMIT })
      setEntries((prev) => [...prev, ...page])
      setHasMore(page.length === LIMIT)
      // Older entries render ABOVE the current view — keep the same row anchored instead of
      // letting the newly-taller content silently shift what's on screen.
      requestAnimationFrame(() => {
        if (el) el.scrollTop += el.scrollHeight - prevHeight
      })
    } finally {
      setLoadingMore(false)
    }
  }

  async function loadMoreTrades() {
    if (!tradesCursor) return
    setTradesLoading(true)
    try {
      const r = await getHandTrades(apiFetch, helmId, handId, { before: tradesCursor, limit: 50 })
      setTrades((prev) => [...prev, ...(r.trades ?? [])])
      setTradesCursor(r.next)
      setTradesHasMore(r.has_more)
    } finally {
      setTradesLoading(false)
    }
  }

  return (
    <div className="flex h-full divide-x divide-border overflow-hidden">
      <div ref={logRef} className="min-w-0 flex-1 overflow-y-auto font-mono text-[11px]">
        {loading ? (
          <div className="flex h-full items-center justify-center text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" /></div>
        ) : sortedEntries.length === 0 ? (
          <div className="flex h-full items-center justify-center text-muted-foreground">No activity yet</div>
        ) : (
          <div className="p-2">
            {/* Top-down chronological: oldest at top, newest at bottom (auto-revealed on first
                load) — "Load older" sits above the oldest row since older history prepends there,
                same convention as a terminal/console log. */}
            {hasMore && (
              <button onClick={() => void loadMoreActivity()} disabled={loadingMore} className="mb-2 w-full rounded-md border border-border py-1 text-[10px] text-muted-foreground hover:border-secondary/50 hover:text-secondary disabled:opacity-40">
                {loadingMore ? 'Loading…' : '↑ Load older'}
              </button>
            )}
            {sortedEntries.map((e, i) => {
              const meta = ACTIVITY_CODE_META[e.code] ?? { label: String(e.code), tone: 'text-muted-foreground' }
              const parts: string[] = []
              if (e.symbol) parts.push(e.symbol)
              if (e.direction) parts.push(e.direction.toUpperCase())
              if (e.side && e.qty) parts.push(`${e.side} ${e.qty}${e.price ? ` @ ${e.price}` : ''}`)
              if (e.reason) parts.push(`"${e.reason}"`)
              if (e.msg) parts.push(e.msg)
              return (
                <div key={i} className="flex gap-2 py-[1px] leading-5 hover:bg-muted/20">
                  <span className="shrink-0 text-muted-foreground/50">{fmtTime(e.at)}</span>
                  <span className={cn('w-32 shrink-0 text-right', meta.tone)}>{meta.label}</span>
                  <span className="min-w-0 truncate text-muted-foreground">{parts.join(' · ')}</span>
                </div>
              )
            })}
          </div>
        )}
      </div>
      <div className="min-w-0 flex-1 overflow-y-auto">
        {trades.length === 0 && !tradesLoading ? (
          <div className="flex h-full items-center justify-center text-[11px] text-muted-foreground">No trades yet</div>
        ) : (
          <table className="w-full text-left text-[10px]">
            <thead className="sticky top-0 bg-background">
              <tr className="text-muted-foreground">
                <th className="px-2 py-1.5 font-medium">Side</th>
                <th className="px-2 py-1.5 font-medium">Entry</th>
                <th className="px-2 py-1.5 font-medium">Exit</th>
                <th className="px-2 py-1.5 text-right font-medium">Net PnL</th>
                <th className="px-2 py-1.5 font-medium">Reason</th>
              </tr>
            </thead>
            <tbody>
              {trades.map((t, i) => {
                const pnl = parseFloat(t.net_pnl)
                return (
                  <tr key={t.id ?? i} className="border-t border-border/40">
                    <td className={cn('px-2 py-1 font-mono capitalize', t.side === 'buy' ? 'text-emerald-500' : 'text-red-400')}>{t.side}</td>
                    <td className="px-2 py-1 font-mono text-muted-foreground">{fmtTime(t.entry_time)}</td>
                    <td className="px-2 py-1 font-mono text-muted-foreground">{fmtTime(t.exit_time)}</td>
                    <td className={cn('px-2 py-1 text-right font-mono', pnl >= 0 ? 'text-emerald-500' : 'text-red-400')}>{fmtUsd(pnl)}</td>
                    <td className="px-2 py-1 capitalize text-muted-foreground">{t.exit_reason || '—'}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
        {tradesHasMore && (
          <button onClick={() => void loadMoreTrades()} disabled={tradesLoading} className="w-full py-1.5 text-[10px] text-muted-foreground hover:text-secondary disabled:opacity-40">
            {tradesLoading ? 'Loading…' : 'Load more'}
          </button>
        )}
      </div>
    </div>
  )
}

// ── Main panel ───────────────────────────────────────────────────────────────

export function HandViewPanel({ params }: Partial<IDockviewPanelProps<HandViewPanelParams>>) {
  const { apiFetch } = useAuth()
  const [hand, setHand] = useState<HandDetail | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<'overview' | 'activity'>('overview')
  const [busyAction, setBusyAction] = useState<string | null>(null)

  const helmId = params?.helmId
  const handId = params?.handId
  const helmName = params?.helmName ?? ''

  function refresh() {
    if (!helmId || !handId) return
    getHand(apiFetch, helmId, handId)
      .then((d) => { setHand(d); setError(null) })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
  }

  useEffect(refresh, [helmId, handId]) // eslint-disable-line react-hooks/exhaustive-deps

  async function runAction(action: 'start' | 'stop' | 'kill' | 'release') {
    if (!helmId || !handId) return
    if (action === 'kill' || action === 'release') {
      const ok = await confirm(
        action === 'kill'
          ? 'Kill this hand? This stops it and places MARKET close orders on all active positions.'
          : 'Release this hand? Positions stay open on the exchange but are disowned by fathom.',
        { title: action === 'kill' ? 'Confirm kill' : 'Confirm release', kind: 'warning' },
      )
      if (!ok) return
    }
    setBusyAction(action)
    try {
      await handAction(apiFetch, helmId, handId, action)
      refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusyAction(null)
    }
  }

  if (!helmId || !handId) return null

  if (!hand) {
    return (
      <div className="flex h-full items-center justify-center p-3 text-center text-xs text-destructive">
        {error ?? <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
      </div>
    )
  }

  return (
    <div className="flex h-full min-w-0 flex-col overflow-hidden bg-background">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-2.5">
        <span className="truncate text-[12px] font-semibold">{hand.name}</span>
        <span className="shrink-0 text-[11px] text-muted-foreground/70">{helmName}</span>
        <span className="shrink-0 font-mono text-[10px] text-muted-foreground/50">{hand.symbols.join(', ')}</span>
        {hand.strategy.timeframe && <span className="shrink-0 font-mono text-[10px] text-muted-foreground/40">{hand.strategy.timeframe}</span>}
        <StatusBadge status={hand.status} />
        <div className="flex shrink-0 items-center gap-1 rounded-md bg-muted/40 p-0.5">
          <button onClick={() => setTab('overview')} className={cn('rounded px-2 py-0.5 text-[10px] font-medium', tab === 'overview' ? 'bg-secondary/15 text-secondary' : 'text-muted-foreground hover:text-foreground')}>Overview</button>
          <button onClick={() => setTab('activity')} className={cn('rounded px-2 py-0.5 text-[10px] font-medium', tab === 'activity' ? 'bg-secondary/15 text-secondary' : 'text-muted-foreground hover:text-foreground')}>Activity</button>
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-1">
          {hand.status === 'stopped' && (
            <ActionBtn icon={Play} label="Start" busy={busyAction === 'start'} onClick={() => void runAction('start')} />
          )}
          {hand.status === 'running' && (
            <>
              <ActionBtn icon={Square} label="Stop" busy={busyAction === 'stop'} onClick={() => void runAction('stop')} />
              <ActionBtn icon={Zap} label="Kill" destructive busy={busyAction === 'kill'} onClick={() => void runAction('kill')} />
              <ActionBtn icon={LogOut} label="Release" destructive busy={busyAction === 'release'} onClick={() => void runAction('release')} />
            </>
          )}
          <button onClick={refresh} title="Refresh" className="rounded p-1 text-muted-foreground hover:text-foreground">
            <RefreshCw className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
      {error && <p className="shrink-0 border-b border-border px-2.5 py-1 text-[10px] text-destructive">{error}</p>}
      {hand.status === 'released' && (
        <div className="shrink-0 border-b border-amber-500/20 bg-amber-500/5 px-2.5 py-1 text-[10px] text-amber-500">
          Hand released — positions disowned and left active on the exchange.
        </div>
      )}
      <div className="min-h-0 flex-1 overflow-hidden">
        {tab === 'overview' ? <OverviewTab hand={hand} onReload={refresh} /> : <ActivityTab helmId={helmId} handId={handId} />}
      </div>
    </div>
  )
}
