// The theme is a personal preference: an explicit choice wins, the system
// preference is the fallback, and it is kept in the browser. Nothing about it
// belongs on the server, so this round adds no storage for it.

export type ThemeChoice = 'system' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

// The inline script in index.html reads the same key before the bundle runs, so
// the first paint is already correct rather than flashing the other theme.
// Keep the two in step.
export const THEME_KEY = 'atoms-theme'

export function readChoice(): ThemeChoice {
  const raw = localStorage.getItem(THEME_KEY)
  return raw === 'light' || raw === 'dark' ? raw : 'system'
}

function prefersLight(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-color-scheme: light)').matches
}

export function resolve(choice: ThemeChoice): ResolvedTheme {
  if (choice === 'system') return prefersLight() ? 'light' : 'dark'
  return choice
}

// apply stamps the resolved theme on <html>; every colour in styles.css reads
// from it through the token set, so this is the whole of the switch.
export function apply(choice: ThemeChoice): ResolvedTheme {
  const theme = resolve(choice)
  document.documentElement.dataset.theme = theme
  return theme
}

export function save(choice: ThemeChoice): void {
  if (choice === 'system') localStorage.removeItem(THEME_KEY)
  else localStorage.setItem(THEME_KEY, choice)
}

// watchSystem re-applies the theme when the system preference changes, so
// "follow the system" keeps following it rather than freezing at load time.
export function watchSystem(choice: ThemeChoice, onChange: () => void): () => void {
  if (typeof window.matchMedia !== 'function') return () => {}
  const media = window.matchMedia('(prefers-color-scheme: light)')
  const listener = () => {
    if (choice === 'system') onChange()
  }
  media.addEventListener('change', listener)
  return () => media.removeEventListener('change', listener)
}
