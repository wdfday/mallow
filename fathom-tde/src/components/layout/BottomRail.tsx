import { useState } from 'react'
import { SlidersHorizontal } from 'lucide-react'
import { useRunConfig } from '@/lib/run-config-context'
import { SIZE_MODES, SIZE_VALUE_LABEL, type SizeMode } from '@/lib/mallow-file'

// Thin status bar under the main row — the GLOBAL run config ("cái chung"), right-aligned
// (status-bar convention: config/metadata live on the right). It IS the editor, not a shortcut:
// clicking toggles an upward popover with the full form (no separate Config dock tab). Per-file
// overrides live in each editor tab's own ConfigRail and win over these values at run time
// (lib/backtest.ts).

function Seg({ label, value }: { label: string; value: string }) {
  return (
    <span className="flex items-center gap-1">
      <span className="text-sidebar-foreground/35">{label}</span>
      <span className="font-mono text-sidebar-foreground/75">{value}</span>
    </span>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex min-w-0 flex-col gap-0.5">
      <span className="truncate text-[10px] uppercase tracking-wider text-muted-foreground">{label}</span>
      {children}
    </label>
  )
}

const inputCls =
  'h-7 w-full rounded-md border border-border bg-background px-2 text-[11px] outline-none placeholder:text-muted-foreground focus:border-secondary/50'

function NumberInput({
  value,
  placeholder,
  onCommit,
}: {
  value: number | undefined
  placeholder?: string
  onCommit: (v: number | undefined) => void
}) {
  return (
    <input
      type="number"
      step="any"
      defaultValue={value ?? ''}
      key={value ?? 'empty'}
      placeholder={placeholder}
      onBlur={(e) => {
        const raw = e.target.value.trim()
        onCommit(raw === '' ? undefined : Number(raw))
      }}
      className={inputCls}
    />
  )
}

function GlobalConfigForm() {
  const { globals, setGlobals } = useRunConfig()

  return (
    <div className="flex flex-col gap-3 p-3">
      <div>
        <div className="mb-1.5 flex items-baseline gap-2">
          <span className="text-[11px] font-semibold">Run</span>
          <span className="text-[10px] text-muted-foreground">applies to every run — snapshotted into output/backtests</span>
        </div>
        <div className="grid grid-cols-3 gap-2">
          <Field label="From">
            <input
              type="date"
              value={globals.from ?? ''}
              onChange={(e) => setGlobals({ from: e.target.value || undefined })}
              className={inputCls}
            />
          </Field>
          <Field label="To">
            <input
              type="date"
              value={globals.to ?? ''}
              onChange={(e) => setGlobals({ to: e.target.value || undefined })}
              className={inputCls}
            />
          </Field>
          <Field label="Capital (USD)">
            <NumberInput
              value={globals.initialCapital}
              placeholder="10000"
              onCommit={(v) => setGlobals({ initialCapital: v })}
            />
          </Field>
          <Field label="Commission %">
            <NumberInput
              value={globals.commissionPct}
              placeholder="0"
              onCommit={(v) => setGlobals({ commissionPct: v })}
            />
          </Field>
          <Field label="Slippage %">
            <NumberInput
              value={globals.slippagePct}
              placeholder="0"
              onCommit={(v) => setGlobals({ slippagePct: v })}
            />
          </Field>
        </div>
      </div>

      <div>
        <div className="mb-1.5 flex items-baseline gap-2">
          <span className="text-[11px] font-semibold">Sizing defaults</span>
          <span className="text-[10px] text-muted-foreground">.mallow files override these via the editor's config rail</span>
        </div>
        <div className="grid grid-cols-3 gap-2">
          <Field label="Size mode">
            <select
              value={globals.sizeMode ?? ''}
              onChange={(e) => setGlobals({ sizeMode: (e.target.value || undefined) as SizeMode | undefined })}
              className={inputCls}
            >
              <option value="">engine default (percent_equity)</option>
              {SIZE_MODES.map((m) => (
                <option key={m} value={m}>{m}</option>
              ))}
            </select>
          </Field>
          <Field label={globals.sizeMode ? SIZE_VALUE_LABEL[globals.sizeMode] : 'Size value'}>
            <NumberInput
              value={globals.sizeValue}
              placeholder="engine default"
              onCommit={(v) => setGlobals({ sizeValue: v })}
            />
          </Field>
          {globals.sizeMode === 'volatility' && (
            <Field label="ATR multiplier">
              <NumberInput
                value={globals.atrMultiplier}
                placeholder="2.0"
                onCommit={(v) => setGlobals({ atrMultiplier: v })}
              />
            </Field>
          )}
          <Field label="Reverse policy">
            <select
              value={globals.reversePolicy ?? ''}
              onChange={(e) => setGlobals({ reversePolicy: e.target.value || undefined })}
              className={inputCls}
            >
              <option value="">engine default (exit)</option>
              <option value="exit">exit</option>
              <option value="flip">flip</option>
            </select>
          </Field>
          {(globals.sizeMode === undefined || globals.sizeMode === 'percent_equity') && (
            <Field label="Strength sizing">
              <select
                value={globals.strengthSizing === undefined ? '' : String(globals.strengthSizing)}
                onChange={(e) =>
                  setGlobals({ strengthSizing: e.target.value === '' ? undefined : e.target.value === 'true' })
                }
                className={inputCls}
              >
                <option value="">engine default (on)</option>
                <option value="true">on</option>
                <option value="false">off</option>
              </select>
            </Field>
          )}
        </div>
      </div>
    </div>
  )
}

export function BottomRail() {
  const { globals } = useRunConfig()
  const [open, setOpen] = useState(false)

  const range = globals.from || globals.to ? `${globals.from ?? '…'} → ${globals.to ?? '…'}` : 'full'
  const sizing = globals.sizeMode
    ? `${globals.sizeMode}${globals.sizeValue !== undefined ? ` ${globals.sizeValue}` : ''}`
    : 'default'
  const fees =
    globals.commissionPct !== undefined || globals.slippagePct !== undefined
      ? `${globals.commissionPct ?? 0}% / ${globals.slippagePct ?? 0}%`
      : '0%'

  return (
    <div className="relative shrink-0">
      {open && (
        <>
          {/* Click-outside catcher — same pattern as TitleBar's popovers. */}
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} />
          <div className="absolute bottom-7 right-2 z-50 w-[520px] rounded-md border border-border bg-popover text-popover-foreground shadow-md">
            <GlobalConfigForm />
          </div>
        </>
      )}
      <button
        onClick={() => setOpen((p) => !p)}
        title="Global run config — click to edit"
        className="flex h-6 w-full items-center gap-3 bg-sidebar px-3 text-[10px] transition-colors hover:bg-sidebar-accent/60"
      >
        <span className="flex-1" />
        <Seg label="range" value={range} />
        <Seg label="capital" value={globals.initialCapital !== undefined ? `$${globals.initialCapital}` : '$10000'} />
        <Seg label="fees" value={fees} />
        <Seg label="sizing" value={sizing} />
        <Seg label="reverse" value={globals.reversePolicy ?? 'exit'} />
        <SlidersHorizontal className="h-3 w-3 text-sidebar-foreground/40" />
      </button>
    </div>
  )
}
