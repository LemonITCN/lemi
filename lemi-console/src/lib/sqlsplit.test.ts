import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { splitSQL, statementAt } from './sqlsplit'

const casesPath = fileURLToPath(new URL('../../../lemi-service/internal/sqlsplit/testdata/cases.json', import.meta.url))
const cases: { name: string; dialect: 'mysql' | 'sqlite'; input: string; statements: string[]; noBackslashEscapes?: boolean; startLines?: number[] }[] = JSON.parse(readFileSync(casesPath, 'utf8'))

describe('与后端共用的切分用例', () => {
  for (const c of cases) {
    it(c.name, () => {
      const got = splitSQL(c.input, c.dialect, { noBackslashEscapes: c.noBackslashEscapes })
      expect(got.map((s) => s.text)).toEqual(c.statements)
      for (const s of got) expect(c.input.slice(s.start, s.end)).toBe(s.text)
      if (c.startLines) expect(got.map((s) => s.startLine)).toEqual(c.startLines)
    })
  }
})

describe('光标所在语句', () => {
  const src = 'SELECT 1;\nSELECT 2;\n\nSELECT 3'
  it('语句内', () => expect(statementAt(src, 'mysql', 3)?.text).toBe('SELECT 1'))
  it('分号后同一行', () => expect(statementAt(src, 'mysql', 9)?.text).toBe('SELECT 1'))
  it('第二条', () => expect(statementAt(src, 'mysql', 12)?.text).toBe('SELECT 2'))
  it('最后一条没有分号', () => expect(statementAt(src, 'mysql', src.length)?.text).toBe('SELECT 3'))
})
