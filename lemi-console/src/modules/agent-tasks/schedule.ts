// 计划（与服务端 sched.Spec 同构）与界面文字。服务端也有一份中文描述，这里按界面语言重新生成。
import { i18n } from '@/i18n'

export interface Spec {
  kind: 'hourly' | 'daily' | 'weekly' | 'monthly' | 'cron' | 'manual'
  minute?: number
  time?: string
  weekdays?: number[]
  day?: number
  cron?: string
  timezone?: string
}

export const KINDS: Spec['kind'][] = ['daily', 'weekly', 'monthly', 'hourly', 'cron', 'manual']

export function defaultSpec(): Spec {
  return { kind: 'daily', time: '09:00', minute: 0, weekdays: [1], day: 1, cron: '0 9 * * 1-5', timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || '' }
}

/** 只留当前类型需要的字段，免得服务端校验到无关字段。 */
export function cleanSpec(s: Spec): Spec {
  const out: Spec = { kind: s.kind }
  if (s.kind !== 'manual' && s.timezone) out.timezone = s.timezone
  if (s.kind === 'hourly') out.minute = Number(s.minute) || 0
  if (s.kind === 'daily' || s.kind === 'weekly' || s.kind === 'monthly') out.time = s.time || '09:00'
  if (s.kind === 'weekly') out.weekdays = [...(s.weekdays || [])].sort()
  if (s.kind === 'monthly') out.day = Number(s.day) || 1
  if (s.kind === 'cron') out.cron = (s.cron || '').trim()
  return out
}

export function describe(s: Spec | undefined): string {
  const t = (k: string, a?: Record<string, unknown>) => String(i18n.global.t('tasks.desc.' + k, a || {}))
  if (!s) return ''
  const wd = (n: number) => String(i18n.global.t('tasks.wd.' + n))
  switch (s.kind) {
    case 'manual': return t('manual')
    case 'hourly': return t('hourly', { m: String(s.minute || 0).padStart(2, '0') })
    case 'daily': return t('daily', { t: s.time })
    case 'weekly': return t('weekly', { d: (s.weekdays || []).slice().sort().map(wd).join(i18n.global.locale.value === 'en' ? ', ' : '、'), t: s.time })
    case 'monthly': return t('monthly', { d: s.day, t: s.time })
    case 'cron': return t('cron', { c: s.cron })
  }
  return ''
}

export function timezones(): string[] {
  try {
    const list = (Intl as any).supportedValuesOf?.('timeZone') as string[] | undefined
    if (list?.length) return list
  } catch { /* 老浏览器 */ }
  return ['Asia/Shanghai', 'Asia/Tokyo', 'Asia/Singapore', 'Europe/London', 'Europe/Berlin', 'America/New_York', 'America/Los_Angeles', 'UTC']
}
