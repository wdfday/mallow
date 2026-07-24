import { useEffect, useState } from 'react'
import 'dockview-react/dist/styles/dockview.css'
import { DockviewReact, type DockviewApi, type DockviewReadyEvent, type IDockviewPanelProps } from 'dockview-react'
import { ChartPanel } from './ChartPanel'
import { EditorPanel } from './EditorPanel'
import { OutputPanel } from './OutputPanel'
import { CliPanel } from './CliPanel'
import { ProblemsPanel } from './ProblemsPanel'
import { HandViewPanel } from './HandViewPanel'
import { ChartSelectionProvider, useChartSelection } from '@/lib/chart-context'
import { useEditor } from '@/lib/editor-context'
import { useRunConfig } from '@/lib/run-config-context'
import { useHandView } from '@/lib/hand-view-context'

// Chart + Editor + Output + CLI are the dock's job — project content, arbitrarily many tabs/
// groups, resize/tab/move for free via Dockview. Agent/Search/Settings/Helm are NOT here — they're
// fixed single-instance tool views, same category as the Sidebar file tree, and live in
// components/layout/RightSidebar.tsx instead (toggled by RightRailEdge, see lib/panel-context.tsx).
// Chart hooks: the default "chart" panel is intentionally hookless (nothing writes to it) —
// EditorPanel's Symbol/Timeframe chips and "Chart View" button (ScriptChartBridge below) open
// dedicated chart tabs hooked to that specific script, and clicking a hand (HandViewBridge below)
// opens/updates a dedicated tab hooked to that hand — neither ever touches the default. "Run
// Backtest" still populates Output via ChartSelectionProvider (React Context, propagates through
// Dockview's panel portals).

const components: Record<string, React.FunctionComponent<IDockviewPanelProps>> = {
  chart: (props) => <ChartPanel {...props} />,
  editor: (props) => <EditorPanel {...props} />,
  output: () => <OutputPanel />,
  cli: () => <CliPanel />,
  problems: () => <ProblemsPanel />,
  hand: (props) => <HandViewPanel {...props} />,
}

function fileBaseName(path: string): string {
  return path.split(/[/\\]/).pop() ?? path
}

/** Registers the real dockview-backed openFile implementation (editor-context.tsx only holds a
 * ref for it) — each opened file gets its own panel, id = its full path (so re-clicking an
 * already-open file focuses the existing tab instead of duplicating it), tabbed alongside the
 * default "editor" panel rather than replacing it. `line` (from SearchPanel) is passed through as
 * a param either way — `updateParameters` on an already-open tab, or as the initial params on a
 * freshly created one; EditorPanel reacts to `params.line` changing to jump/reveal. */
/** Registers the dockview-backed focusDockPanel impl (run-config-context only holds a ref) —
 * lets EditorPanel jump to Output after a run. */
function FocusBridge({ api }: { api: DockviewApi | null }) {
  const { registerFocusHandler } = useRunConfig()
  registerFocusHandler((id) => {
    api?.getPanel(id)?.api.setActive()
  })
  return null
}

function EditorBridge({ api }: { api: DockviewApi | null }) {
  const { registerOpenFileHandler, setActiveFilePath } = useEditor()

  registerOpenFileHandler((path, line) => {
    if (!api) return
    const existing = api.getPanel(path)
    if (existing) {
      existing.api.setActive()
      if (line !== undefined) existing.api.updateParameters({ filePath: path, line })
      return
    }
    const editorPanel = api.getPanel('editor')
    api.addPanel({
      id: path,
      component: 'editor',
      title: fileBaseName(path),
      params: { filePath: path, line },
      position: editorPanel ? { referencePanel: editorPanel, direction: 'within' } : undefined,
    })
  })

  // Feeds Sidebar's active-file highlight/auto-reveal (editor-context.tsx). Only reacts to
  // "editor-shaped" panels — the default untitled editor (no params → clears the highlight, since
  // nothing real is open there) or a file-backed one (sets it to that file). Switching to
  // Chart/Output/CLI is deliberately left alone, matching VSCode: focusing the terminal/a
  // non-editor view doesn't blank out which file was last active in the Explorer.
  useEffect(() => {
    if (!api) return
    const disposable = api.onDidActivePanelChange(({ panel }) => {
      if (!panel) return
      if (panel.id === 'editor' && !panel.params) {
        setActiveFilePath(null)
        return
      }
      const filePath = (panel.params as { filePath?: string } | undefined)?.filePath
      if (typeof filePath === 'string') setActiveFilePath(filePath)
    })
    return () => disposable.dispose()
  }, [api, setActiveFilePath])

  return null
}

