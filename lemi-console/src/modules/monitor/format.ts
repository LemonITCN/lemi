// 监控页的数值格式化与接口小工具。
import { api } from '@/api/client'
import { i18n, isZh } from '@/i18n'

export type MonitorWhat = 'processes' | 'status' | 'variables' | 'tables' | 'slow'

export interface MonitorResp<T> { data: T; at: number }

export function fetchMonitor<T = any>(connId: string, what: MonitorWhat, query: Record<string, string | number> = {}): Promise<MonitorResp<T>> {
  const q = new URLSearchParams(Object.entries(query).map(([k, v]) => [k, String(v)])).toString()
  return api.get(`/connections/${connId}/monitor/${what}${q ? '?' + q : ''}`)
}

export function num(v: unknown): number {
  const n = typeof v === 'number' ? v : Number(v)
  return isFinite(n) ? n : 0
}

/** 千分位整数（表格列用）。 */
export function formatInt(v: unknown): string {
  if (v === null || v === undefined || v === '') return '—'
  const n = Number(v)
  if (!isFinite(n)) return String(v)
  return Math.round(n).toLocaleString(isZh() ? 'zh-CN' : 'en-US')
}

/** 紧凑数值：1,284 / 12.9K / 4.2M（坐标轴与卡片用）。 */
export function formatCompact(v: number, digits = 1): string {
  const a = Math.abs(v)
  if (a < 1000) return a < 10 && a % 1 ? v.toFixed(digits) : String(Math.round(v * 10) / 10)
  const units = ['K', 'M', 'B', 'T']
  let x = v
  let i = -1
  while (Math.abs(x) >= 1000 && i < units.length - 1) { x /= 1000; i++ }
  return (Math.abs(x) < 10 ? x.toFixed(digits) : Math.round(x).toString()) + units[i]
}

/** 每秒字节数。 */
export function formatRate(bytesPerSec: number): string {
  const u = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  let v = bytesPerSec
  let i = 0
  while (Math.abs(v) >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return (i === 0 ? Math.round(v) : v.toFixed(v < 10 ? 1 : 0)) + ' ' + u[i]
}

/** 毫秒耗时：850 ms / 1.2 s / 3.5 min / 2.1 h。 */
export function formatMs(v: unknown): string {
  const ms = num(v)
  if (ms < 1) return ms ? ms.toFixed(2) + ' ms' : '0 ms'
  if (ms < 1000) return (ms < 10 ? ms.toFixed(1) : Math.round(ms)) + ' ms'
  const s = ms / 1000
  if (s < 60) return s.toFixed(s < 10 ? 2 : 1) + ' s'
  if (s < 3600) return (s / 60).toFixed(1) + ' min'
  return (s / 3600).toFixed(1) + ' h'
}

/** 秒数 → 运行时长（3 天 4 小时 / 2h 5m）。 */
export function formatUptime(sec: unknown): string {
  const s = Math.floor(num(sec))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const t = i18n.global.t
  if (d) return t('monitor.uptimeDH', { d, h })
  if (h) return t('monitor.uptimeHM', { h, m })
  return t('monitor.uptimeMS', { m, s: s % 60 })
}

export function formatClock(ms: number, seconds = true): string {
  const d = new Date(ms)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}` + (seconds ? ':' + p(d.getSeconds()) : '')
}
