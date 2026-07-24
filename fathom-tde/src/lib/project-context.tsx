import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import {
  createProjectAt,
  getActiveProjectPath,
  loadProjects,
  openProjectDialog,
  setActiveProjectPath,
  type ProjectEntry,
} from './projects'

// Single source of truth for the project list + which one is "active" — both TitleBar's project
// switcher and Sidebar's project tree read from here instead of each keeping their own local
// state (which is what Sidebar did before this), so creating/opening/removing a project in either
// place is immediately visible in the other.

interface ProjectContextType {
  projects: ProjectEntry[]
  activeProject: ProjectEntry | null
  setActive: (path: string) => void
  /** New Project form path (ProjectDialog): creates `parentDir/name` + scaffold, registers,
   *  activates. Throws (with a user-readable message) when the folder already exists. */
  createNamedProject: (parentDir: string, name: string) => Promise<void>
  openExistingProject: () => Promise<void>
  removeProject: (path: string) => void
}

const ProjectContext = createContext<ProjectContextType | undefined>(undefined)

export function ProjectProvider({ children }: { children: ReactNode }) {
  const [projects, setProjects] = useState<ProjectEntry[]>([])
  const [activePath, setActivePath] = useState<string | null>(null)

  const refresh = useCallback(() => {
    loadProjects().then(setProjects)
  }, [])

  useEffect(() => {
    refresh()
    setActivePath(getActiveProjectPath())
  }, [refresh])

  const setActive = useCallback((path: string) => {
    setActiveProjectPath(path)
    setActivePath(path)
  }, [])

  async function createNamedProject(parentDir: string, name: string) {
    const next = await createProjectAt(parentDir, name)
    setProjects(next)
    setActive(next[next.length - 1].path)
  }

  async function openExistingProject() {
    const next = await openProjectDialog()
    if (!next) return
    setProjects(next)
    setActive(next[next.length - 1].path)
  }

  function removeProject(path: string) {
    setProjects((prev) => prev.filter((p) => p.path !== path))
    if (activePath === path) setActivePath(null)
    // Registry file itself is only rewritten on next create/open — acceptable for phase 1
    // (removing from view doesn't delete the folder or touch disk).
  }

  const activeProject = projects.find((p) => p.path === activePath) ?? null

  return (
    <ProjectContext.Provider
      value={{ projects, activeProject, setActive, createNamedProject, openExistingProject, removeProject }}
    >
      {children}
    </ProjectContext.Provider>
  )
}

export function useProjects() {
  const ctx = useContext(ProjectContext)
  if (!ctx) throw new Error('useProjects must be used within a ProjectProvider')
  return ctx
}
