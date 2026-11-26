// 粗分一条语句是不是只读（只用于浏览器端确认提示；真正的只读强制在数据库会话层）。宁可把只读误判成写入。

/** 去掉语句开头的注释与空白。 */
export function stripLeading(sql: string): string {
  let s = sql
  for (;;) {
    const t = s.replace(/^\s+/, '')
    if (t.startsWith('--') || t.startsWith('#')) { const i = t.indexOf('\n'); s = i < 0 ? '' : t.slice(i + 1); continue }
    if (t.startsWith('/*') && !t.startsWith('/*!')) { const i = t.indexOf('*/'); s = i < 0 ? '' : t.slice(i + 2); continue }
    return t
  }
}

const READ = new Set(['select', 'show', 'describe', 'desc', 'explain', 'values', 'table', 'use', 'help'])
const WRITE_IN_WITH = /\b(insert|update|delete|replace|merge)\b/i
const RISKY_SELECT = /\binto\s+(outfile|dumpfile|@)|\bfor\s+update\b|\block\s+in\s+share\s+mode\b/i
const READ_PRAGMAS = new Set(['table_info', 'table_xinfo', 'table_list', 'index_list', 'index_info', 'index_xinfo', 'foreign_key_list', 'foreign_key_check',
  'integrity_check', 'quick_check', 'compile_options', 'database_list', 'collation_list', 'function_list', 'module_list', 'pragma_list',
  'page_count', 'page_size', 'freelist_count', 'encoding', 'user_version', 'schema_version', 'journal_mode', 'foreign_keys', 'application_id', 'data_version'])

export function isReadOnlySQL(sql: string): boolean {
  const s = stripLeading(sql)
  const m = /^[A-Za-z]+/.exec(s)
  if (!m) return false
  const kw = m[0].toLowerCase()
  if (kw === 'with') return !WRITE_IN_WITH.test(s) && !RISKY_SELECT.test(s)
  if (kw === 'select') return !RISKY_SELECT.test(s)
  if (kw === 'pragma') {
    const p = /^pragma\s+(?:\w+\.)?(\w+)/i.exec(s)
    return !!p && READ_PRAGMAS.has(p[1].toLowerCase()) && !s.includes('=')
  }
  return READ.has(kw)
}