// Registers the real dockview-backed openScriptChart implementation (chart-context.tsx only
// holds a ref for it) — one tab per timeframe, keyed by (editor tab, timeframe) so it survives a
// frontmatter symbol edit without orphaning a stale tab and so two different scripts sharing a
// symbol/TF don't collide into the same panel. Re-triggering for a script that already has a tab
// open refreshes that tab's params (symbol/script may have changed) instead of duplicating it.
// Opened into the *left* chart area (tabbed with the default "Chart" panel) so they fill the
// layout's spare column instead of splitting a new group off the Editor and squeezing it. Lives
// inside <ChartSelectionProvider> as a plain child so useChartSelection works; renders nothing.
function ScriptChartBridge({ api }: { api: DockviewApi | null }) {
  const { registerOpenScriptChartHandler } = useChartSelection()

  registerOpenScriptChartHandler(({ sourceKey, symbol, scripts }) => {
    const timeframes = Object.keys(scripts)
    if (!api || timeframes.length === 0) return
    const chartPanel = api.getPanel('chart')
    const editorPanel = api.getPanel('editor')
    let prevPanel: ReturnType<typeof api.getPanel> | undefined

    timeframes.forEach((tf) => {
      const id = `chart-script-${sourceKey}-${tf}`
      const script = scripts[tf]
      const existing = api.getPanel(id)
      if (existing) {
        existing.api.updateParameters({ symbol, timeframe: tf, script })
        existing.api.setActive()
        prevPanel = existing
        return
      }
      const panel = api.addPanel({
        id,
        component: 'chart',
        title: `${symbol} · ${tf}`,
        params: { symbol, timeframe: tf, script },
        // First tab joins the default Chart's group (left column); the rest stack as sibling
        // tabs there. Fallbacks only matter if the user closed those defaults: left of Editor,
        // else wherever dockview drops it.
        position: prevPanel
          ? { referencePanel: prevPanel, direction: 'within' }
          : chartPanel
            ? { referencePanel: chartPanel, direction: 'within' }
            : editorPanel
              ? { referencePanel: editorPanel, direction: 'left' }
              : undefined,
      })
      prevPanel = panel
    })
  })

  return null
}

/** Registers the dockview-backed openHand implementation (hand-view-context only holds a ref) —
 * opens a "Hand · <name>" tab in the bottom group (alongside Output/CLI, mirroring
 * mallow-client's /strategy page bottom-tab bundle) and, when the caller supplied a
 * symbol/timeframe, opens/updates a dedicated chart tab hooked to this hand (`chart-hand-{id}`) —
 * quietly, without stealing focus from the Hand panel this click is actually opening. Never
 * touches the default "chart" panel (hookless by design, see the module doc comment above). Lives
 * inside <ChartSelectionProvider> (a plain child) so useChartSelection works here. */
function HandViewBridge({ api }: { api: DockviewApi | null }) {
  const { registerOpenHandHandler } = useHandView()

  registerOpenHandHandler((params) => {
    if (!api) return
    if (params.symbol || params.timeframe) {
      const chartId = `chart-hand-${params.handId}`
      const existingChart = api.getPanel(chartId)
      if (existingChart) {
        existingChart.api.updateParameters({ symbol: params.symbol, timeframe: params.timeframe })
      } else {
        const chartPanel = api.getPanel('chart')
        api.addPanel({
          id: chartId,
          component: 'chart',
          title: `${params.symbol ?? params.handName} · ${params.timeframe ?? ''}`,
          params: { symbol: params.symbol, timeframe: params.timeframe },
          position: chartPanel ? { referencePanel: chartPanel, direction: 'within' } : undefined,
          // Quiet — this click is opening the Hand panel (added right below), not the chart;
          // don't steal focus onto a tab the user didn't ask to look at.
          inactive: true,
        })
      }
    }
    const id = `hand-${params.handId}`
    const existing = api.getPanel(id)
    if (existing) {
      existing.api.updateParameters({ helmId: params.helmId, handId: params.handId, helmName: params.helmName })
      existing.api.setActive()
      return
    }
    const outputPanel = api.getPanel('output')
    api.addPanel({
      id,
      component: 'hand',
      title: `Hand · ${params.handName}`,
      params: { helmId: params.helmId, handId: params.handId, helmName: params.helmName },
      position: outputPanel ? { referencePanel: outputPanel, direction: 'within' } : undefined,
    })
  })

  return null
}

export function DockArea() {
  // State, not a plain local — onReady fires from inside dockview's own mount lifecycle, after
  // DockArea's first render. A plain variable assigned there wouldn't trigger a re-render, so
  // ScriptChartBridge (below) would forever close over a stale `null` api.
  const [dockApi, setDockApi] = useState<DockviewApi | null>(null)

  // Default layout: Chart fills the left column full-height; the right column is Editor with
  // Output+CLI docked *under the editor* (not under the chart) — output/terminal belong to the
  // code you're editing, and the chart keeps its full height for price action.
  function onReady(event: DockviewReadyEvent) {
    const chart = event.api.addPanel({ id: 'chart', component: 'chart', title: 'Chart' })
    const editor = event.api.addPanel({
      id: 'editor',
      component: 'editor',
      title: 'Editor',
      position: { referencePanel: chart, direction: 'right' },
    })
    const output = event.api.addPanel({
      id: 'output',
      component: 'output',
      title: 'Output',
      position: { referencePanel: editor, direction: 'below' },
    })
    event.api.addPanel({
      id: 'cli',
      component: 'cli',
      title: 'CLI',
      position: { referencePanel: output, direction: 'within' },
    })
    event.api.addPanel({
      id: 'problems',
      component: 'problems',
      title: 'Problems',
      position: { referencePanel: output, direction: 'within' },
    })
    // Output in front by default — CLI is the sibling tab.
    output.api.setActive()
    setDockApi(event.api)
  }

  return (
    <div className="h-full w-full">
      <ChartSelectionProvider>
        <DockviewReact
          className="dockview-theme-abyss h-full w-full"
          components={components}
          onReady={onReady}
        />
        <ScriptChartBridge api={dockApi} />
        <EditorBridge api={dockApi} />
        <FocusBridge api={dockApi} />
        <HandViewBridge api={dockApi} />
      </ChartSelectionProvider>
    </div>
  )
}
