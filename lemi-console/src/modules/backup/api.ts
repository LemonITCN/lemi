// 备份模块共用：接口类型、格式化、实时进度。
import { reactive } from 'vue'
import { on } from '@/lib/realtime'
import type { Spec } from '@/modules/agent-tasks/schedule'

export interface Retention { mode: '' | 'count' | 'days' | 'gfs'; count?: number; days?: number; hourly?: number; daily?: number; weekly?: number; monthly?: number; yearly?: number }
export interface VerifyConfig { after_backup: boolean; schedule?: Spec | null }
export interface Plan {
  id: string; workspace_id: string; connection_id: string; name: string; enabled: boolean; method: string; databases: string[]
  compression: string; compression_level: number; encrypt: boolean; passphrase_set: boolean; targets: string[]; schedule: Spec; schedule_text: string
  retention: Retention; verify: VerifyConfig; notify_success: string[]; notify_failure: string[]
  next_run_at: number; retry_at: number; retry_attempt: number; next_verify_at: number; busy?: string; created_at: number; updated_at: number
}
export interface Run {
  id: string; workspace_id: string; plan_id: string; plan_name: string; connection_id: string; connection_name: string; driver: string
  trigger: string; attempt: number; status: string; method: string; tool: string; server_version: string; databases: string[]; file_name: string
  compression: string; encrypted: boolean; size: number; raw_size: number; sha256: string; warnings: string[]; error: string; created_by: string
  started_at: number; finished_at: number; verify_status: string; verify_at: number; verify?: any; manifest?: any; files?: FileRec[]; progress?: Progress
}
export interface FileRec { run_id: string; target_id: string; path: string; size: number; status: string; error: string }
export interface Target {
  id: string; name: string; kind: string; config?: Record<string, any>; secrets_set?: string[]; usage?: { files: number; bytes: number }; created_at: number; updated_at: number
}
export interface TargetKind {
  id: string; name: string; name_en: string; admin_only?: boolean; network: boolean; oauth?: boolean
  fields: { key: string; label: string; label_en?: string; type: string; required?: boolean; secret?: boolean; default?: any; placeholder?: string; help?: string; options?: string[] }[]
}
export interface Progress {
  kind: 'backup' | 'restore' | 'verify'; id: string; workspace_id: string; connection_id: string; plan_id: string; phase: string
  bytes: number; written: number; total: number; rows: number; database: string; table: string; tables_done: number; tables_total: number
  statements: number; errors: number; speed: number; elapsed_ms: number; done?: boolean; status?: string; error?: string; run_id?: string
}

export function formatBytes(n: number | undefined): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

export function formatDuration(ms: number): string {
  if (!ms || ms < 0) return '—'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${Math.max(s, ms > 0 ? 1 : 0)}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${s % 60}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

export const statusClass: Record<string, string> = { success: 'success', failed: 'danger', canceled: 'warning', running: 'accent', ok: 'success' }

/** 实时进度：任务 ID → 最新进度（备份、还原、校验）。done 后保留几秒再移除，界面据此刷新。 */
export const live = reactive<Record<string, Progress>>({})
const listeners = new Set<(p: Progress) => void>()
let started = false

export function watchProgress() {
  if (started) return
  started = true
  on('backup_progress', (p: Progress) => {
    if (!p?.id) return
    live[p.id] = p
    listeners.forEach((fn) => fn(p))
    if (p.done) setTimeout(() => { if (live[p.id]?.done) delete live[p.id] }, 4000)
  })
}

/** 订阅进度事件（组件卸载时调用返回的函数取消）。 */
export function onProgress(fn: (p: Progress) => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

/** 某连接正在运行的备份进度。 */
export function liveForConn(connId: string, kind = 'backup'): Progress | undefined {
  return Object.values(live).find((p) => p.connection_id === connId && p.kind === kind && !p.done)
}

export function percent(p: Progress | undefined): number | null {
  if (!p) return null
  if (p.tables_total > 0) return Math.min(100, Math.round((p.tables_done / p.tables_total) * 100))
  if (p.total > 0) return Math.min(100, Math.round(((p.kind === 'backup' ? p.bytes : p.written) / p.total) * 100))
  return null
}
