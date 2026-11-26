// 表结构设计器的草稿模型：TableInfo → 可编辑草稿 → 提交给后端的 TableDesign（对应 driver.TableDesign）。
// 纯函数，不依赖 store。差异由后端计算：列靠 orig_name 识别改名/新增，主键由主键索引（或列的 primary_key）决定，
// 索引与外键按名称比较。
import type { ColumnInfo, ForeignKeyInfo, IndexInfo, TableInfo } from '@/lib/meta'
import type { Dialect } from '@/lib/sql'

export type DefMode = 'none' | 'null' | 'value' | 'expr'

export interface ColDraft {
  key: string
  /** 原列名；新列为空 */
  orig_name: string
  name: string
  type: string
  nullable: boolean
  def_mode: DefMode
  def_value: string
  auto_increment: boolean
  primary_key: boolean
  comment: string
  charset: string
  collation: string
  /** 只读信息：生成列（virtual / stored）与表达式、EXTRA */
  generated: string
  generation_expr: string
  extra: string
}

export type IdxKind = 'index' | 'unique' | 'fulltext' | 'spatial'

export interface IdxColDraft { name: string; length: string }

export interface IdxDraft {
  key: string
  /** 载入时的名称；新索引为空 */
  orig_name: string
  name: string
  kind: IdxKind
  columns: IdxColDraft[]
  comment: string
  /** 原索引类型（BTREE / HASH …），改回普通索引时沿用 */
  orig_type: string
  /** 锁定：auto = SQLite 的 UNIQUE 约束自动索引（不提交）；expr = 表达式索引（原样提交，只能删除） */
  locked: '' | 'auto' | 'expr'
  /** 锁定索引的原始数据 */
  raw?: IndexInfo
}

export interface FkPair { column: string; ref_column: string }

export interface FkDraft {
  key: string
  /** 载入时的名称；新外键为空 */
  orig_name: string
  name: string
  ref_database: string
  ref_table: string
  pairs: FkPair[]
  on_delete: string
  on_update: string
}

export interface OptsDraft {
  name: string
  comment: string
  engine: string
  charset: string
  collation: string
  auto_increment: string
}

export interface Draft {
  cols: ColDraft[]
  idx: IdxDraft[]
  fks: FkDraft[]
  opts: OptsDraft
  /** 原主键列顺序（原列名），保持复合主键顺序不被列顺序改写 */
  pk_order: string[]
}

/** 提交给后端的结构（driver.TableDesign）。 */
export interface TableDesign {
  name: string
  comment: string
  engine: string
  charset: string
  collation: string
  auto_increment: number
  columns: {
    orig_name: string; name: string; type: string; nullable: boolean; default: string | null; default_is_expr: boolean
    auto_increment: boolean; comment: string; charset: string; collation: string; primary_key: boolean
  }[]
  indexes: IndexInfo[]
  foreign_keys: ForeignKeyInfo[]
}

let seq = 0
export function newKey(p = 'k'): string { return p + Date.now().toString(36) + (++seq).toString(36) }

const EXPR_COL = '(表达式)'

function defModeOf(c: ColumnInfo): { mode: DefMode; value: string } {
  if (c.default === null || c.default === undefined) return { mode: 'none', value: '' }
  if (c.default_is_expr) return { mode: 'expr', value: c.default }
  if (c.default.toUpperCase() === 'NULL') return { mode: 'null', value: '' }
  return { mode: 'value', value: c.default }
}

export function colFromInfo(c: ColumnInfo): ColDraft {
  const d = defModeOf(c)
  return {
    key: newKey('c'), orig_name: c.name, name: c.name, type: c.type || '', nullable: !!c.nullable, def_mode: d.mode, def_value: d.value,
    auto_increment: !!c.auto_increment, primary_key: !!c.primary_key, comment: c.comment || '', charset: c.charset || '', collation: c.collation || '',
    generated: c.generated || '', generation_expr: c.generation_expr || '', extra: c.extra || '',
  }
}

export function newColumn(dialect: Dialect, name = ''): ColDraft {
  return {
    key: newKey('c'), orig_name: '', name, type: dialect === 'mysql' ? 'varchar(255)' : 'TEXT', nullable: true, def_mode: 'none', def_value: '',
    auto_increment: false, primary_key: false, comment: '', charset: '', collation: '', generated: '', generation_expr: '', extra: '',
  }
}

