import { useSyncExternalStore } from 'react'

export type Theme = 'deep' | 'light'
const storageKey = 'agentforge.theme'
const changeEvent = 'agentforge:theme'

function applyTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme
  document.documentElement.style.colorScheme = theme === 'deep' ? 'dark' : 'light'
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme === 'deep' ? '#080f1c' : '#f2f5fa')
}

export function setTheme(theme: Theme) {
  applyTheme(theme)
  try { localStorage.setItem(storageKey, theme) } catch { /* Keep the selection for this page when storage is unavailable. */ }
  window.dispatchEvent(new Event(changeEvent))
}

function subscribe(callback: () => void) {
  const onStorage = (event: StorageEvent) => {
    if (event.key !== storageKey && event.key !== null) return
    applyTheme(event.newValue === 'light' ? 'light' : 'deep')
    callback()
  }
  window.addEventListener(changeEvent, callback)
  window.addEventListener('storage', onStorage)
  return () => {
    window.removeEventListener(changeEvent, callback)
    window.removeEventListener('storage', onStorage)
  }
}

export function useTheme(): Theme {
  return useSyncExternalStore(subscribe, () => document.documentElement.dataset.theme === 'light' ? 'light' : 'deep')
}
