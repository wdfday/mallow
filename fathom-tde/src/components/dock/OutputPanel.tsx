import { useChartSelection } from '@/lib/chart-context'
import { EquityCurve } from '@/components/ui/EquityCurve'
import { cn } from '@/lib/utils'

// Displays the result of EditorPanel's "Run Backtest" button — a native
// alm_engine::types::BacktestResponse from the Rust backtest_run command (full local dataset,
// real warm-up), typed in lib/backtest.ts against engine/src/types/response.rs. Stats row +
// equity curve + trades table — the same 5 headline metrics mallow-client's /strategy page
// leads with, plus the curve the response already carries (curves.equity).

function StatTile({ label, value, tone }: { label: string; value: string; tone?: 'pos' | 'neg' }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-md border border-border bg-muted/20 px-2.5 py-1.5">
      <span className="text-[10px] uppercase tracking-wider text-muted-foreground">{label}</span>
      <span className={cn(
        'font-mono text-[13px] font-semibold tabular-nums',
        tone === 'pos' && 'text-emerald-500',
        tone === 'neg' && 'text-red-500',
      )}>
        {value}
      </span>
    </div>
  )
}

function fmtPct(v: unknown): string {
  return typeof v === 'number' ? `${v >= 0 ? '+' : ''}${v.toFixed(2)}%` : '—'
}

function fmtNum(v: unknown, digits = 2): string {
  return typeof v === 'number' ? v.toFixed(digits) : '—'
}

function fmtTs(ms: number): string {
  return new Date(ms).toISOString().slice(0, 16).replace('T', ' ')
}

export function OutputPanel() {
  const { backtest } = useChartSelection()
  const r = backtest.response

  if (backtest.status === 'idle') {
    return (
      <div className="flex h-full items-center justify-center p-4 text-center text-xs text-muted-foreground">
        Click "Run Backtest" in the Editor to see results here.
      </div>
    )
  }

  if (backtest.status === 'running') {
    return (
      <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
        Running backtest…
      </div>
    )
  }

  if (backtest.status === 'error') {
    return (
      <div className="flex h-full items-center justify-center p-4 text-center text-xs text-destructive">
        {backtest.error ?? 'Backtest failed'}
      </div>
    )
  }

  const totalReturn = r?.returns?.total_return_pct
  const winRate = r?.trade_stats?.win_rate_pct
  const maxDd = r?.drawdown?.max_drawdown_pct
  const sharpe = r?.risk_adjusted?.sharpe_ratio
  const profitFactor = r?.trade_stats?.profit_factor
  const trades = r?.trades ?? []
  const equity = r?.curves?.equity ?? []

  return (
    <div className="flex h-full min-w-0 flex-col overflow-hidden bg-background">
      <div className="grid shrink-0 grid-cols-5 gap-2 border-b border-border p-2">
        <StatTile label="Return" value={fmtPct(totalReturn)} tone={typeof totalReturn === 'number' ? (totalReturn > 0 ? 'pos' : 'neg') : undefined} />
        <StatTile label="Win Rate" value={fmtPct(winRate)} />
        <StatTile label="Max DD" value={fmtPct(maxDd)} tone="neg" />
        <StatTile label="Sharpe" value={fmtNum(sharpe)} />
        <StatTile label="Profit F." value={fmtNum(profitFactor)} />
      </div>
      <div className="flex min-h-0 flex-1 overflow-hidden">
        {equity.length > 0 && (
          <div className="min-w-0 flex-1 border-r border-border p-1">
            <EquityCurve points={equity} />
          </div>
        )}
        <div className="min-h-0 min-w-0 flex-1 overflow-y-auto">
          {trades.length === 0 ? (
            <div className="flex h-full items-center justify-center text-xs text-muted-foreground">No trades</div>
          ) : (
            <table className="w-full text-left text-[11px]">
              <thead className="sticky top-0 bg-background">
                <tr className="border-b border-border text-muted-foreground">
                  <th className="py-1.5 pl-3 font-medium">#</th>
                  <th className="py-1.5 font-medium">Side</th>
                  <th className="py-1.5 font-medium">Entry</th>
                  <th className="py-1.5 font-medium">Exit</th>
                  <th className="py-1.5 font-medium">Exit time</th>
                  <th className="py-1.5 font-medium">Reason</th>
                  <th className="py-1.5 pr-3 text-right font-medium">PnL%</th>
                </tr>
              </thead>
              <tbody>
                {trades.map((t, i) => {
                  const pnl = typeof t.pnl_pct === 'number' ? t.pnl_pct : undefined
                  return (
                    <tr key={i} className="border-b border-border/50">
                      <td className="py-1.5 pl-3 text-muted-foreground">{i + 1}</td>
                      <td className="py-1.5 font-mono">{String(t.side ?? '—')}</td>
                      <td className="py-1.5 font-mono">{fmtNum(t.entry_price, 2)}</td>
                      <td className="py-1.5 font-mono">{fmtNum(t.exit_price, 2)}</td>
                      <td className="py-1.5 font-mono text-muted-foreground">{typeof t.exit_ts === 'number' ? fmtTs(t.exit_ts) : '—'}</td>
                      <td className="py-1.5 text-muted-foreground">{String(t.exit_reason ?? '—')}</td>
                      <td className={cn('py-1.5 pr-3 text-right font-mono', pnl !== undefined && (pnl >= 0 ? 'text-emerald-500' : 'text-red-500'))}>
                        {pnl !== undefined ? fmtPct(pnl) : '—'}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  )
}
