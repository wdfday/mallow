import { invoke } from '@tauri-apps/api/core'
import { appDataDir, basename, dirname, join } from '@tauri-apps/api/path'
import { copyFile, exists, mkdir, readDir, readTextFile, remove, rename, writeTextFile } from '@tauri-apps/plugin-fs'
import { open } from '@tauri-apps/plugin-dialog'
import { serializeMallowFile } from './mallow-file'

export interface ProjectEntry {
  path: string
  name: string
  lastOpened: string
}

export interface FileEntry {
  name: string
  path: string
  isDirectory: boolean
}

const REGISTRY_FILE = 'projects.json'
// Self-contained project layout — see research/fathom-cursor-for-alpha-research.md
// ("Project structure — mỗi project tự sở hữu data + output"). No `.venv` yet — Python
// kernel work is a separate, still-open phase.
const PROJECT_SUBDIRS = ['data', 'output', 'strategies', 'notes']

// Which registered project is "current" — shown in TitleBar's project switcher, auto-expanded in
// Sidebar. localStorage, not the Tauri-fs-backed registry file: this is ephemeral UI preference
// (which project you're looking at right now), not project data worth round-tripping through fs.
const ACTIVE_PROJECT_KEY = 'fathom:activeProjectPath'

export function getActiveProjectPath(): string | null {
  return localStorage.getItem(ACTIVE_PROJECT_KEY)
}

export function setActiveProjectPath(path: string): void {
  localStorage.setItem(ACTIVE_PROJECT_KEY, path)
}

async function registryPath(): Promise<string> {
  return join(await appDataDir(), REGISTRY_FILE)
}

function projectNameFromPath(path: string): string {
  const parts = path.split(/[/\\]/).filter(Boolean)
  return parts[parts.length - 1] ?? path
}

export async function loadProjects(): Promise<ProjectEntry[]> {
  const p = await registryPath()
  if (!(await exists(p))) return []
  try {
    return JSON.parse(await readTextFile(p)) as ProjectEntry[]
  } catch {
    return []
  }
}

async function saveProjects(projects: ProjectEntry[]): Promise<void> {
  const dir = await appDataDir()
  if (!(await exists(dir))) await mkdir(dir, { recursive: true })
  await writeTextFile(await registryPath(), JSON.stringify(projects, null, 2))
}

async function registerProject(path: string): Promise<ProjectEntry[]> {
  const projects = await loadProjects()
  const entry: ProjectEntry = {
    path,
    name: projectNameFromPath(path),
    lastOpened: new Date().toISOString(),
  }
  const next = projects.some((p) => p.path === path)
    ? projects.map((p) => (p.path === path ? entry : p))
    : [...projects, entry]
  await saveProjects(next)
  // Just created/opened it — make it the active one (natural default, not an extra step the
  // user has to take separately).
  setActiveProjectPath(path)
  return next
}

/** Scaffolds data/output/strategies/notes under `dir` and registers it as a project. */
export async function createProject(dir: string): Promise<ProjectEntry[]> {
  for (const sub of PROJECT_SUBDIRS) {
    const subPath = await join(dir, sub)
    if (!(await exists(subPath))) await mkdir(subPath, { recursive: true })
  }
  return registerProject(dir)
}

/** `~/Fathom` — default parent for new projects (see docs/VISION.md); null when the Rust side
 *  can't resolve a home dir (exotic setup) — callers then require an explicit Browse. */
export async function defaultProjectParent(): Promise<string | null> {
  return invoke<string>('fathom_home_path').catch(() => null)
}

/** Bare folder picker (no registration) — used by the New Project form's Browse. */
export async function pickFolder(defaultPath?: string | null): Promise<string | null> {
  return open({
    directory: true,
    multiple: false,
    ...(defaultPath ? { defaultPath } : {}),
  })
}

/** Creates `parentDir/name`, scaffolds it as a project, registers + activates it. */
export async function createProjectAt(parentDir: string, name: string): Promise<ProjectEntry[]> {
  const dir = await join(parentDir, name)
  if (await exists(dir)) throw new Error(`"${name}" already exists in ${parentDir}`)
  await mkdir(dir, { recursive: true })
  return createProject(dir)
}

