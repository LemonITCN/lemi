// SQL 方言小工具：标识符与字符串引用、按连接取方言。表格复制、控制台、设计器、对象树菜单共用。
// 纯函数，不依赖 store（vitest 直接测）；按连接取方言见 lib/dialect.ts。
export type Dialect = 'mysql' | 'sqlite'

export function quoteIdent(name: string, d: Dialect): string {
  if (d === 'mysql') return '`' + name.replace(/`/g, '``') + '`'
  return '"' + name.replace(/"/g, '""') + '"'
}

/** 限定表名：库.表（SQLite 的 main 库省略）。 */
export function qualifiedName(db: string | undefined, table: string, d: Dialect): string {
  if (!db || (d === 'sqlite' && db === 'main')) return quoteIdent(table, d)
  return quoteIdent(db, d) + '.' + quoteIdent(table, d)
}

export function quoteString(s: string, d: Dialect): string {
  if (d === 'mysql') {
    return "'" + s.replace(/\\/g, '\\\\').replace(/'/g, "''").replace(/\0/g, '\\0').replace(/\n/g, '\\n').replace(/\r/g, '\\r').replace(/\x1a/g, '\\Z') + "'"
  }
  return "'" + s.replace(/'/g, "''") + "'"
}

/** 十六进制字面量（二进制）。 */
export function hexLiteral(hex: string, d: Dialect): string {
  const h = hex.replace(/^0x/i, '')
  if (d === 'sqlite') return "X'" + h + "'"
  return h ? '0x' + h : "''"
}

const numRe = /^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$/

export function isNumericText(s: string): boolean { return numRe.test(s.trim()) }
