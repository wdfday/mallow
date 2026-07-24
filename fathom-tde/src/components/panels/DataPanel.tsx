import { useCallback, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { listen } from '@tauri-apps/api/event'
import { open } from '@tauri-apps/plugin-dialog'
import { Database, FolderOpen, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { useProjects } from '@/lib/project-context'
import { cn } from '@/lib/utils'

// Data panel — the on-demand loader's UI (see docs/VISION.md, "Kiến trúc data"):
// - Universe: symbols the user added to research; adding one kicks off a background
//   Binance Vision sync into ~/Fathom/.data (progress via the data://sync-progress event).
// - Mounts: external directories registered read-in-place in the SQLite registry.
// - Catalog: what's actually resolvable right now, across project data/ + mounts + lake.
// Rendered inside RightSidebar.tsx (shared title/close bar — no in-content header).

interface UniverseSymbol {
  id: number
  symbol: string
  provider: string
  status: 'pending' | 'syncing' | 'ready' | 'error'
  error: string | null
  addedAt: string
  lastSync: string | null
}

interface MountSource {
  id: number
  name: string
  path: string
  createdAt: string
}

interface CatalogEntry {
  symbol: string
  timeframe: string
  provider: string
  rootKind: string
  rootName: string
  files: number
}

interface SyncProgress {
  symbol: string
  stage: 'm1' | 'ladder' | 'done' | 'error'
  detail: string | null
  done: number
  total: number
  error: string | null
}

const STATUS_STYLES: Record<UniverseSymbol['status'], string> = {
  pending: 'bg-muted/40 text-muted-foreground',
  syncing: 'bg-sky-500/15 text-sky-500',
  ready: 'bg-emerald-500/15 text-emerald-500',
  error: 'bg-red-500/15 text-red-500',
}

function SectionHeader({ title, onRefresh }: { title: string; onRefresh?: () => void }) {
  return (
    <div className="flex items-center justify-between px-1 pb-1 pt-3 first:pt-0">
      <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">{title}</span>
      {onRefresh && (
        <button onClick={onRefresh} title="Refresh" className="text-muted-foreground hover:text-foreground">
          <RefreshCw className="h-3 w-3" />
        </button>
      )}
    </div>
  )
}

export function DataPanel() {
  const { activeProject } = useProjects()
  const [symbols, setSymbols] = useState<UniverseSymbol[]>([])
  const [mounts, setMounts] = useState<MountSource[]>([])
  const [catalog, setCatalog] = useState<CatalogEntry[]>([])
  const [newSymbol, setNewSymbol] = useState('')
  const [progress, setProgress] = useState<Record<string, SyncProgress>>({})
  const [actionError, setActionError] = useState<string | null>(null)

  const refreshSymbols = useCallback(() => {
    invoke<UniverseSymbol[]>('symbols_list').then(setSymbols).catch(() => setSymbols([]))
  }, [])
  const refreshMounts = useCallback(() => {
    invoke<MountSource[]>('sources_list').then(setMounts).catch(() => setMounts([]))
  }, [])
  const refreshCatalog = useCallback(() => {
    invoke<CatalogEntry[]>('data_catalog', { projectPath: activeProject?.path ?? null })
      .then(setCatalog)
      .catch(() => setCatalog([]))
  }, [activeProject?.path])

  useEffect(() => {
    refreshSymbols()
    refreshMounts()
    refreshCatalog()
  }, [refreshSymbols, refreshMounts, refreshCatalog])

  // Live sync progress. Terminal stages also refresh the DB-backed lists so status
  // badges/coverage flip without a manual refresh.
  useEffect(() => {
    const unlisten = listen<SyncProgress>('data://sync-progress', (e) => {
      const p = e.payload
      setProgress((prev) => ({ ...prev, [p.symbol]: p }))
      if (p.stage === 'done' || p.stage === 'error') {
        refreshSymbols()
        refreshCatalog()
      }
    })
    return () => { void unlisten.then((fn) => fn()) }
  }, [refreshSymbols, refreshCatalog])

  async function handleAddSymbol() {
    const sym = newSymbol.trim().toUpperCase()
    if (!sym) return
    setActionError(null)
    try {
      await invoke('symbols_add', { symbol: sym })
      setNewSymbol('')
      refreshSymbols()
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err))
    }
  }

  async function handleRemoveSymbol(sym: string) {
    setActionError(null)
    try {
      await invoke('symbols_remove', { symbol: sym })
      setProgress((prev) => {
        const next = { ...prev }
        delete next[sym]
        return next
      })
      refreshSymbols()
      refreshCatalog()
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err))
    }
  }

  async function handleAddMount() {
    setActionError(null)
    const selected = await open({ directory: true, multiple: false })
    if (!selected) return
    try {
      await invoke('sources_add_mount', { path: selected, name: null })
      refreshMounts()
      refreshCatalog()
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err))
    }
  }

  async function handleRemoveMount(id: number) {
    setActionError(null)
    try {
      await invoke('sources_remove', { id })
      refreshMounts()
      refreshCatalog()
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err))
    }
  }

  function progressLabel(sym: UniverseSymbol): string | null {
    const p = progress[sym.symbol]
    if (!p || (p.stage !== 'm1' && p.stage !== 'ladder')) return null
    return p.stage === 'm1'
      ? `downloading ${p.detail ?? ''} (${p.done}/${p.total} months)`
      : `resampling ${p.detail ?? ''} (${p.done}/${p.total})`
  }

  return (
    <div className="flex h-full min-w-0 flex-col overflow-y-auto bg-background p-2">
      {actionError && (
        <div className="mb-2 rounded-md border border-red-500/30 bg-red-500/10 px-2 py-1 text-[11px] text-red-500">
          {actionError}
        </div>
      )}

      <SectionHeader title="Universe" onRefresh={refreshSymbols} />
      <div className="flex gap-1.5 px-1 pb-1.5">
        <input
          value={newSymbol}
          onChange={(e) => setNewSymbol(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') void handleAddSymbol() }}
          placeholder="Add symbol, e.g. BTCUSDT"
          className="h-7 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-xs uppercase outline-none placeholder:normal-case placeholder:text-muted-foreground focus:border-secondary/50"
        />
        <button
          onClick={() => void handleAddSymbol()}
          disabled={!newSymbol.trim()}
          title="Add symbol — the loader downloads its history into ~/Fathom/.data"
          className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-secondary/15 text-secondary hover:bg-secondary/25 disabled:cursor-not-allowed disabled:opacity-40"
        >
          <Plus className="h-3.5 w-3.5" />
        </button>
      </div>
      {symbols.length === 0 && (
        <p className="px-1 pb-1 text-[11px] italic text-muted-foreground">
          No symbols yet — add one and the loader will download its data locally.
        </p>
      )}
      {symbols.map((s) => {
        const live = progressLabel(s)
        return (
          <div key={s.id} className="group flex items-center gap-1.5 rounded-md px-1.5 py-1 hover:bg-muted/40">
            <Database className="h-3 w-3 shrink-0 text-muted-foreground" />
            <span className="font-mono text-[11px] font-medium">{s.symbol}</span>
            <span className={cn('rounded-sm px-1 py-0.5 text-[9px] font-semibold uppercase', STATUS_STYLES[s.status])}>
              {s.status}
            </span>
            <span className="min-w-0 flex-1 truncate text-[10px] text-muted-foreground" title={s.error ?? undefined}>
              {live ?? (s.status === 'error' ? s.error : s.lastSync ? `sync ${s.lastSync}` : '')}
            </span>
            <button
              onClick={() => void handleRemoveSymbol(s.symbol)}
              title="Remove symbol and its downloaded data"
              className="invisible text-muted-foreground hover:text-red-500 group-hover:visible"
            >
              <Trash2 className="h-3 w-3" />
            </button>
          </div>
        )
      })}

      <SectionHeader title="Mounts" onRefresh={refreshMounts} />
      <button
        onClick={() => void handleAddMount()}
        className="mx-1 mb-1.5 flex h-7 items-center justify-center gap-1.5 rounded-md border border-dashed border-border text-[11px] text-muted-foreground hover:border-secondary/50 hover:text-secondary"
      >
        <FolderOpen className="h-3 w-3" /> Mount data folder
      </button>
      {mounts.map((m) => (
        <div key={m.id} className="group flex items-center gap-1.5 rounded-md px-1.5 py-1 hover:bg-muted/40">
          <FolderOpen className="h-3 w-3 shrink-0 text-muted-foreground" />
          <span className="text-[11px] font-medium">{m.name}</span>
          <span className="min-w-0 flex-1 truncate text-[10px] text-muted-foreground" title={m.path}>{m.path}</span>
          <button
            onClick={() => void handleRemoveMount(m.id)}
            title="Unmount (files are kept)"
            className="invisible text-muted-foreground hover:text-red-500 group-hover:visible"
          >
            <Trash2 className="h-3 w-3" />
          </button>
        </div>
      ))}

      <SectionHeader title="Catalog" onRefresh={refreshCatalog} />
      {catalog.length === 0 && (
        <p className="px-1 text-[11px] italic text-muted-foreground">No resolvable data yet.</p>
      )}
      {catalog.map((c, i) => (
        <div key={i} className="flex items-center gap-1.5 rounded-md px-1.5 py-0.5 text-[11px]">
          <span className="font-mono font-medium">{c.symbol}</span>
          <span className="rounded-sm bg-muted/40 px-1 text-[9px] font-semibold">{c.timeframe}</span>
          <span className="min-w-0 flex-1 truncate text-[10px] text-muted-foreground">
            {c.rootKind === 'lake' ? 'Fathom' : c.rootName} · {c.provider} · {c.files} file
          </span>
        </div>
      ))}
    </div>
  )
}
