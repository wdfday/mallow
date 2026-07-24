import { useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { join } from '@tauri-apps/api/path'
import { Search } from 'lucide-react'
import { useProjects } from '@/lib/project-context'
import { useEditor } from '@/lib/editor-context'

// Plain substring search over the active project's text files (src-tauri/src/search — no regex/
// fuzzy engine, matches a research-project-sized directory). Debounced 300ms so typing doesn't
// re-walk the whole tree on every keystroke. Rendered inside RightSidebar.tsx, which draws the
// shared title/close bar — no in-content header here.

interface SearchHit {
  file: string
  line: number
  text: string
}

export function SearchPanel() {
  const { activeProject } = useProjects()
  const { openFile } = useEditor()
  const [query, setQuery] = useState('')
  const [hits, setHits] = useState<SearchHit[]>([])
  const [searching, setSearching] = useState(false)

  useEffect(() => {
    if (!activeProject || !query.trim()) {
      setHits([])
      return
    }
    let cancelled = false
    setSearching(true)
    const timer = setTimeout(() => {
      invoke<SearchHit[]>('search_project', { projectPath: activeProject.path, query })
        .then((result) => { if (!cancelled) setHits(result) })
        .catch(() => { if (!cancelled) setHits([]) })
        .finally(() => { if (!cancelled) setSearching(false) })
    }, 300)
    return () => { cancelled = true; clearTimeout(timer) }
  }, [activeProject, query])

  async function handleOpen(hit: SearchHit) {
    if (!activeProject) return
    const fullPath = await join(activeProject.path, hit.file)
    openFile(fullPath, hit.line)
  }

  return (
    <div className="flex h-full min-w-0 flex-col overflow-hidden bg-background">
      <div className="shrink-0 p-2">
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            disabled={!activeProject}
            autoFocus
            placeholder={activeProject ? 'Search project…' : 'Open a project first'}
            className="h-8 w-full rounded-md border border-border bg-background pl-8 pr-2 text-xs outline-none placeholder:text-muted-foreground focus:border-secondary/50 disabled:opacity-50"
          />
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-2 pb-2">
        {searching && <p className="px-1 py-1 text-[11px] text-muted-foreground">Searching…</p>}
        {!searching && query.trim() && hits.length === 0 && (
          <p className="px-1 py-1 text-[11px] italic text-muted-foreground">No results</p>
        )}
        {hits.map((hit, i) => (
          <button
            key={`${hit.file}:${hit.line}:${i}`}
            onClick={() => void handleOpen(hit)}
            className="flex w-full flex-col gap-0.5 rounded-md px-1.5 py-1 text-left hover:bg-muted/40"
          >
            <span className="truncate text-[10px] text-muted-foreground">{hit.file}:{hit.line}</span>
            <span className="truncate font-mono text-[11px] text-foreground">{hit.text}</span>
          </button>
        ))}
      </div>
    </div>
  )
}
