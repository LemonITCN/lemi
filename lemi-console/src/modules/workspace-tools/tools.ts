// 智能体操作主工作区的浏览器端工具（docs/spec.md §16.5.1）。服务端经实时通道发来 client_call，这里在用户的浏览器里执行。
// 标签的可读状态与动作由各标签组件登记在 stores/tabstate.ts（表格：update / loaded；控制台：run）。
import { useConns, openers, type Conn } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { readTabState, tabAction } from '@/stores/tabstate'
import { confirmDialog } from '@/lib/confirm'
import { dialectOf } from '@/lib/dialect'
import { splitSQL } from '@/lib/sqlsplit'
import { i18n } from '@/i18n'
import { isReadOnlySQL } from './classify'

const SAMPLE_ROWS = 20
const MAX_SELECTION_ROWS = 200
const MAX_CELL_CHARS = 500

/** 单元格文本过长（长文本、二进制的十六进制）时截断，免得把大块内容塞给模型。 */
function clipRows(rows: any[][] | undefined, n: number): string[][] {
  return (rows || []).slice(0, n).map((r) => r.map((v) => {
    const s = v === null || v === undefined ? 'NULL' : String(v)
    return s.length > MAX_CELL_CHARS ? s.slice(0, MAX_CELL_CHARS) + '…' : s
  }))
}
const t = (k: string, a?: any) => i18n.global.t(k, a)

class ToolError extends Error {}

async function connOf(id: string): Promise<Conn> {
  const conns = useConns()
  if (!conns.list.length) await conns.load()
  const c = conns.byId[id]
  if (!c) throw new ToolError(t('wstools.noConn', { id }))
  return c
}

/** 等标签组件挂载并登记状态（标签组件是异步组件，控制台还要先开会话）。 */
async function waitState(tabId: string, timeoutMs = 20000): Promise<any> {
  const end = Date.now() + timeoutMs
  for (;;) {
    const s = readTabState(tabId)
    if (s) return s
    if (!useTabs().tabs.some((x) => x.id === tabId)) throw new ToolError(t('wstools.tabClosed'))
    if (Date.now() > end) throw new ToolError(t('wstools.timeout'))
    await new Promise((r) => setTimeout(r, 80))
  }
}

/** 等表格的当前加载完成。 */
async function waitTableLoaded(tabId: string) {
  const end = Date.now() + 60000
  for (;;) {
    const p = tabAction(tabId, 'loaded')?.()
    if (p) { await p; return }
    if (Date.now() > end) throw new ToolError(t('wstools.timeout'))
    await new Promise((r) => setTimeout(r, 80))
  }
}

function pickColumns(s: any, want?: string[]) {
  const cols: string[] = s.columns || []
  const sample: string[][] = clipRows(s.visible_rows_sample, SAMPLE_ROWS)
  if (!want?.length) return { columns: cols, sample }
  const idx = want.map((w) => cols.findIndex((c) => c.toLowerCase() === w.toLowerCase())).filter((i) => i >= 0)
  if (!idx.length) return { columns: cols, sample }
  return { columns: idx.map((i) => cols[i]), sample: sample.map((r) => idx.map((i) => r[i])) }
}

function tableSummary(tabId: string, want?: string[]) {
  const s = readTabState(tabId)
  if (!s) throw new ToolError(t('wstools.tabClosed'))
  if (s.error) return { tab_id: tabId, error: s.error }
  const { columns, sample } = pickColumns(s, want)
  return { tab_id: tabId, db: s.db, table: s.table, where: s.where || '', order_by: s.order_by || '', offset: s.offset || 0,
    rows_loaded: s.total_loaded || 0, has_more: !!s.has_more, columns, sample }
}

function limitSelection(sel: any) {
  if (!sel) return { columns: [], rows: [] }
  const rows: any[][] = sel.rows || []
  return { columns: sel.columns || [], rows: clipRows(rows, MAX_SELECTION_ROWS), truncated: rows.length > MAX_SELECTION_ROWS || undefined }
}

function wrap<A>(fn: (a: A) => Promise<any>) {
  return async (args: A) => {
    try {
      return await fn(args || ({} as A))
    } catch (e: any) {
      return { error: e?.message || String(e) }
    }
  }
}

export interface OpenTableArgs { connection_id: string; db: string; table: string; where?: string; order_by?: string; columns?: string[] }
export interface RunSQLArgs { connection_id: string; db?: string; sql: string; title?: string; confirmed?: boolean }
export interface UpdateViewArgs { tab_id?: string; where?: string; order_by?: string; columns?: string[]; scroll_to_row?: number }

export const workspace_open_table = wrap(async (a: OpenTableArgs) => {
  if (!a.connection_id || !a.table) throw new ToolError(t('wstools.badArgs'))
  const c = await connOf(a.connection_id)
  const db = a.db || (dialectOf(c) === 'sqlite' ? 'main' : '')
  const tabs = useTabs()
  const key = `table:${c.id}:${db}:${a.table}`
  const ex = tabs.tabs.find((x) => x.key === key)
  if (ex && readTabState(ex.id)) {
    // 已经打开：激活后用标签自己的 update 动作改条件并等加载完成（不经参数监听，避免重复加载）
    tabs.activate(ex.id)
    tabs.update(ex.id, { fromAI: true })
    const s = readTabState(ex.id)
    const changing = (a.where !== undefined && a.where !== s.where) || (a.order_by !== undefined && a.order_by !== s.order_by)
    if (changing && s.pending_changes > 0) throw new ToolError(t('wstools.pending', { n: s.pending_changes }))
    if (a.columns) tabs.setParams(ex.id, { columns: a.columns })
    if (changing) await tabAction(ex.id, 'update')!({ where: a.where, order_by: a.order_by })
    else await waitTableLoaded(ex.id)
    return tableSummary(ex.id, a.columns)
  }
  const params: Record<string, any> = {}
  if (a.where !== undefined) params.where = a.where
  if (a.order_by !== undefined) params.orderBy = a.order_by
  if (a.columns) params.columns = a.columns
  const tab = openers.table(tabs, c, db, a.table, params, true)
  await waitState(tab.id)
  await waitTableLoaded(tab.id)
  return tableSummary(tab.id, a.columns)
})

