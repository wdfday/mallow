import { useEffect, useMemo, useRef, useState } from 'react'
import { Editor, loader, type BeforeMount, type OnMount } from '@monaco-editor/react'
import * as monaco from 'monaco-editor'
import type { IDockviewPanelProps } from 'dockview-react'
import { readTextFile, writeTextFile } from '@tauri-apps/plugin-fs'
import { registerScriptLanguage, SCRIPT_LANGUAGE_ID } from '@/lib/monaco-script'
import { parseMallowFile, serializeMallowFile, SIZE_MODES, SIZE_VALUE_LABEL, type MallowMetadata, type SizeMode } from '@/lib/mallow-file'
import { type GlobalRunConfig } from '@/lib/run-config-context'
import { useChartSelection } from '@/lib/chart-context'
import { useEditor, UNTITLED_DIAGNOSTICS_KEY } from '@/lib/editor-context'
import { useTheme } from '@/lib/theme-context'
import { validateScript, probeScriptHtfs, getIndicatorCatalog, type IndicatorCatalogEntry } from '@/lib/almanac-wasm'
import { parseDeclaredIndicators, buildPerTfScripts } from '@/lib/script-indicators'
import { INDICATOR_COLORS } from '@/lib/indicator-colors'
import { buildEngineConfig, persistBacktestRun, runBacktest } from '@/lib/backtest'
import { useProjects } from '@/lib/project-context'
import { useRunConfig } from '@/lib/run-config-context'
import { cn } from '@/lib/utils'
import { AlertCircle, LayoutGrid, Play } from 'lucide-react'

// Local package, not the CDN (jsDelivr) — same rationale as mallow-client's
// components/editor/monaco-script-editor.tsx:10.
loader.config({ monaco })

// The default "editor" panel DockArea creates on startup has no `filePath` — it keeps this
// hardcoded sample .mallow doc, same as before Phase 3. Every panel opened via Sidebar's file
// tree (DockArea's EditorBridge) gets a `filePath` param instead and reads real content off disk.
const SAMPLE_MALLOW = serializeMallowFile({
  metadata: { symbol: 'BTCUSDT', timeframe: 'M15' },
  script: [
    'let rsi = ind.rsi(14);',
    'let h1_trend = ind.ema(50, "H1");',
    'let h4_trend = ind.ema(50, "H4");',
    '',
    'long = cross_above(rsi, 30);',
    'exit = cross_below(rsi, 70);',
  ].join('\n'),
})

interface EditorPanelParams {
  filePath?: string
  /** 1-based line to reveal/select — set by SearchPanel via useEditor().openFile(path, line). */
  line?: number
}

function revealLine(editor: monaco.editor.IStandaloneCodeEditor, line: number) {
  editor.revealLineInCenter(line)
  editor.setPosition({ lineNumber: line, column: 1 })
  editor.focus()
}

function fileBaseName(path: string): string {
  return path.split(/[/\\]/).pop() ?? path
}

function MetadataChip({ label, value, onClick }: { label: string; value: string; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className="rounded-md border border-border bg-muted/30 px-2 py-1 text-[11px] transition-colors hover:border-secondary/50 hover:bg-secondary/10 hover:text-secondary"
      title={`Visualize ${label.toLowerCase()} on Chart`}
    >
      <span className="text-muted-foreground">{label}: </span>
      <span className="font-mono font-medium">{value}</span>
    </button>
  )
}

const TIMEFRAMES = ['M1', 'M5', 'M15', 'M30', 'H1', 'H4', 'D1', 'W1']

/** "+ Symbol" chip → inline input; Enter/blur commits into the frontmatter, Esc cancels. */
function AddSymbolChip({ onCommit }: { onCommit: (symbol: string) => void }) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState('')

  function commit() {
    const sym = value.trim().toUpperCase()
    setEditing(false)
    setValue('')
    if (sym) onCommit(sym)
  }

  if (!editing) {
    return (
      <button
        onClick={() => setEditing(true)}
        title="Set the symbol in this .mallow file's frontmatter"
        className="rounded-md border border-dashed border-border px-2 py-1 text-[11px] text-muted-foreground transition-colors hover:border-secondary/50 hover:text-secondary"
      >
        + Symbol
      </button>
    )
  }
  return (
    <input
      autoFocus
      value={value}
      onChange={(e) => setValue(e.target.value.toUpperCase())}
      onKeyDown={(e) => {
        if (e.key === 'Enter') commit()
        else if (e.key === 'Escape') { setEditing(false); setValue('') }
      }}
      onBlur={commit}
      placeholder="BTCUSDT"
      className="h-[26px] w-24 rounded-md border border-secondary/50 bg-background px-2 font-mono text-[11px] uppercase outline-none placeholder:normal-case placeholder:text-muted-foreground"
    />
  )
}

