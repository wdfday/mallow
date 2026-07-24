import { AlertCircle, AlertTriangle } from 'lucide-react'
import { useEditor, UNTITLED_DIAGNOSTICS_KEY } from '@/lib/editor-context'
import { cn } from '@/lib/utils'

// Aggregated lint output across every currently-open editor tab (VSCode's "Problems" panel
// scope) — inline squigglies + EditorPanel's per-tab error-count badge only show one file at a
// time; this is the project-wide view. Diagnostics come from the SAME `validate_script` call
// EditorPanel already runs (the full static-analysis engine, not just syntax), pushed into
// editor-context's `diagnosticsByFile` registry as each tab lints itself.

export function ProblemsPanel() {
  const { diagnosticsByFile, openFile } = useEditor()

  const rows = Object.entries(diagnosticsByFile)
    .flatMap(([key, { fileName, diagnostics }]) => diagnostics.map((d, i) => ({ key, fileName, idx: i, ...d })))
    .sort((a, b) => a.fileName.localeCompare(b.fileName) || (a.line ?? 0) - (b.line ?? 0))

  const errorCount = rows.filter((r) => r.severity === 'error').length
  const warningCount = rows.filter((r) => r.severity === 'warning').length

  if (rows.length === 0) {
    return (
      <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
        No problems — every open script is clean.
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col overflow-hidden bg-background">
      <div className="flex shrink-0 items-center gap-1.5 border-b border-border px-2.5 py-1.5 text-[11px] text-muted-foreground">
        {errorCount > 0 && (
          <span className="flex items-center gap-1 text-destructive">
            <AlertCircle className="h-3 w-3" /> {errorCount} error{errorCount === 1 ? '' : 's'}
          </span>
        )}
        {warningCount > 0 && (
          <span className="flex items-center gap-1 text-amber-500">
            <AlertTriangle className="h-3 w-3" /> {warningCount} warning{warningCount === 1 ? '' : 's'}
          </span>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {rows.map((r) => {
          const clickable = r.key !== UNTITLED_DIAGNOSTICS_KEY
          return (
            <button
              key={`${r.key}-${r.idx}`}
              onClick={() => clickable && openFile(r.key, r.line)}
              disabled={!clickable}
              title={clickable ? 'Jump to line' : 'Unsaved tab — no file to jump to'}
              className={cn(
                'flex w-full items-start gap-2 border-b border-border/40 px-2.5 py-1.5 text-left text-[11px]',
                clickable ? 'cursor-pointer hover:bg-muted/40' : 'cursor-default opacity-70',
              )}
            >
              {r.severity === 'error' ? (
                <AlertCircle className="mt-0.5 h-3 w-3 shrink-0 text-destructive" />
              ) : (
                <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0 text-amber-500" />
              )}
              <span className="min-w-0 flex-1 truncate text-foreground/85">{r.message}</span>
              <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                {r.fileName}{r.line ? `:${r.line}` : ''}
              </span>
            </button>
          )
        })}
      </div>
    </div>
  )
}
