// 对象定义（DDL）标签：只读高亮显示表、视图、例程、触发器、事件的创建语句。
import { defineModule } from '@/modules/registry'
import { registerTreeAction } from '@/modules/explorer/actions'
import { useTabs } from '@/stores/tabs'
import type { Conn } from '@/stores/conns'
import { i18n } from '@/i18n'

const kindIcon: Record<string, string> = { table: 'table', view: 'view', procedure: 'procedure', function: 'function', trigger: 'trigger', event: 'event' }

/** 打开某个对象的 DDL 标签（同一对象只开一个）。 */
export function openDDL(conn: Conn, database: string, kind: string, name: string) {
  return useTabs().open('ddl', { connectionId: conn.id, database, kind, name }, { title: name, key: `ddl:${conn.id}:${database}:${kind}:${name}`, connectionId: conn.id })
}

// 对象树右键：表与视图加「查看 DDL」（例程、触发器、事件双击即打开 DDL）
registerTreeAction(['table', 'view'], (ctx, kind) => {
  if (!ctx.database || !ctx.name) return []
  const { conn, database, name } = ctx
  return [{ label: i18n.global.t('ddl.view'), icon: 'code', run: () => openDDL(conn, database, kind, name) }]
}, 20)

export default defineModule({
  name: 'ddl',
  tabs: [{ type: 'ddl', component: () => import('./DdlTab.vue'), icon: (tab) => kindIcon[tab.params.kind] || 'code' }],
})
