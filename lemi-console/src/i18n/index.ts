// 多语言：每个模块在 zh-CN/<模块>.ts 与 en/<模块>.ts 里各放一份词条，按文件名作为命名空间自动合并，
// 增加模块不需要改动本文件。
import { createI18n } from 'vue-i18n'
import { ApiError } from '@/api/client'

export type Locale = 'zh-CN' | 'en'

function collect(mods: Record<string, any>) {
  const out: Record<string, any> = {}
  for (const [path, m] of Object.entries(mods)) {
    const name = path.split('/').pop()!.replace(/\.ts$/, '')
    out[name] = m.default
  }
  return out
}

const zh = collect(import.meta.glob('./zh-CN/*.ts', { eager: true }))
const en = collect(import.meta.glob('./en/*.ts', { eager: true }))

function initialLocale(): Locale {
  try {
    const saved = localStorage.getItem('lemi.locale')
    if (saved === 'en' || saved === 'zh-CN') return saved
  } catch {}
  return navigator.language?.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
}

// 词条按模块动态收集，类型推断没有意义（还会让 TypeScript 推断过深），这里统一按 any 处理
export const i18n: any = createI18n({ legacy: false, locale: initialLocale(), fallbackLocale: 'zh-CN', messages: { 'zh-CN': zh, en } as any })

export function setLocale(l: string) {
  const loc: Locale = l === 'en' ? 'en' : 'zh-CN'
  i18n.global.locale.value = loc
  document.documentElement.lang = loc
  try { localStorage.setItem('lemi.locale', loc) } catch {}
}

export function isZh() { return i18n.global.locale.value !== 'en' }

/** 把接口错误翻译成当前语言的提示：英文界面优先用词条，其余用服务端的中文说明。 */
export function errorText(e: unknown): string {
  const t = i18n.global.t
  if (e instanceof ApiError) {
    const key = 'errors.' + e.code
    if (!isZh() && i18n.global.te(key)) return t(key)
    return e.message || (i18n.global.te(key) ? t(key) : e.code)
  }
  if (e instanceof Error) return e.message
  return String(e)
}
