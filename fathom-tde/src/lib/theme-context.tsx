import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

// Mechanism ported from mallow-client/lib/auth-context.tsx:44-76 — standalone here since
// fathom has no auth system to couple theme state to. No mounted-guard: Vite is client-only
// (no SSR/hydration mismatch to avoid), unlike the Next.js original.

export type ThemeMode = 'foundation' | 'midnight'

export type ThemePalette =
  | 'navy' | 'purple' | 'charcoal' | 'neon' | 'orange'
  | 'crimson' | 'rose' | 'amber' | 'matrix' | 'cyberpunk'
  | 'chrome' | 'emerald' | 'mono'

export const THEME_PALETTES: ThemePalette[] = [
  'navy', 'purple', 'charcoal', 'neon', 'orange',
  'crimson', 'rose', 'amber', 'matrix', 'cyberpunk',
  'chrome', 'emerald', 'mono',
]

/** Corner mode decoupled from palette — 'auto' keeps each palette's native radius (mono square,
 *  the rest rounded); the explicit modes force it via `.corners-*` overrides in index.css. */
export type CornerStyle = 'auto' | 'rounded' | 'square'

export const CORNER_STYLES: CornerStyle[] = ['auto', 'rounded', 'square']

interface ThemeContextType {
  themeMode: ThemeMode
  themePalette: ThemePalette
  cornerStyle: CornerStyle
  setThemeMode: (mode: ThemeMode) => void
  setThemePalette: (palette: ThemePalette) => void
  setCornerStyle: (style: CornerStyle) => void
}

const ThemeContext = createContext<ThemeContextType | undefined>(undefined)

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [themeMode, setThemeMode] = useState<ThemeMode>('midnight')
  const [themePalette, setThemePalette] = useState<ThemePalette>('navy')
  const [cornerStyle, setCornerStyle] = useState<CornerStyle>('auto')

  useEffect(() => {
    const savedMode = localStorage.getItem('themeMode') as ThemeMode | null
    const savedPalette = localStorage.getItem('themePalette') as ThemePalette | null
    const savedCorners = localStorage.getItem('themeCorners') as CornerStyle | null
    if (savedMode === 'foundation' || savedMode === 'midnight') setThemeMode(savedMode)
    if (savedPalette && THEME_PALETTES.includes(savedPalette)) setThemePalette(savedPalette)
    if (savedCorners && CORNER_STYLES.includes(savedCorners)) setCornerStyle(savedCorners)
  }, [])

  useEffect(() => {
    const variant = themeMode === 'midnight' ? 'dark' : 'light'
    const corners = cornerStyle === 'auto' ? '' : ` corners-${cornerStyle}`
    document.documentElement.className = `${themePalette}-${variant}${corners}`
    localStorage.setItem('themeMode', themeMode)
    localStorage.setItem('themePalette', themePalette)
    localStorage.setItem('themeCorners', cornerStyle)
  }, [themeMode, themePalette, cornerStyle])

  return (
    <ThemeContext.Provider
      value={{ themeMode, themePalette, cornerStyle, setThemeMode, setThemePalette, setCornerStyle }}
    >
      {children}
    </ThemeContext.Provider>
  )
}

export function useTheme() {
  const ctx = useContext(ThemeContext)
  if (!ctx) throw new Error('useTheme must be used within a ThemeProvider')
  return ctx
}