/** Per-file config rail — this editor tab's OVERRIDES of the global run config. Every control
 *  writes the file's frontmatter (via applyMetadata); "global" (empty) = inherit the BottomRail
 *  defaults. Overridden controls are tinted so a file that deviates from global is visible at a
 *  glance. */
function ConfigRail({
  metadata,
  globals,
  applyMetadata,
}: {
  metadata: MallowMetadata
  globals: GlobalRunConfig
  applyMetadata: (patch: Partial<MallowMetadata>) => void
}) {
  const effMode = metadata.sizeMode ?? globals.sizeMode
  const sel = (overridden: boolean) =>
    cn(
      'h-[22px] cursor-pointer rounded border bg-background px-1 text-[10px] outline-none',
      overridden ? 'border-secondary/60 text-secondary' : 'border-border text-muted-foreground',
    )

  return (
    <div className="flex h-7 shrink-0 items-center gap-2 overflow-x-auto border-b border-border bg-muted/10 px-2 text-[10px]">
      <span className="shrink-0 uppercase tracking-wider text-muted-foreground/60" title="Per-file config overrides — written to this file's frontmatter; 'global' inherits the BottomRail defaults">
        override
      </span>
      <select
        value={metadata.sizeMode ?? ''}
        onChange={(e) => applyMetadata({ sizeMode: (e.target.value || undefined) as SizeMode | undefined })}
        className={sel(metadata.sizeMode !== undefined)}
        title="Size mode"
      >
        <option value="">sizing: global ({globals.sizeMode ?? 'default'})</option>
        {SIZE_MODES.map((m) => (
          <option key={m} value={m}>{m}</option>
        ))}
      </select>
      {effMode && (
        <input
          type="number"
          step="any"
          key={metadata.sizeValue ?? 'inherit'}
          defaultValue={metadata.sizeValue ?? ''}
          placeholder={metadata.sizeMode === undefined && globals.sizeValue !== undefined ? `global (${globals.sizeValue})` : SIZE_VALUE_LABEL[effMode]}
          onBlur={(e) => {
            const raw = e.target.value.trim()
            applyMetadata({ sizeValue: raw === '' ? undefined : Number(raw) })
          }}
          title={SIZE_VALUE_LABEL[effMode]}
          className={cn(
            'h-[22px] w-24 rounded border bg-background px-1 text-[10px] outline-none placeholder:text-muted-foreground/60',
            metadata.sizeValue !== undefined ? 'border-secondary/60 text-secondary' : 'border-border text-muted-foreground',
          )}
        />
      )}
      {effMode === 'volatility' && (
        <input
          type="number"
          step="any"
          key={metadata.atrMultiplier ?? 'inherit'}
          defaultValue={metadata.atrMultiplier ?? ''}
          placeholder="ATR ×"
          onBlur={(e) => {
            const raw = e.target.value.trim()
            applyMetadata({ atrMultiplier: raw === '' ? undefined : Number(raw) })
          }}
          title="ATR multiplier"
          className={cn(
            'h-[22px] w-14 rounded border bg-background px-1 text-[10px] outline-none placeholder:text-muted-foreground/60',
            metadata.atrMultiplier !== undefined ? 'border-secondary/60 text-secondary' : 'border-border text-muted-foreground',
          )}
        />
      )}
      <select
        value={metadata.reversePolicy ?? ''}
        onChange={(e) => applyMetadata({ reversePolicy: e.target.value || undefined })}
        className={sel(metadata.reversePolicy !== undefined)}
        title="Reverse policy"
      >
        <option value="">reverse: global ({globals.reversePolicy ?? 'exit'})</option>
        <option value="exit">exit</option>
        <option value="flip">flip</option>
      </select>
      {(effMode === undefined || effMode === 'percent_equity') && (
        <select
          value={metadata.strengthSizing === undefined ? '' : String(metadata.strengthSizing)}
          onChange={(e) =>
            applyMetadata({ strengthSizing: e.target.value === '' ? undefined : e.target.value === 'true' })
          }
          className={sel(metadata.strengthSizing !== undefined)}
          title="Strength sizing"
        >
          <option value="">strength: global ({globals.strengthSizing === undefined ? 'on' : globals.strengthSizing ? 'on' : 'off'})</option>
          <option value="true">on</option>
          <option value="false">off</option>
        </select>
      )}
    </div>
  )
}

