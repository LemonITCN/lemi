// SQL 语句切分（控制台找光标所在语句、执行选中或全部用）。规则与后端 lemi-service/internal/sqlsplit 完全一致，
// 两边共用 lemi-service/internal/sqlsplit/testdata/cases.json 里的用例。偏移是 JS 字符串下标（UTF-16），src.slice(start, end) === text。
export type Dialect = 'mysql' | 'sqlite'

export interface Statement {
  text: string
  startLine: number
  start: number
  end: number
  terminated: boolean
}

const ST = { invalid: 0, start: 1, normal: 2, explain: 3, create: 4, trigger: 5, semi: 6, end: 7 }
const TK = { semi: 0, ws: 1, other: 2, explain: 3, create: 4, temp: 5, trigger: 6, end: 7 }
const trans = [
  [ST.start, ST.invalid, ST.normal, ST.explain, ST.create, ST.normal, ST.normal, ST.normal],
  [ST.start, ST.start, ST.normal, ST.explain, ST.create, ST.normal, ST.normal, ST.normal],
  [ST.start, ST.normal, ST.normal, ST.normal, ST.normal, ST.normal, ST.normal, ST.normal],
  [ST.start, ST.explain, ST.explain, ST.normal, ST.create, ST.normal, ST.normal, ST.normal],
  [ST.start, ST.create, ST.normal, ST.normal, ST.normal, ST.create, ST.trigger, ST.normal],
  [ST.semi, ST.trigger, ST.trigger, ST.trigger, ST.trigger, ST.trigger, ST.trigger, ST.trigger],
  [ST.semi, ST.semi, ST.trigger, ST.trigger, ST.trigger, ST.trigger, ST.trigger, ST.end],
  [ST.start, ST.end, ST.trigger, ST.trigger, ST.trigger, ST.trigger, ST.trigger, ST.trigger],
]

const isSpace = (c: string) => c === ' ' || c === '\t' || c === '\n' || c === '\r' || c === '\f' || c === '\v'
const isIdent = (c: string) => /[A-Za-z0-9_$]/.test(c) || c.charCodeAt(0) >= 0x80

