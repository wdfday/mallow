// Types + endpoint helpers for a single hand's detail/activity/trades/equity — shared between
// HelmPanel.tsx (hand list + row actions) and HandViewPanel.tsx (the dedicated dock panel opened
// by clicking a hand). Field names verified against mallow-client's own working client
// (service/helm/{hand.service,types}.ts) — the same shapes that app's shipped HandBottomView
// already renders successfully against the real helm API.
//
// Plain functions taking `apiFetch` as the first arg (not a class/hook) — this module has no
// React dependency, but the underlying fetch (useAuth().apiFetch) is a hook value, so callers
// pass it in rather than this module trying to be a hook itself.

type ApiFetch = <T>(path: string, init?: RequestInit) => Promise<T>

export interface HandPositionConfig {
  max_units?: number
  size_mode?: string
}

export interface HandRiskConfig {
  window_trades?: number
  max_consec_loss?: number
  max_total_loss_pct?: number
  max_avg_loss_pct?: number
  max_single_loss_pct?: number
}

export interface HandMetrics {
  signals_received: number
  signals_filtered?: number
  signals_dropped: number
  trades_approved: number
  orders_placed: number
  orders_filled: number
  orders_failed: number
  total_pnl: string
  total_commission?: string
  win_count: number
  loss_count: number
}

export interface HandHealth {
  status: string
  last_error?: string
  uptime?: string
}

export type HandStatus = 'stopped' | 'running' | 'killed' | 'released'

export interface HandDetail {
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
  health: HandHealth
  metrics: HandMetrics
  created_at: string
  allocated_capital: string
  deployed_capital: string
  available_cash: string
  position?: HandPositionConfig
  risk?: HandRiskConfig
}

export interface UpdateHandRequest {
  name?: string
  position?: HandPositionConfig
  risk?: HandRiskConfig
}

export interface ActivityEntry {
  at: string
  code: number
  symbol?: string
  direction?: string
  side?: string
  qty?: string
  price?: string
  order_id?: string
  reason?: string
  msg?: string
}

export interface HandTradeResp {
  id?: string
  hand_id?: string
  symbol: string
  side: string
  qty: string
  entry_price: string
  exit_price: string
  entry_time?: string
  exit_time?: string
  net_pnl: string
  pnl_pct: string
  exit_reason?: string
}

export interface TradesPageResp {
  trades: HandTradeResp[]
  next?: string
  has_more: boolean
  limit: number
}

/** One point per trade EXIT (event-driven, not time-bucketed) — server accumulates newest→oldest
 *  while walking trade history, capped at 5000 trades. `cum_pnl`/`cum_net_pnl` are decimal-as-
 *  string, same convention as every other decimal field here. NOT the {points,next,has_more}
 *  shape trades/activity use — this endpoint returns a bare array, no pagination envelope, no
 *  `after`/`before`/`resolution` params (only `period`, ignored for now — full history is small
 *  enough to fetch in one shot). */
export interface HandEquityPoint {
  ts: string
  cum_pnl: string
  cum_net_pnl: string
}

function qs(params?: Record<string, string | number | undefined>): string {
  if (!params) return ''
  const u = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined) u.set(k, String(v))
  }
  const s = u.toString()
  return s ? `?${s}` : ''
}

export function getHand(apiFetch: ApiFetch, helmId: string, id: string): Promise<HandDetail> {
  return apiFetch(`/api/v1/hands/${helmId}/${id}`)
}

/** `position`/`risk` are whole-object replaces server-side (pointers — nil leaves the field
 *  untouched, but any field OMITTED from a supplied object is set back to its zero value, not
 *  "left alone"). Callers must spread the hand's CURRENT sub-object before overriding just the
 *  edited keys — see HandViewPanel's config form, which only ever touches `risk` (a small,
 *  fully-known, all-plain-number object, safe to round-trip) and leaves `position`/sizing
 *  strictly read-only: `position`'s response shape has two decimal-as-string fields
 *  (`fixed_qty`/`fixed_quote_qty`) that the request DTO expects as plain numbers, so naively
 *  spreading the response object back into a request body would send the wrong JSON type. */
export function updateHand(apiFetch: ApiFetch, helmId: string, id: string, data: UpdateHandRequest): Promise<HandDetail> {
  return apiFetch(`/api/v1/hands/${helmId}/${id}`, { method: 'PUT', body: JSON.stringify(data) })
}

export function allocateHandCapital(apiFetch: ApiFetch, helmId: string, id: string, amount: number): Promise<HandDetail> {
  return apiFetch(`/api/v1/hands/${helmId}/${id}/allocate-capital`, { method: 'POST', body: JSON.stringify({ amount }) })
}

export function handAction(
  apiFetch: ApiFetch,
  helmId: string,
  id: string,
  action: 'start' | 'stop' | 'kill' | 'release',
): Promise<unknown> {
  return apiFetch(`/api/v1/hands/${helmId}/${id}/${action}`, { method: 'POST' })
}

/** Bare array, no pagination envelope (unlike trades) — callers track their own cursor by
 *  passing the oldest `at` seen so far as `before`, and infer `hasMore` from
 *  `page.length === limit`. */
export function getHandActivity(
  apiFetch: ApiFetch,
  helmId: string,
  id: string,
  params?: { before?: string; limit?: number },
): Promise<ActivityEntry[]> {
  return apiFetch<ActivityEntry[]>(`/api/v1/hands/${helmId}/${id}/activity${qs(params)}`)
    .then((r) => (Array.isArray(r) ? r : []))
}

export function getHandTrades(
  apiFetch: ApiFetch,
  helmId: string,
  id: string,
  params?: { before?: string; limit?: number },
): Promise<TradesPageResp> {
  return apiFetch(`/api/v1/hands/${helmId}/${id}/trades${qs(params)}`)
}

export function getHandEquity(apiFetch: ApiFetch, helmId: string, id: string): Promise<HandEquityPoint[]> {
  return apiFetch<HandEquityPoint[]>(`/api/v1/hands/${helmId}/${id}/equity`)
    .then((r) => (Array.isArray(r) ? r : []))
}