/** Read-only strip listing this script's `ind.*` declarations (varName + type + args), color
 *  swatch matching ChartPanel's overlay line colors (both iterate declaration/snapshot order
 *  against the same INDICATOR_COLORS array) — mirrors mallow-client's chart-legend indicator
 *  chips, placed here instead since fathom's chart only updates on an explicit chip click (see
 *  chart-context.tsx) and this needs to show what's declared even before that click happens.
 *  Informational only for now — no visibility toggle (ChartPanel currently draws every mainpane
 *  indicator unconditionally); wiring per-indicator show/hide would need a second context field
 *  alongside `script`, not built here since it wasn't asked for. */
function IndicatorsRail({ script }: { script: string }) {
  const [catalog, setCatalog] = useState<IndicatorCatalogEntry[] | null>(null)

  useEffect(() => {
    getIndicatorCatalog().then(setCatalog).catch(() => setCatalog([]))
  }, [])

  const declared = useMemo(() => parseDeclaredIndicators(script), [script])
  if (declared.length === 0) return null

  return (
    <div className="flex h-7 shrink-0 items-center gap-1.5 overflow-x-auto border-b border-border bg-muted/10 px-2 text-[10px]">
      <span
        className="shrink-0 uppercase tracking-wider text-muted-foreground/60"
        title="Indicators declared in this script (let NAME = ind.TYPE(...))"
      >
        indicators
      </span>
      {declared.map((d, i) => {
        const meta = catalog?.find((c) => c.name === d.type)
        return (
          <span
            key={`${d.varName}-${i}`}
            className="flex shrink-0 items-center gap-1 rounded border border-border bg-background px-1.5 py-0.5"
            title={meta ? `${meta.label} — ${meta.description}` : `ind.${d.type}`}
          >
            <span
              className="h-1.5 w-1.5 shrink-0 rounded-full"
              style={{ backgroundColor: INDICATOR_COLORS[i % INDICATOR_COLORS.length] }}
            />
            <span className="font-mono font-medium text-foreground/80">{d.varName}</span>
            <span className="text-muted-foreground/60">
              {meta?.label ?? d.type}
              {d.args ? `(${d.args})` : ''}
            </span>
          </span>
        )
      })}
    </div>
  )
}

/** "+ TF" chip as a native select — picking a timeframe commits it into the frontmatter. */
function AddTimeframeChip({ onCommit }: { onCommit: (tf: string) => void }) {
  return (
    <select
      value=""
      onChange={(e) => { if (e.target.value) onCommit(e.target.value) }}
      title="Set the timeframe in this .mallow file's frontmatter"
      className="h-[26px] cursor-pointer appearance-none rounded-md border border-dashed border-border bg-background px-2 text-[11px] text-muted-foreground outline-none transition-colors hover:border-secondary/50 hover:text-secondary"
    >
      <option value="" disabled>+ TF</option>
      {TIMEFRAMES.map((tf) => (
        <option key={tf} value={tf}>{tf}</option>
      ))}
    </select>
  )
}

