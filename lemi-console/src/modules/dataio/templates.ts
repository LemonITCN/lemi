// 对象树「复制 SELECT / INSERT 语句」与危险操作语句的生成（纯函数）。
import { quoteIdent, type Dialect } from '@/lib/sql'
import type { ColumnInfo } from '@/lib/meta'

// 常见保留字：撞上了就加引号（不追求完整，宁可多引）
const RESERVED = new Set(('add all alter and as asc between by case check column constraint create cross current_date current_time current_timestamp database default delete desc distinct drop else end exists false for foreign from full group having in index inner insert interval into is join key left like limit match natural not null on or order outer primary references right rowid select set table then to true union unique update using values when where with ' +
  'abort action after analyze attach autoincrement before begin cascade cast collate commit conflict deferrable deferred detach each escape except exclusive explain fail glob if ignore immediate indexed initially instead intersect isnull notnull of offset plan pragma query raise recursive regexp reindex release rename replace restrict rollback row savepoint temp temporary transaction trigger vacuum view virtual ' +
  'change condition div dual fulltext keys kill lock long modify mod option outfile range read regexp rlike schema separator show spatial sql ssl status usage use utc_date write xor year zerofill').split(/\s+/))

/** 只在需要时加引号：普通标识符原样输出，读起来更清爽。 */
export function maybeQuote(name: string, d: Dialect): string {
  if (/^[A-Za-z_][A-Za-z0-9_]*$/.test(name) && !RESERVED.has(name.toLowerCase())) return name
  return quoteIdent(name, d)
}

export function selectTemplate(table: string, cols: ColumnInfo[], d: Dialect): string {
  const list = cols.length ? cols.map((c) => maybeQuote(c.name, d)).join(', ') : '*'
  return `SELECT ${list}\nFROM ${maybeQuote(table, d)};`
}

/** INSERT 模板：跳过自增列与生成列，值用 ? 占位。 */
export function insertTemplate(table: string, cols: ColumnInfo[], d: Dialect): string {
  const use = cols.filter((c) => !c.auto_increment && !c.generated)
  const names = use.map((c) => maybeQuote(c.name, d)).join(', ')
  const marks = use.map(() => '?').join(', ')
  return `INSERT INTO ${maybeQuote(table, d)} (${names})\nVALUES (${marks});`
}

/** 清空 / 删除表的语句（SQLite 没有 TRUNCATE，用 DELETE）。 */
export function dangerSQL(op: 'truncate' | 'drop', db: string, table: string, d: Dialect): string {
  const name = d === 'mysql' && db ? quoteIdent(db, d) + '.' + quoteIdent(table, d) : quoteIdent(table, d)
  if (op === 'drop') return `DROP TABLE ${name}`
  return d === 'sqlite' ? `DELETE FROM ${name}` : `TRUNCATE TABLE ${name}`
}