function idxKindOf(ix: IndexInfo): IdxKind {
  const t = (ix.type || '').toUpperCase()
  if (t === 'FULLTEXT') return 'fulltext'
  if (t === 'SPATIAL') return 'spatial'
  return ix.unique ? 'unique' : 'index'
}

export function idxFromInfo(ix: IndexInfo, dialect: Dialect): IdxDraft {
  let locked: IdxDraft['locked'] = ''
  if (dialect === 'sqlite' && ix.name.startsWith('sqlite_autoindex_')) locked = 'auto'
  else if (ix.columns.includes(EXPR_COL)) locked = 'expr'
  return {
    key: newKey('i'), orig_name: ix.name, name: ix.name, kind: idxKindOf(ix), comment: ix.comment || '', orig_type: ix.type || '', locked,
    columns: ix.columns.map((c, i) => ({ name: c, length: ix.lengths?.[i] ? String(ix.lengths[i]) : '' })),
    raw: locked ? ix : undefined,
  }
}

export function newIndex(kind: IdxKind = 'index'): IdxDraft {
  return { key: newKey('i'), orig_name: '', name: '', kind, columns: [], comment: '', orig_type: '', locked: '' }
}

export function fkFromInfo(fk: ForeignKeyInfo): FkDraft {
  const pairs = fk.columns.map((c, i) => ({ column: c, ref_column: fk.ref_columns[i] || '' }))
  return {
    key: newKey('f'), orig_name: fk.name, name: fk.name, ref_database: fk.ref_database || '', ref_table: fk.ref_table, pairs,
    on_delete: (fk.on_delete || '').toUpperCase(), on_update: (fk.on_update || '').toUpperCase(),
  }
}

export function newFk(database: string): FkDraft {
  return { key: newKey('f'), orig_name: '', name: '', ref_database: database, ref_table: '', pairs: [{ column: '', ref_column: '' }], on_delete: '', on_update: '' }
}

/** 已有表 → 草稿。 */
export function draftFromTable(t: TableInfo, dialect: Dialect): Draft {
  const pk = (t.indexes || []).find((ix) => ix.primary)
  return {
    cols: t.columns.map(colFromInfo),
    idx: (t.indexes || []).filter((ix) => !ix.primary).map((ix) => idxFromInfo(ix, dialect)),
    fks: (t.foreign_keys || []).map(fkFromInfo),
    opts: {
      name: t.name, comment: t.comment || '', engine: t.engine || '', charset: t.charset || '', collation: t.collation || '',
      auto_increment: t.auto_increment ? String(t.auto_increment) : '',
    },
    pk_order: pk ? [...pk.columns] : t.columns.filter((c) => c.primary_key).map((c) => c.name),
  }
}

/** 新建表的初始草稿：一列自增主键 id。 */
export function newDraft(dialect: Dialect): Draft {
  const id = newColumn(dialect, 'id')
  id.type = dialect === 'mysql' ? 'bigint unsigned' : 'INTEGER'
  id.nullable = false
  id.primary_key = true
  id.auto_increment = true
  return {
    cols: [id],
    idx: [],
    fks: [],
    opts: { name: '', comment: '', engine: dialect === 'mysql' ? 'InnoDB' : '', charset: dialect === 'mysql' ? 'utf8mb4' : '', collation: '', auto_increment: '' },
    pk_order: [],
  }
}

/** 主键列（新列名）：原主键里仍是主键的列按原顺序在前，新加入主键的列按列顺序在后。 */
export function pkColumns(d: Draft): string[] {
  const byOrig = new Map(d.cols.filter((c) => c.orig_name).map((c) => [c.orig_name, c]))
  const out: string[] = []
  const used = new Set<string>()
  for (const o of d.pk_order) {
    const c = byOrig.get(o)
    if (c && c.primary_key) { out.push(c.name); used.add(c.key) }
  }
  for (const c of d.cols) if (c.primary_key && !used.has(c.key)) out.push(c.name)
  return out
}

function defaultOf(c: ColDraft): { default: string | null; default_is_expr: boolean } {
  switch (c.def_mode) {
    case 'null': return { default: 'NULL', default_is_expr: false }
    case 'value': return { default: c.def_value, default_is_expr: false }
    case 'expr': return { default: c.def_value.trim(), default_is_expr: true }
    default: return { default: null, default_is_expr: false }
  }
}