export const workspace_run_sql = wrap(async (a: RunSQLArgs) => {
  if (!a.connection_id || !a.sql?.trim()) throw new ToolError(t('wstools.badArgs'))
  const c = await connOf(a.connection_id)
  const d = dialectOf(c)
  const stmts = splitSQL(a.sql, d)
  if (!stmts.length) throw new ToolError(t('wstools.badArgs'))
  // 写语句：服务端已按档位确认过的带 confirmed:true；否则在这里请用户确认
  const writes = stmts.filter((s) => !isReadOnlySQL(s.text))
  if (writes.length && !a.confirmed) {
    const preview = writes.map((s) => s.text).join(';\n')
    const ok = await confirmDialog({
      title: t(c.env === 'prod' ? 'wstools.confirmProdTitle' : 'wstools.confirmTitle', { name: c.name }),
      message: t('wstools.confirmHint', { n: writes.length }) + '\n\n' + (preview.length > 800 ? preview.slice(0, 800) + '…' : preview),
      danger: true, okText: t('wstools.run'),
    })
    if (!ok) throw new ToolError(t('wstools.rejected'))
  }
  const tabs = useTabs()
  const tab = openers.console(tabs, c, a.db || '', { sql: a.sql, fromAI: true, title: a.title })
  await waitState(tab.id)
  const run = tabAction(tab.id, 'run')
  if (!run) throw new ToolError(t('wstools.tabClosed'))
  const out = await run(a.sql)
  if (!out) throw new ToolError(t('wstools.busy'))
  const s = readTabState(tab.id) || {}
  return {
    tab_id: tab.id, db: s.db, in_tx: !!s.in_tx,
    results: (s.results || []).map((r: any) => ({ kind: r.kind, statement: r.statement, columns: r.columns || [], rows_loaded: r.rows_loaded || 0,
      affected: r.affected || 0, error: r.error || undefined, sample: clipRows(r.sample, SAMPLE_ROWS) })),
  }
})

export const workspace_update_view = wrap(async (a: UpdateViewArgs) => {
  const tabs = useTabs()
  const id = a.tab_id || tabs.activeId
  const tab = tabs.tabs.find((x) => x.id === id)
  if (!tab) throw new ToolError(t('wstools.noTab'))
  if (tab.type !== 'table') throw new ToolError(t('wstools.notTable'))
  const s = await waitState(tab.id)
  const changing = (a.where !== undefined && a.where !== s.where) || (a.order_by !== undefined && a.order_by !== s.order_by)
  if (changing && s.pending_changes > 0) throw new ToolError(t('wstools.pending', { n: s.pending_changes }))
  tabs.activate(tab.id)
  tabs.update(tab.id, { fromAI: true })
  if (a.columns) tabs.setParams(tab.id, { columns: a.columns })
  if (changing || a.scroll_to_row !== undefined) {
    await tabAction(tab.id, 'update')!({ where: changing ? a.where : undefined, order_by: changing ? a.order_by : undefined, scroll_to_row: a.scroll_to_row })
  } else {
    await waitTableLoaded(tab.id)
  }
  return tableSummary(tab.id, a.columns)
})

export const workspace_read_view = wrap(async () => {
  const tabs = useTabs()
  const tab = tabs.active
  if (!tab) return { tab_id: null, kind: null, error: t('wstools.noTab') }
  const p = tab.params || {}
  const base = { tab_id: tab.id, kind: tab.type, title: tab.title, connection_id: p.connectionId || tab.connectionId || null, db: p.database ?? null }
  const s = readTabState(tab.id)
  if (tab.type === 'table' && s) {
    return { ...base, db: s.db, table: s.table, where: s.where || '', order_by: s.order_by || '', sql: null, offset: s.offset || 0,
      columns: s.columns || [], selection: limitSelection(s.selection), visible_rows_sample: clipRows(s.visible_rows_sample, SAMPLE_ROWS),
      total_loaded: s.total_loaded || 0, has_more: !!s.has_more, pending_changes: s.pending_changes || 0, error: s.error }
  }
  if (tab.type === 'console' && s) {
    const results = (s.results || []).map((r: any) => ({ kind: r.kind, statement: r.statement, columns: r.columns || [], rows_loaded: r.rows_loaded || 0, affected: r.affected || 0, error: r.error || undefined }))
    const firstRows = (s.results || []).find((r: any) => r.kind === 'rows')
    return { ...base, db: s.db, table: null, where: null, order_by: null, sql: s.sql || '', in_tx: !!s.in_tx, results,
      selection: limitSelection(s.selection), visible_rows_sample: clipRows(firstRows?.sample, SAMPLE_ROWS),
      total_loaded: results.reduce((n: number, r: any) => n + (r.rows_loaded || 0), 0) }
  }
  // 其他标签（DDL、设计器、ER 图、监控、账号）：只给出它是什么
  return { ...base, table: p.table ?? p.name ?? null, where: null, order_by: null, sql: null, selection: { columns: [], rows: [] }, visible_rows_sample: [], total_loaded: 0 }
})
