// 按表结构与外键生成 Mermaid erDiagram 源码（纯函数，vitest 直接测）。
// Mermaid 的实体名、属性名、类型只用 [A-Za-z0-9_]：原名不同时实体用别名语法 safe["原名"] 显示，
// 属性把原名写进属性注释；类型去掉括号、空格等符号。
import type { TableInfo } from '@/lib/meta'

export interface ErdOptions {
  /** 把列注释写成属性注释（默认 true） */
  comments?: boolean
  /** 只画主键、外键、唯一键列 */
  keysOnly?: boolean
}

// erDiagram 词法里会被当成关键字的词（实体名不能用）
const RESERVED_ENTITY = new Set(['one', 'many', 'to', 'end', 'style', 'class', 'classdef', 'subgraph', 'erdiagram', 'direction', 'title', 'u'])
// 属性块里 PK / FK / UK 是键标记
const RESERVED_ATTR = new Set(['pk', 'fk', 'uk'])

/** 非法字符段替换成一个下划线，去掉因替换产生的首尾下划线（原名本身的下划线保留）。 */
function sanitize(name: string): string {
  let s = name.replace(/[^A-Za-z0-9_]+/g, '_')
  if (/[^A-Za-z0-9_]/.test(name.charAt(0))) s = s.replace(/^_/, '')
  if (/[^A-Za-z0-9_]/.test(name.charAt(name.length - 1))) s = s.replace(/_$/, '')
  return s
}

/** 实体名：只保留 [A-Za-z0-9_]，不能以数字开头，不能是关键字。 */
export function entityName(name: string): string {
  let s = sanitize(name)
  if (!/[A-Za-z0-9]/.test(s)) s = 'table'
  if (/^[0-9]/.test(s) || RESERVED_ENTITY.has(s.toLowerCase())) s = 't_' + s
  return s
}

/** 属性名：同实体名规则，另外不能是 PK / FK / UK。 */
export function attrName(name: string): string {
  let s = sanitize(name)
  if (!/[A-Za-z0-9]/.test(s)) s = 'col'
  if (/^[0-9]/.test(s) || RESERVED_ATTR.has(s.toLowerCase())) s = 'c_' + s
  return s
}

/** 类型：varchar(64) → varchar_64，decimal(10,2) unsigned → decimal_10_2_unsigned，enum/set 只取类型名。 */
export function attrType(type: string): string {
  const t = (type || '').trim()
  if (!t) return 'any'
  const m = /^(enum|set)\s*\(/i.exec(t)
  if (m) return m[1].toLowerCase()
  let s = t.replace(/[^A-Za-z0-9_]+/g, '_').replace(/_{2,}/g, '_').replace(/^_+|_+$/g, '')
  if (!s) return 'any'
  if (s.length > 40) s = s.slice(0, 40).replace(/_+$/, '')
  if (/^[0-9]/.test(s)) s = 't_' + s
  if (RESERVED_ATTR.has(s.toLowerCase())) s += '_t'
  return s
}

/** 引号字符串里的文本：去掉双引号与换行，避免截断 Mermaid 语法。 */
export function quoteText(s: string, max = 80): string {
  let t = s.replace(/["\\%]/g, '').replace(/[\r\n\t\v\f\b]+/g, ' ').replace(/\s{2,}/g, ' ').trim()
  if (t.length > max) t = t.slice(0, max - 1) + '…'
  return t
}

/** 生成 erDiagram 源码。只画给定表之间的外键关系。 */
export function buildErd(tables: TableInfo[], opts: ErdOptions = {}): string {
  const withComments = opts.comments !== false
  const used = new Set<string>()
  const ids = new Map<string, string>()
  for (const t of tables) {
    let id = entityName(t.name)
    if (used.has(id.toLowerCase())) {
      let n = 2
      while (used.has(`${id}_${n}`.toLowerCase())) n++
      id = `${id}_${n}`
    }
    used.add(id.toLowerCase())
    ids.set(t.name, id)
  }

  const lines: string[] = ['erDiagram']
  for (const t of tables) {
    const id = ids.get(t.name)!
    const fkCols = new Set((t.foreign_keys || []).flatMap((f) => f.columns))
    const ukCols = new Set((t.indexes || []).filter((x) => x.unique && !x.primary && x.columns.length === 1).map((x) => x.columns[0]))
    const pkCols = new Set((t.indexes || []).find((x) => x.primary)?.columns || [])
    const head = id === t.name ? id : `${id}["${quoteText(t.name, 120) || id}"]`
    const attrs: string[] = []
    for (const c of t.columns) {
      const keys: string[] = []
      if (c.primary_key || pkCols.has(c.name)) keys.push('PK')
      if (fkCols.has(c.name)) keys.push('FK')
      if (ukCols.has(c.name) && !keys.includes('PK')) keys.push('UK')
      if (opts.keysOnly && !keys.length) continue
      const an = attrName(c.name)
      const notes: string[] = []
      if (an !== c.name) notes.push(c.name)
      if (withComments && c.comment) notes.push(c.comment)
      const note = quoteText(notes.join(' · '))
      let line = `    ${attrType(c.type || c.data_type)} ${an}`
      if (keys.length) line += ' ' + keys.join(', ')
      if (note) line += ` "${note}"`
      attrs.push(line)
    }
    lines.push(`  ${head} {`, ...attrs, '  }')
  }

  // 关系：被引用表 ||--o{ 引用表；外键列可空时被引用一侧为零或一
  const seen = new Set<string>()
  for (const t of tables) {
    const from = ids.get(t.name)!
    for (const fk of t.foreign_keys || []) {
      if (fk.ref_database && t.database && fk.ref_database !== t.database) continue
      const to = ids.get(fk.ref_table)
      if (!to) continue
      const nullable = fk.columns.some((cn) => t.columns.find((c) => c.name === cn)?.nullable)
      const label = quoteText(fk.name || fk.columns.join(', ')) || 'fk'
      const key = `${to}|${from}|${label}`
      if (seen.has(key)) continue
      seen.add(key)
      lines.push(`  ${to} ${nullable ? '|o' : '||'}--o{ ${from} : "${label}"`)
    }
  }
  return lines.join('\n') + '\n'
}
