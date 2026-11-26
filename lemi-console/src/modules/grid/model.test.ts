import { describe, expect, it } from 'vitest'
import { buildChanges, copyAs, parseTSV, toTSV } from './model'

describe('TSV', () => {
  it('Excel 风格的多行单元格与引号', () => {
    expect(parseTSV('a\tb\r\n"多行\n内容"\t"有""引号"""\r\n')).toEqual([['a', 'b'], ['多行\n内容', '有"引号"']])
  })
  it('往返', () => {
    const rows = [['x\ty', 'line1\nline2', 'q"'], ['', 'plain', '1']]
    expect(parseTSV(toTSV(rows))).toEqual(rows)
  })
  it('单个值', () => expect(parseTSV('hello')).toEqual([['hello']]))
})

describe('变更集', () => {
  const cols = [{ name: 'id', type: 'INT', kind: 'int', origin: 'id', table: 't', pk: true }, { name: 'n', type: 'TEXT', kind: 'text', origin: 'name', table: 't' }]
  it('改、增、删', () => {
    const orig = [['1', 'a'], ['2', 'b']]
    const edits = new Map([[0, new Map([[1, 'A']])]])
    const ch = buildChanges(orig as any, edits as any, [[{ $default: true }, 'new']] as any, new Set([1]), cols as any, ['id'])
    expect(ch).toEqual([
      { op: 'update', key: { id: '1' }, set: { name: 'A' }, old: { name: 'a' } },
      { op: 'insert', values: { name: 'new' } },
      { op: 'delete', key: { id: '2' } },
    ])
  })
})

describe('复制为', () => {
  const cols = [{ name: 'id', type: 'INT', kind: 'int' }, { name: 's', type: 'VARCHAR', kind: 'string' }]
  it('INSERT', () => expect(copyAs('insert', cols as any, [['1', "it's"], ['2', null]], { dialect: 'mysql', table: 't' }))
    .toBe("INSERT INTO `t` (`id`, `s`) VALUES (1, 'it''s');\nINSERT INTO `t` (`id`, `s`) VALUES (2, NULL);"))
  it('WHERE', () => expect(copyAs('where', cols as any, [['1', null]], { dialect: 'sqlite' })).toBe('("id" = 1 AND "s" IS NULL)'))
})
