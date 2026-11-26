// 数据库账号与权限标签（MySQL）：账号列表、授权与回收、改口令、锁定、一键只读账号。只对有管理权限的连接开放。
import { defineModule } from '@/modules/registry'
import { registerTreeAction } from '@/modules/explorer/actions'
import { useTabs } from '@/stores/tabs'
import type { Conn } from '@/stores/conns'
import { hasCap } from '@/lib/dialect'
import { i18n } from '@/i18n'

/** 打开某个连接的账号与权限标签（每个连接只开一个）。 */
export function openDbUsers(conn: Conn) {
  return useTabs().open('dbusers', { connectionId: conn.id }, { title: `${conn.name} · ${i18n.global.t('dbusers.title')}`, key: `dbusers:${conn.id}`, connectionId: conn.id })
}

registerTreeAction(['connection'], (ctx) => {
  const { conn } = ctx
  if (!hasCap(conn, 'users') || conn.access !== 'manage') return []
  return [{ label: i18n.global.t('dbusers.title'), icon: 'users', run: () => openDbUsers(conn) }]
}, 55)

export default defineModule({
  name: 'dbusers',
  tabs: [{ type: 'dbusers', component: () => import('./DbUsersTab.vue'), icon: 'users' }],
})
