import { invoke } from '@tauri-apps/api/core'
import { join } from '@tauri-apps/api/path'
import { exists, mkdir, writeTextFile } from '@tauri-apps/plugin-fs'

// Native backtest — invokes the Rust `backtest_run` command (alm_engine::backtest::run on a
// background thread): full local dataset via tiered resolution (project data/ → mounts →
// ~/Fathom/.data lake), real warm-up handling, MTF auto-detect. This replaced the WASM
// ChartState.backtest path, which was capped to whatever bars the chart happened to hold and
// had no warm-up; WASM stays for editor intelligence only (lint/completion — almanac-wasm.ts).
// Same function the agent's run_backtest tool calls — button and agent can never disagree.

export interface RunBacktestParams {
  projectPath: string
  script: string
  symbol: string
  timeframe?: string
  from?: string
  to?: string
  /** Extra ScriptBacktestRequest fields (snake_case, engine units) — see buildEngineConfig. */
  config?: Record<string, unknown>
}

// Typed to alm_engine::types::BacktestResponse's nested-group shape
// (engine/src/types/response.rs) — only the leaves the UI reads are spelled out; everything
// else stays open via the index signature so nothing gets silently dropped.
export interface CurvePoint {
  t: number
  v: number
}

export interface TradeResponse {
  symbol: string
  side: string
  qty: number
  entry_price: number
  exit_price: number
  entry_ts: number
  exit_ts: number
  entry_time: string
  exit_time: string
  pnl: number
  pnl_pct: number
  exit_reason: string
  [key: string]: unknown
}

export interface BacktestResponse {
  strategy: string
  symbol: string
  timeframe: string
  bar_count: number
  returns?: { total_return_pct?: number; [key: string]: unknown }
  risk_adjusted?: { sharpe_ratio?: number; sortino_ratio?: number; [key: string]: unknown }
  drawdown?: { max_drawdown_pct?: number; [key: string]: unknown }
  trade_stats?: { total?: number; win_rate_pct?: number; profit_factor?: number; [key: string]: unknown }
  curves?: { equity: CurvePoint[]; drawdown: CurvePoint[]; [key: string]: unknown }
  trades: TradeResponse[]
  [key: string]: unknown
}

export async function runBacktest(params: RunBacktestParams): Promise<BacktestResponse> {
  return invoke<BacktestResponse>('backtest_run', {
    projectPath: params.projectPath,
    script: params.script,
    symbol: params.symbol,
    timeframe: params.timeframe ?? null,
    from: params.from ?? null,
    to: params.to ?? null,
    config: params.config ?? null,
  })
}

interface SizingConfig {
  sizeMode?: string
  sizeValue?: number
  atrMultiplier?: number
  reversePolicy?: string
  strengthSizing?: boolean
}

/**
 * Merges the two config layers — GLOBAL defaults (Config tab / BottomRail) overridden by the
 * file's frontmatter (per-editor config rail) — and maps the result onto ScriptBacktestRequest
 * fields (snake_case, engine units). Percent-style UI values become the fractions the engine's
 * build_sizer expects; `size_value` fans out to the field its mode reads (engine_builder.rs:
 * fixed_qty→quantity, quote_qty→usd, volatility/fixed_fractional→risk, percent_equity→pct).
 *
 * `sizeValue`/`atrMultiplier` only inherit from global when the MODE is also inherited — a
 * file that overrides just the mode must not pick up a global value with different semantics
 * (e.g. global "percent_equity 10" leaking 10 USD into a file's "quote_qty").
 */
export function buildEngineConfig(
  meta: SizingConfig,
  globals: SizingConfig & { initialCapital?: number; commissionPct?: number; slippagePct?: number },
): Record<string, unknown> {
  const cfg: Record<string, unknown> = {}
  if (globals.initialCapital !== undefined) cfg.initial_capital = globals.initialCapital
  if (globals.commissionPct !== undefined) cfg.commission_pct = globals.commissionPct
  if (globals.slippagePct !== undefined) cfg.slippage_pct = globals.slippagePct

  const modeInherited = meta.sizeMode === undefined
  const sizing: SizingConfig = {
    sizeMode: meta.sizeMode ?? globals.sizeMode,
    sizeValue: meta.sizeValue ?? (modeInherited ? globals.sizeValue : undefined),
    atrMultiplier: meta.atrMultiplier ?? (modeInherited ? globals.atrMultiplier : undefined),
    reversePolicy: meta.reversePolicy ?? globals.reversePolicy,
    strengthSizing: meta.strengthSizing ?? globals.strengthSizing,
  }

  if (sizing.reversePolicy) cfg.reverse_policy = sizing.reversePolicy
  if (sizing.strengthSizing !== undefined) cfg.strength_sizing = sizing.strengthSizing
  if (sizing.sizeMode) {
    cfg.size_mode = sizing.sizeMode
    const v = sizing.sizeValue
    if (v !== undefined) {
      switch (sizing.sizeMode) {
        case 'fixed_qty': cfg.position_size_quantity = v; break
        case 'quote_qty': cfg.position_size_usd = v; break
        case 'volatility':
        case 'fixed_fractional': cfg.risk_per_trade_pct = v / 100; break
        case 'percent_equity': cfg.position_size_pct = v / 100; break
      }
    }
    if (sizing.sizeMode === 'volatility' && sizing.atrMultiplier !== undefined) {
      cfg.atr_multiplier = sizing.atrMultiplier
    }
  }
  return cfg
}

async function sha256Hex(text: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text))
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, '0')).join('')
}

/**
 * Persists a completed run into `{project}/output/backtests/{ts}-{symbol}.json` — request +
 * response + script hash, so results stay with the project (self-contained/audit-trail,
 * docs/VISION.md). Best-effort: a failed write logs but never fails the run that produced it.
 */
export async function persistBacktestRun(
  params: RunBacktestParams,
  response: BacktestResponse,
): Promise<void> {
  try {
    const dir = await join(params.projectPath, 'output', 'backtests')
    if (!(await exists(dir))) await mkdir(dir, { recursive: true })
    const stamp = new Date().toISOString().replace(/[:.]/g, '-')
    const record = {
      created_at: new Date().toISOString(),
      script_sha256: await sha256Hex(params.script),
      request: {
        symbol: params.symbol,
        timeframe: params.timeframe ?? null,
        from: params.from ?? null,
        to: params.to ?? null,
        config: params.config ?? null,
        script: params.script,
      },
      response,
    }
    await writeTextFile(
      await join(dir, `${stamp}-${params.symbol}.json`),
      JSON.stringify(record, null, 2),
    )
  } catch (err) {
    console.error('[backtest] persist to output/backtests failed:', err)
  }
}
