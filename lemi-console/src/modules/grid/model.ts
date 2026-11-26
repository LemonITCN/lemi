// 表格的纯逻辑：单元格文本、TSV 解析与生成、复制格式、改动集合 → 变更集请求。不依赖 Vue，vitest 直接测。
import type { Cell, ResultColumn } from '@/lib/meta'
import { quoteIdent, quoteString, hexLiteral, isNumericText, type Dialect } from '@/lib/sql'

export const NUMERIC_KINDS = new Set(['int', 'decimal', 'float', 'bit', 'year', 'bool'])

export function isBin(v: any): v is { $bin: string; len: number } { return !!v && typeof v === 'object' && '$bin' in v }
export function isTrunc(v: any): v is { $trunc: string; len: number } { return !!v && typeof v === 'object' && '$trunc' in v }
export function isDefault(v: any): v is { $default: true } { return !!v && typeof v === 'object' && '$default' in v }
export function isHex(v: any): v is { $hex: string } { return !!v && typeof v === 'object' && '$hex' in v }

export function b64ToBytes(b64: string): Uint8Array {
  const s = atob(b64)
  const out = new Uint8Array(s.length)
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i)
  return out
}

export function bytesToHex(b: Uint8Array, max = Infinity): string {
  let s = ''
  const n = Math.min(b.length, max)
  for (let i = 0; i < n; i++) s += b[i].toString(16).padStart(2, '0')
  return s
}

export function hexToB64(hex: string): string {
  const h = hex.replace(/^0x/i, '').replace(/\s+/g, '')
  let s = ''
  for (let i = 0; i + 1 < h.length; i += 2) s += String.fromCharCode(parseInt(h.slice(i, i + 2), 16))
  return btoa(s)
}

/** 表格里显示的文本（单行）。 */
export function displayText(v: Cell | undefined): string {
  if (v === null || v === undefined) return 'NULL'
  if (typeof v === 'string') {
    const s = v.length > 300 ? v.slice(0, 300) : v
    return s.replace(/\r?\n/g, ' ↵ ').replace(/\t/g, ' ')
  }
  if (isBin(v)) {
    const b = b64ToBytes(v.$bin)
    return '0x' + bytesToHex(b, 16).toUpperCase() + (v.len > 16 ? '…' : '') + (v.len > 16 ? ` (${v.len} B)` : '')
  }
  if (isTrunc(v)) return v.$trunc.slice(0, 300).replace(/\r?\n/g, ' ↵ ') + '…'
  if (isDefault(v)) return 'DEFAULT'
  if (isHex(v)) return '0x' + v.$hex.replace(/^0x/i, '').toUpperCase()
  return String(v)
}

/** 复制用的纯文本（NULL 为空串，二进制为 0x 十六进制）。 */
export function plainText(v: Cell | undefined, nullText = ''): string {
  if (v === null || v === undefined) return nullText
  if (typeof v === 'string') return v
  if (isBin(v)) return '0x' + bytesToHex(b64ToBytes(v.$bin)).toUpperCase()
  if (isTrunc(v)) return v.$trunc
  if (isDefault(v)) return ''
  if (isHex(v)) return '0x' + v.$hex.replace(/^0x/i, '').toUpperCase()
  return String(v)
}

export function sameCell(a: Cell | undefined, b: Cell | undefined): boolean {
  if (a === b) return true
  if (a === null || b === null || a === undefined || b === undefined) return false
  return JSON.stringify(a) === JSON.stringify(b)
}

// ---------- TSV ----------

