// ER 图标签：选库里的表，按外键生成 Mermaid erDiagram，可导出 SVG / PNG。
import { defineModule } from '@/modules/registry'
import { registerTreeAction } from '@/modules/explorer/actions'
import { useTabs } from '@/stores/tabs'
import type { Conn } from '@/stores/conns'
import { hasCap } from '@/lib/dialect'
import { i18n } from '@/i18n'

/** 打开某个库的 ER 图（同一库只开一个）。 */
export function openErd(conn: Conn, database: string, tables?: string[]) {
  const params: Record<string, any> = { connectionId: conn.id, database }
  if (tables?.length) params.tables = tables
  return useTabs().open('erd', params, { title: `${database} · ER`, key: `erd:${conn.id}:${database}`, connectionId: conn.id })
}

registerTreeAction(['database'], (ctx) => {
  const { conn, database } = ctx
  if (!database || !hasCap(conn, 'introspect')) return []
  return [{ label: i18n.global.t('erd.open'), icon: 'erd', run: () => openErd(conn, database) }]
}, 40)

export default defineModule({
  name: 'erd',
  tabs: [{ type: 'erd', component: () => import('./ErdTab.vue'), icon: 'erd' }],
})