export function EditorPanel({ params, api }: Partial<IDockviewPanelProps<EditorPanelParams>>) {
  const filePath = params?.filePath
  const baseName = filePath ? fileBaseName(filePath) : 'Editor'

  const [content, setContent] = useState(filePath ? '' : SAMPLE_MALLOW)
  const [fileLoading, setFileLoading] = useState(!!filePath)
  const [dirty, setDirty] = useState(false)
  const [errorCount, setErrorCount] = useState(0)
  const [probingHtfs, setProbingHtfs] = useState(false)
  const { backtest, setBacktest, openScriptChart } = useChartSelection()
  const { setFileDiagnostics, clearFileDiagnostics } = useEditor()
  const { activeProject } = useProjects()
  const { globals, focusDockPanel } = useRunConfig()
  const diagnosticsKey = filePath ?? UNTITLED_DIAGNOSTICS_KEY
  // Stable identity for THIS open tab (not the file path — two untitled tabs would otherwise
  // share one diagnosticsKey and collide into the same hooked chart tab). `api.id` is dockview's
  // own per-panel id, unique for the lifetime of this tab regardless of save state.
  const sourceKey = api?.id ?? diagnosticsKey
  const { themeMode } = useTheme()
  const editorRef = useRef<monaco.editor.IStandaloneCodeEditor | null>(null)
  const monacoRef = useRef<typeof monaco | null>(null)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const saveDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const contentRef = useRef(content)

  const parsed = useMemo(() => parseMallowFile(content), [content])

  // Real file load (Phase 3) — the sample-doc `useState` initializer above only covers the
  // no-filePath default panel; every other Editor tab reads its actual content here.
  useEffect(() => {
    if (!filePath) return
    let cancelled = false
    setFileLoading(true)
    readTextFile(filePath)
      .then((text) => { if (!cancelled) setContent(text) })
      .catch((err) => {
        if (!cancelled) {
          setContent(`# Could not read this file\n# ${err instanceof Error ? err.message : String(err)}`)
        }
      })
      .finally(() => { if (!cancelled) setFileLoading(false) })
    return () => { cancelled = true }
  }, [filePath])

  // Jump-to-line from SearchPanel — fires both when this panel is freshly created with an
  // initial `line` (once loading finishes and the real content is in the model) and when an
  // already-open tab gets `updateParameters({ line })` from clicking another search hit for the
  // same file (params.line changes, editor is already ready).
  useEffect(() => {
    const line = params?.line
    const editor = editorRef.current
    if (line === undefined || !editor || fileLoading) return
    revealLine(editor, line)
  }, [params?.line, fileLoading])

  function markDirty(next: boolean) {
    setDirty(next)
    api?.setTitle(next ? `• ${baseName}` : baseName)
  }

  async function saveNow(text: string) {
    if (!filePath) return
    try {
      await writeTextFile(filePath, text)
      markDirty(false)
    } catch (err) {
      console.error('[editor] save failed:', err)
    }
  }

  // Flushes a pending debounced save on unmount (tab closed right after typing) instead of just
  // cancelling it — losing the last ~1s of edits silently on close would be worse than one extra
  // write.
  useEffect(() => {
    return () => {
      if (saveDebounceRef.current) {
        clearTimeout(saveDebounceRef.current)
        if (filePath) void writeTextFile(filePath, contentRef.current)
      }
    }
  }, [filePath])

  function handleContentChange(next: string) {
    contentRef.current = next
    setContent(next)
    if (!filePath) return
    markDirty(true)
    if (saveDebounceRef.current) clearTimeout(saveDebounceRef.current)
    saveDebounceRef.current = setTimeout(() => void saveNow(next), 1200)
  }

  /** Writes a metadata patch into the file's frontmatter (via the +Symbol/+TF header chips and
   *  this editor's own config rail) — goes through handleContentChange so dirty-marking and
   *  the autosave debounce apply as if the user had typed the frontmatter by hand. */
  function applyMetadata(patch: Partial<typeof parsed.metadata>) {
    handleContentChange(
      serializeMallowFile({ metadata: { ...parsed.metadata, ...patch }, script: parsed.script, extra: parsed.extra }),
    )
  }

  const handleBeforeMount: BeforeMount = (m) => {
    registerScriptLanguage(m)
  }

  const handleMount: OnMount = (editor, m) => {
    editorRef.current = editor
    monacoRef.current = m
    if (filePath) {
      // Cmd/Ctrl+S — save immediately instead of waiting out the autosave debounce.
      editor.addCommand(m.KeyMod.CtrlCmd | m.KeyCode.KeyS, () => {
        if (saveDebounceRef.current) clearTimeout(saveDebounceRef.current)
        void saveNow(editor.getValue())
      })
    }
  }

  // Real lint via alm-wasm's validate_script — the full static-analysis engine (bare-array-compare,
  // wrong-output-var, and/or/not friendly errors, …, see almanac/crates/strategy/src/script/lint.rs),
  // not just basic syntax. No herald HTTP fallback needed (see almanac-wasm.ts: it was only a
  // resilience fallback in mallow-client, never load-bearing). Every diagnostic also gets pushed
  // into editor-context's `diagnosticsByFile` (keyed by this tab's file) for ProblemsPanel, which
  // aggregates across every open tab — inline squigglies alone don't give a project-wide view.
  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(async () => {
      const editor = editorRef.current
      const m = monacoRef.current
      if (!editor || !m) return
      const model = editor.getModel()
      if (!model || !parsed.script.trim()) {
        setErrorCount(0)
        setFileDiagnostics(diagnosticsKey, baseName, [])
        if (model) m.editor.setModelMarkers(model, 'script-lint', [])
        return
      }
      try {
        const result = await validateScript(parsed.script, parsed.metadata.timeframe ?? '')
        const errs = result.errors ?? []
        const lineCount = model.getLineCount()
        const markers: monaco.editor.IMarkerData[] = errs
          .filter((e) => (e.line ?? 1) >= 1 && (e.line ?? 1) <= lineCount)
          .map((e) => ({
            severity: e.severity === 'error' ? m.MarkerSeverity.Error : m.MarkerSeverity.Warning,
            startLineNumber: e.line ?? 1,
            startColumn: Math.max(1, e.col ?? 1),
            endLineNumber: e.line ?? 1,
            endColumn: model.getLineMaxColumn(e.line ?? 1),
            message: e.message,
            source: 'script-lint',
          }))
        m.editor.setModelMarkers(model, 'script-lint', markers)
        setErrorCount(errs.filter((e) => e.severity === 'error').length)
        setFileDiagnostics(diagnosticsKey, baseName, errs)
      } catch (err) {
        console.error('[lint] validate failed:', err)
      }
    }, 800)
    return () => { if (debounceRef.current) clearTimeout(debounceRef.current) }
  }, [parsed.script, parsed.metadata.timeframe, diagnosticsKey, baseName, setFileDiagnostics])

  // Remove this tab's diagnostics from the shared registry when it closes — otherwise
  // ProblemsPanel would keep showing stale problems for a file you're no longer editing.
  useEffect(() => {
    return () => clearFileDiagnostics(diagnosticsKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- diagnosticsKey is stable per tab instance
  }, [diagnosticsKey])

  // "Chart View": from the current script's base TF (.mallow metadata) + every HTF it declares
  // (ind.TYPE(period, "H1") lines, via probe_script_htfs — the same probe ChartState::backtest
  // uses internally to reject HTF scripts on-chart, since a single ChartState is base-TF only),
  // open one chart tab per timeframe so the user can inspect each TF's price action separately —
  // there's no other way to view a multi-TF script's timeframes on-chart in this app.
  //
  // Opens/refreshes one chart tab per timeframe, hooked to THIS script (sourceKey). `htfs`: probed
  // higher-timeframes the script actually uses (via `ind.TYPE(period, "H1")` lines) in addition to
  // the base TF — `[]` skips probing entirely (the chip's single-TF shortcut below). Each tab gets
  // its OWN declarations-only mini-script (buildPerTfScripts) — feeding the raw full script into
  // every tab would trip ChartState's HTF-not-supported guard identically on all of them (the
  // script text contains an HTF reference regardless of which tab it's rendered on), showing zero
  // indicators anywhere instead of each TF's own indicators on its own chart.
  async function openHookedChart(htfs: string[]) {
    const symbol = parsed.metadata.symbol
    const baseTf = parsed.metadata.timeframe
    if (!symbol || !baseTf) return
    const timeframes = [...new Set([baseTf, ...htfs])]
    const scripts = buildPerTfScripts(parsed.script, baseTf, timeframes)
    openScriptChart({ sourceKey, symbol, scripts })
  }

  async function handleChartView() {
    setProbingHtfs(true)
    try {
      const htfs = parsed.script.trim() ? await probeScriptHtfs(parsed.script) : []
      await openHookedChart(htfs)
    } catch (err) {
      console.error('[chart-view] probe failed:', err)
    } finally {
      setProbingHtfs(false)
    }
  }

  async function handleRunBacktest() {
    if (!activeProject) {
      setBacktest({ status: 'error', response: null, error: 'Open a project before running a backtest' })
      return
    }
    setBacktest({ status: 'running', response: null, error: null })
    const params = {
      projectPath: activeProject.path,
      script: parsed.script,
      symbol: parsed.metadata.symbol ?? 'UNKNOWN',
      timeframe: parsed.metadata.timeframe,
      // Global run config (Config tab / BottomRail) merged with this file's frontmatter
      // overrides (config rail below the header) — file wins, see buildEngineConfig.
      from: globals.from,
      to: globals.to,
      config: buildEngineConfig(parsed.metadata, globals),
    }
    try {
      // Native alm-engine run over the full resolved local dataset (Rust backtest_run — the
      // same function the agent's run_backtest tool calls). No mock path here: if no tier has
      // data for the symbol, the run errors with pointers to import/mount/add-symbol.
      const response = await runBacktest(params)
      setBacktest({ status: 'done', response, error: null })
      focusDockPanel('output')
      void persistBacktestRun(params, response)
    } catch (err) {
      setBacktest({ status: 'error', response: null, error: err instanceof Error ? err.message : String(err) })
    }
  }

  return (
    <div className="flex h-full min-w-0 flex-col overflow-hidden bg-background">
      <div className="flex h-9 shrink-0 items-center gap-1.5 border-b border-border px-2">
        {parsed.metadata.symbol ? (
          <MetadataChip
            label="Symbol"
            value={parsed.metadata.symbol}
            onClick={() => void openHookedChart([])}
          />
        ) : (
          <AddSymbolChip onCommit={(symbol) => applyMetadata({ symbol })} />
        )}
        {parsed.metadata.timeframe ? (
          <MetadataChip
            label="Timeframe"
            value={parsed.metadata.timeframe}
            onClick={() => void openHookedChart([])}
          />
        ) : (
          <AddTimeframeChip onCommit={(timeframe) => applyMetadata({ timeframe })} />
        )}
        {filePath && (
          <span className="text-[11px] text-muted-foreground" title={filePath}>
            {fileLoading ? 'Opening…' : dirty ? 'Unsaved' : 'Saved'}
          </span>
        )}
        <div className="flex-1" />
        {errorCount > 0 && (
          <button
            onClick={() => focusDockPanel('problems')}
            className="flex items-center gap-1 rounded px-1 text-[11px] text-destructive hover:bg-destructive/10"
            title={`${errorCount} error(s) — click to open Problems`}
          >
            <AlertCircle className="h-3 w-3" /> {errorCount}
          </button>
        )}
        <button
          onClick={handleChartView}
          disabled={probingHtfs || !parsed.metadata.symbol}
          title="Open one chart tab per timeframe this script uses (base TF + probed HTFs)"
          className="flex items-center gap-1 rounded-md border border-border px-2 py-1 text-[11px] font-medium text-foreground/70 transition-colors hover:border-secondary/50 hover:text-secondary disabled:cursor-not-allowed disabled:opacity-40"
        >
          <LayoutGrid className="h-3 w-3" />
          {probingHtfs ? 'Probing…' : 'Chart View'}
        </button>
        <button
          onClick={handleRunBacktest}
          disabled={backtest.status === 'running' || !parsed.script.trim()}
          className="flex items-center gap-1 rounded-md bg-secondary/15 px-2 py-1 text-[11px] font-medium text-secondary transition-colors hover:bg-secondary/25 disabled:cursor-not-allowed disabled:opacity-40"
        >
          <Play className="h-3 w-3" />
          {backtest.status === 'running' ? 'Running…' : 'Run Backtest'}
        </button>
      </div>
      <ConfigRail metadata={parsed.metadata} globals={globals} applyMetadata={applyMetadata} />
      <IndicatorsRail script={parsed.script} />
      <div className={cn('min-h-0 flex-1')}>
        <Editor
          language={SCRIPT_LANGUAGE_ID}
          theme={themeMode === 'midnight' ? 'vs-dark' : 'vs'}
          value={content}
          onChange={(v) => handleContentChange(v ?? '')}
          beforeMount={handleBeforeMount}
          onMount={handleMount}
          options={{
            fontSize: 13,
            minimap: { enabled: false },
            extraEditorClassName: 'mallow-editor',
            scrollBeyondLastLine: false,
            automaticLayout: true,
          }}
        />
      </div>
    </div>
  )
}