/** 字符串类型（可以有字符集与排序规则）。 */
export function isTextType(type: string): boolean {
  return /^\s*(national\s+)?(var)?char\b|^\s*(tiny|medium|long)?text\b|^\s*(enum|set)\s*\(/i.test(type)
}

function idxType(ix: IdxDraft): string {
  if (ix.kind === 'fulltext') return 'FULLTEXT'
  if (ix.kind === 'spatial') return 'SPATIAL'
  const o = ix.orig_type.toUpperCase()
  // 由全文/空间改回普通或唯一索引时显式给出 BTREE，否则后端会忽略类型变化
  if (o === 'FULLTEXT' || o === 'SPATIAL') return 'BTREE'
  return ix.orig_type
}

/**
 * 草稿 → TableDesign。
 * origAutoInc：载入时的自增值；未改动时提交 0（不改），避免把过时的计数器写回去。
 */
export function designFromDraft(d: Draft, dialect: Dialect, origAutoInc = ''): TableDesign {
  const pk = pkColumns(d)
  const autoInc = d.opts.auto_increment.trim()
  const indexes: IndexInfo[] = []
  if (pk.length) indexes.push({ name: 'PRIMARY', columns: pk, unique: true, primary: true })
  for (const ix of d.idx) {
    if (ix.locked === 'auto') continue
    if (ix.locked === 'expr' && ix.raw) { indexes.push(ix.raw); continue }
    indexes.push({
      name: ix.name.trim(),
      columns: ix.columns.map((c) => c.name),
      lengths: ix.columns.map((c) => (dialect === 'mysql' ? Number(c.length) || 0 : 0)),
      unique: ix.kind === 'unique',
      primary: false,
      type: idxType(ix),
      comment: dialect === 'mysql' ? ix.comment : '',
    })
  }
  return {
    name: d.opts.name.trim(),
    comment: d.opts.comment,
    engine: dialect === 'mysql' ? d.opts.engine.trim() : '',
    charset: dialect === 'mysql' ? d.opts.charset.trim() : '',
    collation: dialect === 'mysql' ? d.opts.collation.trim() : '',
    auto_increment: autoInc && autoInc !== origAutoInc ? Math.max(0, Math.floor(Number(autoInc)) || 0) : 0,
    columns: d.cols.map((c) => ({
      orig_name: c.orig_name,
      name: c.name.trim(),
      type: c.type.trim(),
      nullable: c.primary_key ? false : c.nullable,
      ...defaultOf(c),
      auto_increment: c.auto_increment,
      comment: dialect === 'mysql' ? c.comment : '',
      // 字符集只对字符串类型有意义；改成数值等类型后不再带上原来的排序规则
      charset: dialect === 'mysql' && isTextType(c.type) ? c.charset.trim() : '',
      collation: dialect === 'mysql' && isTextType(c.type) ? c.collation.trim() : '',
      primary_key: c.primary_key,
    })),
    indexes,
    foreign_keys: d.fks.map((f) => ({
      name: f.name.trim(),
      table: '',
      columns: f.pairs.map((p) => p.column),
      ref_database: f.ref_database,
      ref_table: f.ref_table,
      ref_columns: f.pairs.map((p) => p.ref_column),
      on_delete: f.on_delete,
      on_update: f.on_update,
    })),
  }
}

export interface Problem { section: 'columns' | 'indexes' | 'fks' | 'options'; key: string; params?: Record<string, any> }

/** 提交前的本地检查（后端还会再检查一遍）。返回 i18n 键与参数。 */
export function validate(d: Draft, dialect: Dialect): Problem[] {
  const out: Problem[] = []
  if (!d.opts.name.trim()) out.push({ section: 'options', key: 'designer.err.tableName' })
  if (!d.cols.length) out.push({ section: 'columns', key: 'designer.err.noColumns' })
  const seen = new Set<string>()
  d.cols.forEach((c, i) => {
    const n = c.name.trim()
    if (!n) out.push({ section: 'columns', key: 'designer.err.colName', params: { n: i + 1 } })
    else if (seen.has(n.toLowerCase())) out.push({ section: 'columns', key: 'designer.err.colDup', params: { name: n } })
    else seen.add(n.toLowerCase())
    if (dialect === 'mysql' && !c.type.trim()) out.push({ section: 'columns', key: 'designer.err.colType', params: { name: n || i + 1 } })
    if (c.def_mode === 'expr' && !c.def_value.trim()) out.push({ section: 'columns', key: 'designer.err.colExpr', params: { name: n || i + 1 } })
  })
  const colNames = new Set(d.cols.map((c) => c.name.trim()))
  const ixSeen = new Set<string>()
  d.idx.forEach((ix, i) => {
    if (ix.locked) return
    const n = ix.name.trim()
    if (!n) out.push({ section: 'indexes', key: 'designer.err.idxName', params: { n: i + 1 } })
    else if (ixSeen.has(n.toLowerCase())) out.push({ section: 'indexes', key: 'designer.err.idxDup', params: { name: n } })
    else ixSeen.add(n.toLowerCase())
    if (!ix.columns.length) out.push({ section: 'indexes', key: 'designer.err.idxCols', params: { name: n || i + 1 } })
    for (const c of ix.columns) if (!colNames.has(c.name)) out.push({ section: 'indexes', key: 'designer.err.idxMissing', params: { name: n || i + 1, col: c.name } })
  })
  d.fks.forEach((f, i) => {
    const n = f.name.trim() || String(i + 1)
    if (dialect === 'mysql' && !f.name.trim()) out.push({ section: 'fks', key: 'designer.err.fkName', params: { n: i + 1 } })
    if (!f.ref_table) out.push({ section: 'fks', key: 'designer.err.fkRef', params: { name: n } })
    if (!f.pairs.length || f.pairs.some((p) => !p.column || !p.ref_column)) out.push({ section: 'fks', key: 'designer.err.fkCols', params: { name: n } })
    for (const p of f.pairs) if (p.column && !colNames.has(p.column)) out.push({ section: 'fks', key: 'designer.err.fkMissing', params: { name: n, col: p.column } })
  })
  return out
}

/** 改列名时同步更新索引与外键里的引用。 */
export function renameRefs(d: Draft, from: string, to: string) {
  if (!from || from === to) return
  for (const ix of d.idx) for (const c of ix.columns) if (c.name === from) c.name = to
  for (const f of d.fks) for (const p of f.pairs) if (p.column === from) p.column = to
}

/** 删除列时移除索引与外键里的引用（索引列删空的保留，提交前检查会提示）。 */
export function dropRefs(d: Draft, name: string) {
  for (const ix of d.idx) if (!ix.locked) ix.columns = ix.columns.filter((c) => c.name !== name)
  for (const f of d.fks) for (const p of f.pairs) if (p.column === name) p.column = ''
}

export const MYSQL_TYPES = [
  'int', 'int unsigned', 'bigint', 'bigint unsigned', 'tinyint', 'tinyint(1)', 'smallint', 'mediumint', 'decimal(10,2)', 'float', 'double', 'bit(1)',
  'varchar(255)', 'varchar(64)', 'char(36)', 'text', 'mediumtext', 'longtext', 'json', 'enum(\'a\',\'b\')', 'set(\'a\',\'b\')',
  'date', 'datetime', 'datetime(3)', 'timestamp', 'time', 'year', 'blob', 'mediumblob', 'longblob', 'binary(16)', 'varbinary(255)', 'geometry', 'point',
]
export const SQLITE_TYPES = ['INTEGER', 'TEXT', 'REAL', 'NUMERIC', 'BLOB', 'BOOLEAN', 'DATE', 'DATETIME', 'VARCHAR(255)', 'DECIMAL(10,2)']
export const MYSQL_ENGINES = ['InnoDB', 'MyISAM', 'MEMORY', 'ARCHIVE', 'CSV', 'Aria']
export const MYSQL_CHARSETS = ['utf8mb4', 'utf8mb3', 'utf8', 'latin1', 'ascii', 'binary', 'gbk', 'gb18030', 'big5']
export const MYSQL_COLLATIONS = [
  'utf8mb4_0900_ai_ci', 'utf8mb4_0900_as_cs', 'utf8mb4_general_ci', 'utf8mb4_unicode_ci', 'utf8mb4_unicode_520_ci', 'utf8mb4_bin',
  'utf8mb3_general_ci', 'utf8mb3_unicode_ci', 'utf8mb3_bin', 'utf8_general_ci', 'utf8_unicode_ci', 'utf8_bin', 'latin1_swedish_ci', 'latin1_bin',
  'ascii_general_ci', 'ascii_bin', 'binary', 'gbk_chinese_ci', 'gbk_bin', 'gb18030_chinese_ci', 'big5_chinese_ci',
]
export const FK_ACTIONS: Record<Dialect, string[]> = {
  mysql: ['RESTRICT', 'CASCADE', 'SET NULL', 'NO ACTION'],
  sqlite: ['NO ACTION', 'RESTRICT', 'CASCADE', 'SET NULL', 'SET DEFAULT'],
}