/** Opens a folder picker and registers the chosen directory as a project (no scaffolding).
 *  Starts at `~/Fathom/` — the default place projects live (see docs/VISION.md), still free to
 *  navigate anywhere. */
export async function openProjectDialog(): Promise<ProjectEntry[] | null> {
  const selected = await pickFolder(await defaultProjectParent())
  if (!selected) return null
  return registerProject(selected)
}

/**
 * Opens a file picker (Parquet/CSV) and copies whatever's chosen into `projectPath/data/` —
 * matches `data-service.ts::findLocalDataFile`'s lookup convention (`data/{symbol}.parquet|.csv`),
 * so the file just needs to be named after its symbol (e.g. `BTCUSDT.parquet`) to be picked up by
 * Chart/backtest immediately. Returns the copied filenames (for a toast/confirmation), or `null`
 * if the picker was cancelled.
 */
export async function importDataFiles(projectPath: string): Promise<string[] | null> {
  const selected = await open({
    multiple: true,
    filters: [{ name: 'OHLCV data', extensions: ['parquet', 'csv'] }],
  })
  if (!selected) return null
  const paths = Array.isArray(selected) ? selected : [selected]
  const dataDir = await join(projectPath, 'data')
  if (!(await exists(dataDir))) await mkdir(dataDir, { recursive: true })

  const names: string[] = []
  for (const src of paths) {
    const name = await basename(src)
    await copyFile(src, await join(dataDir, name))
    names.push(name)
  }
  return names
}

/**
 * Creates a new file under `dirPath`. A `.mallow` file gets the real frontmatter+script skeleton
 * (via `serializeMallowFile` — same format the Editor/backtest already parse), not an empty file
 * masquerading as one; any other extension starts empty. Throws if the name already exists —
 * no silent overwrite, no auto-numbering surprise-rename.
 */
export async function createFile(dirPath: string, name: string): Promise<string> {
  const path = await join(dirPath, name)
  if (await exists(path)) throw new Error(`"${name}" already exists`)
  const content = name.toLowerCase().endsWith('.mallow')
    ? serializeMallowFile({ metadata: {}, script: '' })
    : ''
  await writeTextFile(path, content)
  return path
}

export async function createFolder(dirPath: string, name: string): Promise<string> {
  const path = await join(dirPath, name)
  if (await exists(path)) throw new Error(`"${name}" already exists`)
  await mkdir(path, { recursive: true })
  return path
}

/** Renames/moves `path` to `newName` within the same parent directory. A no-op rename (name
 *  unchanged) resolves immediately instead of hitting the exists-check — without this, it would
 *  false-positive on "already exists" since the target IS the source. */
export async function renameEntry(path: string, newName: string): Promise<string> {
  const parent = await dirname(path)
  const newPath = await join(parent, newName)
  if (newPath === path) return path
  if (await exists(newPath)) throw new Error(`"${newName}" already exists`)
  await rename(path, newPath)
  return newPath
}

export async function deleteEntry(path: string, isDirectory: boolean): Promise<void> {
  await remove(path, { recursive: isDirectory })
}

/**
 * Lists the real, immediate contents of `dir` — a genuine recursive-on-demand file tree (like
 * VSCode's Explorer), not a fixed 4-subdir assumption. Bug fix: the previous version only ever
 * looked for `data`/`output`/`strategies`/`notes` — correct for a project `createProject`
 * scaffolded, but empty for any real folder opened via `openProjectDialog` (e.g. an existing repo
 * with its own structure), which made the sidebar tree appear broken/empty for those.
 * Directories sort before files, then alphabetically — standard file-explorer convention.
 */
export async function listDir(dir: string): Promise<FileEntry[]> {
  const entries = await readDir(dir)
  const withPaths = await Promise.all(
    entries.map(async (e) => ({
      name: e.name,
      path: await join(dir, e.name),
      isDirectory: e.isDirectory,
    })),
  )
  return withPaths.sort((a, b) => {
    if (a.isDirectory !== b.isDirectory) return a.isDirectory ? -1 : 1
    return a.name.localeCompare(b.name)
  })
}
