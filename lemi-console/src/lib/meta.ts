// 后端表结构与结果集的类型（对应 lemi-service/internal/driver 的 TableInfo、Result 等），以及取表结构的小工具。
import { api } from '@/api/client'

export interface ColumnInfo {
  name: string
  type: string
  data_type: string
  kind: string
  nullable: boolean
  default: string | null
  default_is_expr?: boolean
  auto_increment?: boolean
  generated?: string
  generation_expr?: string
  comment?: string
  charset?: string
  collation?: string
  length?: number
  precision?: number
  scale?: number
  unsigned?: boolean
  enum_values?: string[]
  extra?: string
  primary_key?: boolean
  invisible?: boolean
  /** MySQL：ON UPDATE 表达式（例如 CURRENT_TIMESTAMP） */
  on_update?: string
}

export interface IndexInfo {
  name: string
  columns: string[]
  lengths?: number[]
  unique: boolean
  primary: boolean
  type?: string
  comment?: string
}

export interface ForeignKeyInfo {
  name: string
  table: string
  columns: string[]
  ref_database: string
  ref_table: string
  ref_columns: string[]
  on_delete?: string
  on_update?: string
}

export interface TableInfo {
  database: string
  name: string
  kind: 'table' | 'view'
  comment?: string
  engine?: string
  charset?: string
  collation?: string
  rows_est?: number
  auto_increment?: number
  columns: ColumnInfo[]
  indexes: IndexInfo[] | null
  foreign_keys: ForeignKeyInfo[] | null
  referenced_by: ForeignKeyInfo[] | null
  triggers: any[] | null
  key: string[] | null
  key_kind?: 'primary' | 'unique' | 'rowid'
  ddl?: string
  options?: Record<string, string>
  without_rowid?: boolean
}

/** 二进制单元格：base64（超过 64KB 截断，len 为原始字节数）。 */
export interface BinCell { $bin: string; len: number }
/** 被截断的长文本。 */
export interface TruncCell { $trunc: string; len: number }
/** 前端改出来的特殊值。 */
export interface DefaultCell { $default: true }
export interface HexCell { $hex: string }
export type Cell = string | null | BinCell | TruncCell | DefaultCell | HexCell

export interface ResultColumn {
  name: string
  type: string
  kind: string
  nullable?: boolean
  table?: string
  db?: string
  origin?: string
  pk?: boolean
}

export interface Editable { ok: boolean; reason?: string; db?: string; table?: string; key?: string[] }

export interface StmtClass { kind: string; keyword: string; returns_rows?: boolean; dangerous?: string }

export interface QueryResult {
  statement: string
  kind: 'rows' | 'exec' | 'error'
  columns?: ResultColumn[]
  rows?: Cell[][]
  has_more?: boolean
  affected: number
  last_insert_id?: number
  duration_ms: number
  error?: string
  class?: StmtClass
  editable?: Editable
}

/** 取一张表的完整结构。 */
export async function fetchTable(connId: string, db: string, table: string): Promise<TableInfo> {
  return api.get(`/connections/${connId}/databases/${encodeURIComponent(db)}/tables/${encodeURIComponent(table)}`)
}

/** 结构快照：表 → 列（编辑器补全用），按连接与库缓存一分钟。 */
const completionCache = new Map<string, { at: number; p: Promise<Record<string, string[]>> }>()
export function fetchCompletion(connId: string, db: string, force = false): Promise<Record<string, string[]>> {
  const k = connId + '\u0000' + db
  const c = completionCache.get(k)
  if (c && !force && Date.now() - c.at < 60_000) return c.p
  const p = api.get(`/connections/${connId}/databases/${encodeURIComponent(db)}/completion`).then((r) => (r.tables || {}) as Record<string, string[]>)
  p.catch(() => completionCache.delete(k))
  completionCache.set(k, { at: Date.now(), p })
  return p
}

/** 并发受限地依次执行。 */
export async function mapLimit<T, R>(items: T[], limit: number, fn: (x: T, i: number) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length)
  let next = 0
  async function worker() {
    while (next < items.length) {
      const i = next++
      out[i] = await fn(items[i], i)
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker))
  return out
}

export function formatBytes(n?: number | string | null): string {
  const v0 = typeof n === 'string' ? Number(n) : n
  if (v0 === undefined || v0 === null || !isFinite(v0)) return ''
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = v0
  while (Math.abs(v) >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return (i === 0 ? String(v) : v.toFixed(v < 10 ? 1 : 0)) + ' ' + u[i]
}
