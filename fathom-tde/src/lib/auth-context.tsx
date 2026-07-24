import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { registerHeraldFetch } from '@/service/data-service'

// Session lifecycle lives in Rust (src-tauri/src/auth/mod.rs) — the refresh token is an HttpOnly
// cookie the WebView can't be trusted to carry across native `invoke()` calls, so login/refresh/
// logout are all Tauri commands, not `fetch`. This context just mirrors whatever Rust reports:
// no token storage here at all, only the already-authenticated user + short-lived access token
// (kept in memory for a later Bearer-header use, once fathom calls other resource APIs).

export interface AuthUser {
  id: string
  email: string
  full_name: string
  display_name?: string | null
  avatar_url?: string | null
  role: string
  status: string
  email_verified: boolean
  last_login_at?: string | null
}

interface AuthToken {
  access_token: string
  token_type: string
  expires_in: number
  expires_at: number
}

interface AuthSession {
  user: AuthUser
  token: AuthToken
}

type AuthStatus = 'loading' | 'authenticated' | 'unauthenticated'

interface AuthContextType {
  status: AuthStatus
  user: AuthUser | null
  accessToken: string | null
  error: string | null
  login: (email: string, password: string) => Promise<void>
  loginWithGoogle: () => Promise<void>
  logout: () => Promise<void>
  /** Calls any hosted-hub resource API (helm/herald/…) — a thin `invoke('gateway_fetch', …)`
   *  wrapper. ALL the actual work (Bearer auth, gateway URL, envelope unwrap, one retry through
   *  `refresh_session` on a 401) happens in Rust (src-tauri/src/auth/mod.rs::gateway_fetch),
   *  the same file/pattern `auth_login`/`auth_refresh` already use — this app makes zero network
   *  calls from the TypeScript/webview side, full stop. Business panels (Helm/Hand console, the
   *  herald chart-data fallback, …) go through this instead of calling `fetch` directly so the
   *  network layer stays in exactly one place; none of those call sites needed to change when
   *  this moved from a JS `fetch` (via `@tauri-apps/plugin-http`, since removed) to Rust — they
   *  only ever depended on this function's `(path, init) => Promise<T>` shape. */
  apiFetch: <T>(path: string, init?: RequestInit) => Promise<T>
}

const AuthContext = createContext<AuthContextType | undefined>(undefined)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<AuthStatus>('loading')
  const [session, setSession] = useState<AuthSession | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Cold start: ask Rust whether a refresh token is already sitting in the OS keychain from a
  // previous run — `null` back means "not logged in" (never an error to surface, just go to the
  // Login screen).
  useEffect(() => {
    invoke<AuthSession | null>('auth_current_session')
      .then((s) => {
        setSession(s)
        setStatus(s ? 'authenticated' : 'unauthenticated')
      })
      .catch(() => setStatus('unauthenticated'))
  }, [])

  async function login(email: string, password: string) {
    setError(null)
    try {
      const s = await invoke<AuthSession>('auth_login', { email, password })
      setSession(s)
      setStatus('authenticated')
    } catch (err) {
      setError(typeof err === 'string' ? err : 'Sign-in failed')
      throw err
    }
  }

  async function loginWithGoogle() {
    setError(null)
    try {
      const s = await invoke<AuthSession>('auth_google_start')
      setSession(s)
      setStatus('authenticated')
    } catch (err) {
      setError(typeof err === 'string' ? err : 'Google sign-in failed')
      throw err
    }
  }

  async function logout() {
    await invoke('auth_logout').catch(() => {})
    setSession(null)
    setStatus('unauthenticated')
  }

  async function apiFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
    if (!session) throw new Error('not authenticated')
    return invoke<T>('gateway_fetch', {
      path,
      method: init.method ?? 'GET',
      // Every call site in this app passes a JSON string (or nothing) — see hand-api.ts/
      // BrokerPanel.tsx/HelmPanel.tsx — never FormData/binary, so this is safe to unconditionally
      // parse back into an object for the Rust side, which sends it as a real JSON body (not a
      // double-encoded string).
      body: init.body ? JSON.parse(init.body as string) : undefined,
    })
  }

  // Hand the authed fetch to data-service (plain module, no hooks) so its chart-data fallback
  // can reach hosted herald when a symbol has no local data yet. Re-registered whenever the
  // session changes so the closure always carries the current access token.
  useEffect(() => {
    registerHeraldFetch(session ? apiFetch : null)
    return () => registerHeraldFetch(null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session])

  return (
    <AuthContext.Provider
      value={{
        status,
        user: session?.user ?? null,
        accessToken: session?.token.access_token ?? null,
        error,
        login,
        loginWithGoogle,
        logout,
        apiFetch,
      }}
    >
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within an AuthProvider')
  return ctx
}
