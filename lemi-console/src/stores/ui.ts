import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { useMediaQuery } from '@vueuse/core'

function saved<T>(key: string, def: T): T {
  try {
    const v = localStorage.getItem('lemi.ui.' + key)
    if (v !== null) return JSON.parse(v)
  } catch {}
  return def
}

export const useUI = defineStore('ui', () => {
  const theme = ref<'system' | 'light' | 'dark'>(saved('theme', 'system'))
  const accent = ref<string>(saved('accent', 'blue'))
  const density = ref<'compact' | 'normal' | 'comfortable'>(saved('density', 'compact'))
  const leftWidth = ref<number>(saved('leftWidth', 280))
  const rightWidth = ref<number>(saved('rightWidth', 420))
  const leftOpen = ref<boolean>(saved('leftOpen', true))
  const aiOpen = ref<boolean>(saved('aiOpen', false))
  const rail = ref<string>(saved('rail', 'explorer'))
  const isMobile = useMediaQuery('(max-width: 760px)')
  const isTablet = useMediaQuery('(max-width: 1100px)')
  const prefersDark = useMediaQuery('(prefers-color-scheme: dark)')

  function applyTheme() {
    const el = document.documentElement
    const dark = theme.value === 'dark' || (theme.value === 'system' && prefersDark.value)
    el.dataset.theme = dark ? 'dark' : 'light'
    el.dataset.accent = accent.value
    el.dataset.density = density.value
  }
  watch([theme, accent, density, prefersDark], applyTheme, { immediate: true })
  for (const [k, r] of Object.entries({ theme, accent, density, leftWidth, rightWidth, leftOpen, aiOpen, rail })) {
    watch(r as any, (v) => { try { localStorage.setItem('lemi.ui.' + k, JSON.stringify(v)) } catch {} })
  }
  return { theme, accent, density, leftWidth, rightWidth, leftOpen, aiOpen, rail, isMobile, isTablet }
})