/** 生成与 Excel 兼容的 TSV：含制表符、换行、引号的字段用双引号包住并双写引号。 */
export function toTSV(rows: string[][]): string {
  return rows.map((r) => r.map((f) => (/[\t\n\r"]/.test(f) ? '"' + f.replace(/"/g, '""') + '"' : f)).join('\t')).join('\n')
}

/** 解析从 Excel / 表格复制来的 TSV（支持引号包住的多行单元格）。 */
export function parseTSV(text: string): string[][] {
  const rows: string[][] = []
  let row: string[] = []
  let field = ''
  let i = 0
  let quoted = false
  let atStart = true
  text = text.replace(/\r\n?/g, '\n')
  while (i < text.length) {
    const ch = text[i]
    if (quoted) {
      if (ch === '"') {
        if (text[i + 1] === '"') { field += '"'; i += 2; continue }
        quoted = false
        i++
        continue
      }
      field += ch
      i++
      continue
    }
    if (ch === '"' && atStart) { quoted = true; atStart = false; i++; continue }
    if (ch === '\t') { row.push(field); field = ''; atStart = true; i++; continue }
    if (ch === '\n') { row.push(field); rows.push(row); row = []; field = ''; atStart = true; i++; continue }
    field += ch
    atStart = false
    i++
  }
  if (field !== '' || row.length) { row.push(field); rows.push(row) }
  // Excel 复制会在最后多一个换行
  while (rows.length > 1 && rows[rows.length - 1].length === 1 && rows[rows.length - 1][0] === '') rows.pop()
  return rows
}

// ---------- 复制为 ----------

export type CopyFormat = 'tsv' | 'csv' | 'json' | 'insert' | 'update' | 'markdown' | 'where'

function csvField(s: string): string { return /[",\n\r]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s }

function sqlLit(v: Cell | undefined, kind: string, d: Dialect): string {
  if (v === null || v === undefined) return 'NULL'
  if (isDefault(v)) return 'DEFAULT'
  if (isBin(v)) return hexLiteral(bytesToHex(b64ToBytes(v.$bin)), d)
  if (isHex(v)) return hexLiteral(v.$hex, d)
  const s = plainText(v)
  if (NUMERIC_KINDS.has(kind) && isNumericText(s)) return s
  return quoteString(s, d)
}

/** 把选中的单元格转成某种格式的文本。cols 为选中的列，rows 为选中的行（每行只含这些列的值）。 */
export function copyAs(fmt: CopyFormat, cols: ResultColumn[], rows: (Cell | undefined)[][], opts: { dialect: Dialect; table?: string; keyCols?: string[]; keyRows?: (Cell | undefined)[][] } = { dialect: 'mysql' }): string {
  const d = opts.dialect
  const names = cols.map((c) => c.origin || c.name)
  switch (fmt) {
    case 'tsv':
      return toTSV(rows.map((r) => r.map((v) => plainText(v))))
    case 'csv':
      return [names.map(csvField).join(','), ...rows.map((r) => r.map((v) => csvField(plainText(v))).join(','))].join('\n')
    case 'json':
      return JSON.stringify(rows.map((r) => Object.fromEntries(names.map((n, i) => [n, r[i] === null || r[i] === undefined ? null : plainText(r[i])]))), null, 2)
    case 'markdown': {
      const esc = (s: string) => s.replace(/\|/g, '\\|').replace(/\r?\n/g, '<br>')
      return ['| ' + names.map(esc).join(' | ') + ' |', '|' + names.map(() => ' --- |').join(''), ...rows.map((r) => '| ' + r.map((v) => esc(plainText(v, 'NULL'))).join(' | ') + ' |')].join('\n')
    }
    case 'insert': {
      const t = quoteIdent(opts.table || 'table_name', d)
      const cl = names.map((n) => quoteIdent(n, d)).join(', ')
      return rows.map((r) => `INSERT INTO ${t} (${cl}) VALUES (${r.map((v, i) => sqlLit(v, cols[i].kind, d)).join(', ')});`).join('\n')
    }
    case 'update': {
      const t = quoteIdent(opts.table || 'table_name', d)
      const keys = opts.keyCols || []
      return rows.map((r, ri) => {
        const sets = r.map((v, i) => `${quoteIdent(names[i], d)} = ${sqlLit(v, cols[i].kind, d)}`).join(', ')
        const kr = opts.keyRows?.[ri] || []
        const where = keys.length ? keys.map((k, i) => `${quoteIdent(k, d)} = ${sqlLit(kr[i], 'string', d)}`).join(' AND ') : '/* 条件 */'
        return `UPDATE ${t} SET ${sets} WHERE ${where};`
      }).join('\n')
    }
    case 'where': {
      const parts = rows.map((r) => {
        const conds = r.map((v, i) => (v === null || v === undefined ? `${quoteIdent(names[i], d)} IS NULL` : `${quoteIdent(names[i], d)} = ${sqlLit(v, cols[i].kind, d)}`))
        return conds.length > 1 ? '(' + conds.join(' AND ') + ')' : conds[0]
      })
      return parts.join(' OR ')
    }
  }
}

/** 一个单元格作为 WHERE 条件片段（按此值筛选）。 */
export function filterFor(col: ResultColumn, v: Cell | undefined, d: Dialect): string {
  const n = quoteIdent(col.origin || col.name, d)
  if (v === null || v === undefined) return `${n} IS NULL`
  return `${n} = ${sqlLit(v, col.kind, d)}`
}

// ---------- 变更集 ----------

export interface ChangeReq {
  op: 'update' | 'insert' | 'delete'
  key?: Record<string, any>
  set?: Record<string, any>
  old?: Record<string, any>
  values?: Record<string, any>
}

function wire(v: Cell | undefined): any {
  if (v === undefined) return null
  if (isHex(v)) return { $hex: v.$hex }
  return v
}

/**
 * 把表格里的改动转成后端的变更集：
 * orig 原始行；edits 行号 → (列号 → 新值)；added 新行；deleted 待删除的原始行号；
 * cols 结果列（origin 为表里的列名）；key 行键列名。
 */
export function buildChanges(orig: Cell[][], edits: Map<number, Map<number, Cell>>, added: Cell[][], deleted: Set<number>, cols: ResultColumn[], key: string[]): ChangeReq[] {
  const idx: Record<string, number> = {}
  cols.forEach((c, i) => { idx[c.origin || c.name] = i })
  const keyOf = (row: Cell[]) => Object.fromEntries(key.map((k) => [k, wire(row[idx[k]])]))
  const out: ChangeReq[] = []
  for (const [r, m] of edits) {
    if (deleted.has(r) || !m.size) continue
    const set: Record<string, any> = {}
    const old: Record<string, any> = {}
    for (const [c, v] of m) {
      const col = cols[c]
      if (!col.origin && !col.table) continue
      const name = col.origin || col.name
      set[name] = wire(v)
      old[name] = orig[r][c]
    }
    if (Object.keys(set).length) out.push({ op: 'update', key: keyOf(orig[r]), set, old })
  }
  for (const row of added) {
    const values: Record<string, any> = {}
    row.forEach((v, c) => {
      if (isDefault(v)) return
      const col = cols[c]
      if (!col) return
      values[col.origin || col.name] = wire(v)
    })
    out.push({ op: 'insert', values })
  }
  for (const r of [...deleted].sort((a, b) => a - b)) out.push({ op: 'delete', key: keyOf(orig[r]) })
  return out
}

/** 粗略估算列宽（字符宽度：中日韩按 2 计）。 */
export function textWidth(s: string): number {
  let w = 0
  for (const ch of s) w += ch.charCodeAt(0) > 0x2e80 ? 2 : 1
  return w
}
