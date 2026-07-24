import { ThemeProvider } from '@/lib/theme-context'
import { ProjectProvider } from '@/lib/project-context'
import { EditorProvider } from '@/lib/editor-context'
import { PanelProvider } from '@/lib/panel-context'
import { RunConfigProvider } from '@/lib/run-config-context'
import { HandViewProvider } from '@/lib/hand-view-context'
import { AuthProvider, useAuth } from '@/lib/auth-context'
import { AppShell } from '@/components/layout/AppShell'
import { DockArea } from '@/components/dock/DockArea'
import { Login } from '@/screens/Login'
import { Spinner } from '@/components/ui/spinner'

// Gated the same way mallow-client's dashboard layout gates on `isAuthenticated`
// (app/dashboard/layout.tsx) — ProjectProvider/AppShell only ever mount for a signed-in user, so
// nothing downstream (project file tree, editor, agent) has to handle a logged-out state itself.
function Gate() {
  const { status } = useAuth()

  if (status === 'loading') {
    return (
      <div data-tauri-drag-region className="flex h-screen w-screen items-center justify-center bg-background">
        <Spinner className="h-5 w-5 text-muted-foreground" />
      </div>
    )
  }

  if (status === 'unauthenticated') {
    return <Login />
  }

  return (
    <ProjectProvider>
      <EditorProvider>
        <PanelProvider>
          {/* Above AppShell, not inside DockArea: BottomRail (shell chrome) and ConfigPanel
              (a dock panel) both consume run-config state from different subtrees. */}
          <RunConfigProvider>
            {/* Same reasoning: HelmPanel lives in RightSidebar, a sibling of DockArea — a
                Provider scoped to DockArea's own tree wouldn't reach it. */}
            <HandViewProvider>
              <AppShell>
                <DockArea />
              </AppShell>
            </HandViewProvider>
          </RunConfigProvider>
        </PanelProvider>
      </EditorProvider>
    </ProjectProvider>
  )
}

function App() {
  return (
    <ThemeProvider>
      <AuthProvider>
        <Gate />
      </AuthProvider>
    </ThemeProvider>
  )
}

export default App
