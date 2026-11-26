// 对外入口：表格页、控制台结果、对象树、智能体都用这几个函数打开导入导出对话框与执行表操作。
import { openDialog } from '@/lib/dialogs'
import { api } from '@/api/client'
import { useConns, type Conn } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { confirmDialog } from '@/lib/confirm'
import { dialectOf } from '@/lib/dialect'
import { errorText, i18n } from '@/i18n'
import { dangerSQL } from './templates'

/** 导出表数据（可带当前筛选条件）或一条查询的结果。 */
export async function openExport(o: { conn: Conn; database: string; table?: string; where?: string; orderBy?: string; sql?: string }) {
  const comp = (await import('./ExportDialog.vue')).default
  return openDialog<boolean>(comp, o)
}

/** 把 CSV / TSV / Excel 导入到表；完成后返回导入的行数。 */
export async function openImport(o: { conn: Conn; database: string; table: string }) {
  const comp = (await import('./ImportDialog.vue')).default
  return openDialog<{ inserted: number }>(comp, o)
}

/** 整库或部分表导出为 SQL 文件。 */
export async function openDumpExport(o: { conn: Conn; database: string; tables?: string[] }) {
  const comp = (await import('./DumpExportDialog.vue')).default
  return openDialog<boolean>(comp, o)
}

/** 运行 SQL 文件。 */
export async function openRunSqlFile(o: { conn: Conn; database: string }) {
  const comp = (await import('./RunSqlFileDialog.vue')).default
  return openDialog<boolean>(comp, o)
}

/** 清空或删除表：输入表名确认，生产库再确认一次，经临时会话执行。 */
export async function dangerTableOp(conn: Conn, database: string, table: string, op: 'truncate' | 'drop'): Promise<boolean> {
  const t = i18n.global.t
  const toast = useToast()
  const d = dialectOf(conn)
  const sql = dangerSQL(op, database, table, d)
  const comp = (await import('./TypeConfirmDialog.vue')).default
  const ok = await openDialog<boolean>(comp, {
    title: t(op === 'drop' ? 'dataio.dropTitle' : 'dataio.truncateTitle', { name: table }),
    message: t(op === 'drop' ? 'dataio.dropHint' : 'dataio.truncateHint'),
    prompt: t('dataio.typeToConfirm', { name: table }), expected: table, sql,
    okText: t(op === 'drop' ? 'dataio.drop' : 'dataio.truncate').replace(/…$/, ''),
  })
  if (!ok) return false
  if (conn.env === 'prod' && !(await confirmDialog({ title: t('dataio.prodDangerTitle'), message: t('dataio.prodDangerHint', { conn: conn.name, sql }), danger: true, okText: t('common.confirm') }))) return false
  let sid = ''
  try {
    sid = (await api.post(`/connections/${conn.id}/sessions`, { db: database })).id
    const r = await api.post(`/sessions/${sid}/execute`, { sql })
    const bad = (r.results || []).find((x: any) => x.kind === 'error')
    if (bad) throw new Error(bad.error)
    toast.success(t(op === 'drop' ? 'dataio.dropped' : 'dataio.truncated', { name: table }))
  } catch (e) {
    toast.error(errorText(e))
    return false
  } finally {
    if (sid) api.del(`/sessions/${sid}`).catch(() => {})
  }
  const conns = useConns()
  conns.loadObjects(conn.id, database, true).catch(() => {})
  if (op === 'drop') {
    const tabs = useTabs()
    for (const x of [...tabs.tabs]) {
      const p = x.params || {}
      if (x.connectionId === conn.id && p.database === database && p.table === table && (x.type === 'table' || x.type === 'designer')) tabs.close(x.id)
    }
  }
  return true
}
