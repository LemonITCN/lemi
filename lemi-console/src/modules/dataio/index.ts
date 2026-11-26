// 数据导入导出与表操作：导出数据、导入 CSV/Excel、整库导出 SQL 文件、运行 SQL 文件、复制语句模板、清空与删除表。
import { defineModule } from '@/modules/registry'
import { registerTreeAction } from '@/modules/explorer/actions'
import type { Conn } from '@/stores/conns'
import type { MenuItem } from '@/components/ui/Menu.vue'
import { useToast } from '@/stores/toast'
import { captureAppContext } from '@/lib/dialogs'
import { dialectOf } from '@/lib/dialect'
import { fetchTable } from '@/lib/meta'
import { copyText } from '@/lib/clipboard'
import { errorText, i18n } from '@/i18n'
import { dangerTableOp, openDumpExport, openExport, openImport, openRunSqlFile } from './open'
import { insertTemplate, selectTemplate } from './templates'

const t = (k: string) => i18n.global.t(k)
const canWrite = (c: Conn) => !c.read_only && c.access !== 'read'

async function copyTemplate(conn: Conn, db: string, table: string, kind: 'select' | 'insert') {
  const toast = useToast()
  try {
    const info = await fetchTable(conn.id, db, table)
    const d = dialectOf(conn)
    const text = kind === 'select' ? selectTemplate(table, info.columns, d) : insertTemplate(table, info.columns, d)
    if (await copyText(text)) toast.success(t('dataio.copiedTemplate'))
  } catch (e) { toast.error(errorText(e)) }
}

registerTreeAction(['database'], (ctx) => {
  const db = ctx.database!
  const items: MenuItem[] = [{ label: t('dataio.exportSqlFile'), icon: 'download', run: () => openDumpExport({ conn: ctx.conn, database: db }) }]
  if (canWrite(ctx.conn)) items.push({ label: t('dataio.runSqlFile'), icon: 'play', run: () => openRunSqlFile({ conn: ctx.conn, database: db }) })
  return items
}, 60)

registerTreeAction(['table', 'view'], (ctx, kind) => {
  const db = ctx.database!
  const name = ctx.name!
  const items: MenuItem[] = [
    { label: t('dataio.exportData'), icon: 'download', run: () => openExport({ conn: ctx.conn, database: db, table: name }) },
    { label: t('dataio.exportSqlFile'), icon: 'file', run: () => openDumpExport({ conn: ctx.conn, database: db, tables: [name] }) },
  ]
  if (kind === 'table' && canWrite(ctx.conn)) items.push({ label: t('dataio.importData'), icon: 'upload', run: () => openImport({ conn: ctx.conn, database: db, table: name }) })
  return items
}, 70)

registerTreeAction(['table', 'view'], (ctx, kind) => {
  const items: MenuItem[] = [{ label: t('dataio.copySelect'), icon: 'copy', run: () => copyTemplate(ctx.conn, ctx.database!, ctx.name!, 'select') }]
  if (kind === 'table') items.push({ label: t('dataio.copyInsert'), icon: 'copy', run: () => copyTemplate(ctx.conn, ctx.database!, ctx.name!, 'insert') })
  return items
}, 80)

registerTreeAction(['table'], (ctx) => {
  if (!canWrite(ctx.conn)) return []
  return [
    { label: t('dataio.truncate'), icon: 'filter-x', danger: true, run: () => dangerTableOp(ctx.conn, ctx.database!, ctx.name!, 'truncate') },
    { label: t('dataio.drop'), icon: 'trash', danger: true, run: () => dangerTableOp(ctx.conn, ctx.database!, ctx.name!, 'drop') },
  ]
}, 90)

export default defineModule({
  name: 'dataio',
  setup: () => captureAppContext(),
})
