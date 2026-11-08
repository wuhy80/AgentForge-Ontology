import { MoonStar, Sun } from 'lucide-react'
import { setTheme, useTheme } from '../lib/theme'

export function ThemeSwitcher() {
  const theme = useTheme()
  return <div className="theme-switcher" role="group" aria-label="界面主题">
    <button type="button" aria-label="深空蓝" aria-pressed={theme === 'deep'} onClick={() => setTheme('deep')} title="深空蓝主题"><MoonStar size={14} /><span>深空蓝</span></button>
    <button type="button" aria-label="精密白" aria-pressed={theme === 'light'} onClick={() => setTheme('light')} title="精密白主题"><Sun size={14} /><span>精密白</span></button>
  </div>
}
