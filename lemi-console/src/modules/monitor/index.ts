// 服务器监控标签：进程列表、全局状态曲线、变量、表大小、慢查询（SQLite 为文件状态、编译选项、表大小）。
import { defineModule } from '@/modules/registry'
import { registerTreeAction } from '@/modules/explorer/actions'
import { useTabs } from '@/stores/tabs'
import type { Conn } from '@/stores/conns'
import { dialectOf, hasCap } from '@/lib/dialect'
import { i18n } from '@/i18n'

/** 打开某个连接的监控标签（每个连接只开一个）。 */
export function openMonitor(conn: Conn, section?: string) {
  const t = i18n.global.t
  const title = `${conn.name} · ${dialectOf(conn) === 'sqlite' ? t('monitor.titleSqlite') : t('monitor.title')}`
  const params: Record<string, any> = { connectionId: conn.id }
  if (section) params.section = section
  return useTabs().open('monitor', params, { title, key: `monitor:${conn.id}`, connectionId: conn.id })
}

registerTreeAction(['connection'], (ctx) => {
  const { conn } = ctx
  if (!hasCap(conn, 'monitor')) return []
  const t = i18n.global.t
  return [{ label: dialectOf(conn) === 'sqlite' ? t('monitor.titleSqlite') : t('monitor.title'), icon: 'pulse', run: () => openMonitor(conn) }]
}, 50)

export default defineModule({
  name: 'monitor',
  tabs: [{ type: 'monitor', component: () => import('./MonitorTab.vue'), icon: 'pulse' }],
})
