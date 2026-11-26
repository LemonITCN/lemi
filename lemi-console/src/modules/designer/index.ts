// 表结构设计器标签：新建表 / 修改表结构，执行前预览生成的 DDL。
import { defineModule } from '@/modules/registry'
import { registerTreeAction } from '@/modules/explorer/actions'
import { useTabs } from '@/stores/tabs'
import type { Conn } from '@/stores/conns'
import { hasCap } from '@/lib/dialect'
import { i18n } from '@/i18n'

/** 打开设计器；不给 table 为新建表。 */
export function openDesigner(conn: Conn, database: string, table?: string) {
  const t = i18n.global.t
  if (table) {
    return useTabs().open('designer', { connectionId: conn.id, database, table }, { title: table, key: `designer:${conn.id}:${database}:${table}`, connectionId: conn.id })
  }
  return useTabs().open('designer', { connectionId: conn.id, database }, { title: t('designer.newTable'), connectionId: conn.id })
}

registerTreeAction(['database', 'table'], (ctx, kind) => {
  const { conn, database } = ctx
  if (!database || !hasCap(conn, 'ddl')) return []
  const t = i18n.global.t
  if (kind === 'database') return [{ label: t('designer.newTable'), icon: 'plus', run: () => openDesigner(conn, database) }]
  if (!ctx.name) return []
  const name = ctx.name
  return [{ label: t('designer.design'), icon: 'columns', run: () => openDesigner(conn, database, name) }]
}, 30)

export default defineModule({
  name: 'designer',
  tabs: [{ type: 'designer', component: () => import('./DesignerTab.vue'), icon: 'columns' }],
})
