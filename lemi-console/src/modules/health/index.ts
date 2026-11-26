// 健康检查：每个连接的状态、色块时间线、延迟曲线、可用率与检查设置；对象树连接上的状态点（首次加载 + 实时事件）。
import { getCurrentInstance, watch } from 'vue'
import type { Router } from 'vue-router'
import { defineModule } from '@/modules/registry'
import { registerTreeAction } from '@/modules/explorer/actions'
import { api } from '@/api/client'
import { on } from '@/lib/realtime'
import { useConns } from '@/stores/conns'
import { useSession } from '@/stores/session'
import { i18n } from '@/i18n'

let router: Router | null = null

/** 读当前空间全部连接的健康状态，填进连接缓存（对象树状态点用）。 */
export async function loadHealth() {
  const session = useSession()
  const conns = useConns()
  const ws = session.current?.id
  if (!ws) return
  try {
    const r = await api.get(`/workspaces/${ws}/health`)
    const out: Record<string, { status: string; latency_ms?: number; error?: string }> = {}
    for (const it of r.connections || []) {
      if (it.enabled) out[it.connection_id] = { status: it.status, latency_ms: it.last_latency_ms, error: it.last_error }
    }
    conns.health = out
  } catch { /* 没有权限或网络问题时不显示状态点 */ }
}

registerTreeAction(['connection'], (ctx) => [
  { label: i18n.global.t('health.tree'), icon: 'pulse', run: () => router?.push('/health/' + ctx.conn.id) },
], 61)

export default defineModule({
  name: 'health',
  routes: [{ path: 'health/:id?', name: 'health', component: () => import('./HealthView.vue') }],
  rail: [{ id: 'health', icon: 'pulse', label: 'health.title', order: 41, route: '/health' }],
  setup: () => {
    router = (getCurrentInstance()?.appContext.config.globalProperties as any)?.$router || null
    const session = useSession()
    const conns = useConns()
    on('health', (d: any) => {
      if (!d?.connection_id || d.workspace_id !== session.current?.id) return
      conns.health = { ...conns.health, [d.connection_id]: { status: d.status, latency_ms: d.latency_ms, error: d.error } }
    })
    // 实时通道断开重连后状态可能错过了变化，重新读一次
    on('open', () => loadHealth())
    watch(() => session.current?.id, () => loadHealth(), { immediate: true })
  },
})