export function splitSQL(src: string, dialect: Dialect, opts: { noBackslashEscapes?: boolean } = {}): Statement[] {
  const mysql = dialect !== 'sqlite'
  const out: Statement[] = []
  let pos = 0
  let line = 1
  let delim = ';'
  let inCond = false
  let trig = ST.invalid
  let stmtStart = -1
  let stmtLine = 0
  let lastSig = 0
  const n = src.length
  const feed = (tk: number) => { if (!mysql) trig = trans[trig][tk] }
  const mark = () => { if (stmtStart < 0) { stmtStart = pos; stmtLine = line } }
  const sig = () => { lastSig = pos }
  const emit = (terminated: boolean) => {
    out.push({ text: src.slice(stmtStart, lastSig), startLine: stmtLine, start: stmtStart, end: lastSig, terminated })
    stmtStart = -1
  }
  const skipLine = () => { while (pos < n && src[pos] !== '\n') pos++ }
  const skipBlock = () => {
    pos += 2
    while (pos < n) {
      if (src[pos] === '*' && src[pos + 1] === '/') { pos += 2; return }
      if (src[pos] === '\n') line++
      pos++
    }
  }
  const scanQuoted = (q: string, backslash: boolean) => {
    pos++
    while (pos < n) {
      const c = src[pos]
      if (backslash && c === '\\') {
        pos++
        if (pos < n) { if (src[pos] === '\n') line++; pos++ }
        continue
      }
      if (c === q) {
        pos++
        if (src[pos] === q) { pos++; continue }
        return
      }
      if (c === '\n') line++
      pos++
    }
  }
  const scanUntil = (end: string) => {
    pos++
    while (pos < n) {
      const c = src[pos++]
      if (c === '\n') line++
      if (c === end) return
    }
  }
  const scanWord = (): number => {
    const s0 = pos
    while (pos < n && isIdent(src[pos])) pos++
    const w = src.slice(s0, pos).toLowerCase()
    switch (w) {
      case 'create': return TK.create
      case 'temp': case 'temporary': return TK.temp
      case 'trigger': return TK.trigger
      case 'explain': return TK.explain
      case 'end': return TK.end
    }
    return TK.other
  }
  const delimiterDirective = (): boolean => {
    const kw = 'delimiter'
    if (src.slice(pos, pos + kw.length).toLowerCase() !== kw) return false
    const sep = src[pos + kw.length]
    if (sep !== ' ' && sep !== '\t') return false
    let end = kw.length
    while (pos + end < n && end < 4096 && src[pos + end] !== '\n' && src[pos + end] !== '\r') end++
    const rest = src.slice(pos + kw.length, pos + end).replace(/^[ \t]+/, '')
    let arg = ''
    if (rest) {
      const q = rest[0]
      if (q === "'" || q === '"' || q === '`') {
        const j = rest.indexOf(q, 1)
        if (j >= 0) arg = rest.slice(1, j)
      } else {
        const m = rest.search(/[ \t]/)
        arg = m < 0 ? rest : rest.slice(0, m)
      }
    }
    if (!arg || arg.includes('\\')) return false
    delim = arg
    pos += end
    return true
  }

  while (true) {
    if (pos >= n) {
      if (stmtStart >= 0) emit(false)
      return out
    }
    const c = src[pos]
    if (pos === 0 && c === '﻿') { pos++; continue }
    if (isSpace(c)) {
      if (c === '\n') line++
      pos++
      feed(TK.ws)
      continue
    }
    if (inCond) {
      if (c === '*' && src[pos + 1] === '/') {
        mark()
        pos += 2
        inCond = false
        sig()
        continue
      }
    } else if (mysql) {
      if (src.startsWith(delim, pos)) {
        pos += delim.length
        if (stmtStart >= 0) emit(true)
        continue
      }
      if (stmtStart < 0 && (c === 'd' || c === 'D') && delimiterDirective()) continue
    } else if (c === ';') {
      feed(TK.semi)
      if (trig === ST.start) {
        pos++
        if (stmtStart >= 0) emit(true)
        continue
      }
      mark()
      pos++
      sig()
      continue
    }
    if (c === '#' && mysql) { skipLine(); feed(TK.ws); continue }
    if (c === '-' && src[pos + 1] === '-') {
      const n2 = src[pos + 2]
      if (!mysql || n2 === undefined || n2.charCodeAt(0) <= 32 || n2.charCodeAt(0) === 0x7f) { skipLine(); feed(TK.ws); continue }
    }
    if (c === '/' && src[pos + 1] === '*') {
      if (mysql && !inCond) {
        const n2 = src[pos + 2]
        const n3 = src[pos + 3]
        if (n2 === '!' || (n2 === 'M' && n3 === '!')) {
          mark()
          pos += n2 === '!' ? 3 : 4
          inCond = true
          sig()
          continue
        }
      }
      skipBlock()
      feed(TK.ws)
      continue
    }
    mark()
    if (c === "'" || c === '"') { scanQuoted(c, mysql && !opts.noBackslashEscapes); feed(TK.other) }
    else if (c === '`') { scanQuoted(c, false); feed(TK.other) }
    else if (c === '[' && !mysql) { scanUntil(']'); feed(TK.other) }
    else if (!mysql && isIdent(c)) feed(scanWord())
    else { pos++; feed(TK.other) }
    sig()
  }
}

/** 光标所在（或紧挨着的）语句。 */
export function statementAt(src: string, dialect: Dialect, cursor: number): Statement | undefined {
  const list = splitSQL(src, dialect)
  if (!list.length) return undefined
  // 光标在语句内或语句后的分隔符与空白里，都算这一条
  let best: Statement | undefined
  for (const s of list) {
    if (cursor >= s.start) best = s
    else break
  }
  if (!best) return list[0]
  const next = list[list.indexOf(best) + 1]
  if (next && cursor > best.end) {
    // 光标在两条语句之间：如果和上一条之间只有分隔符/空白且在同一行，取上一条，否则取下一条
    const between = src.slice(best.end, cursor)
    if (between.includes('\n') && src.slice(cursor, next.start).trim() === '') return next
  }
  return best
}
