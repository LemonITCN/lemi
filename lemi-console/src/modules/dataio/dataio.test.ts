import { describe, expect, it } from 'vitest'
import { autoMapping, normName } from './mapping'
import { dangerSQL, insertTemplate, maybeQuote, selectTemplate } from './templates'
import type { ColumnInfo } from '@/lib/meta'

const col = (name: string, extra: Partial<ColumnInfo> = {}): ColumnInfo => ({ name, type: 'int', data_type: 'int', kind: 'int', nullable: true, default: null, ...extra })

describe('autoMapping', () => {
  it('有表头时按名称对应，大小写与下划线不敏感', () => {
    const m = autoMapping(['id', 'user_name', 'Email', 'age'], ['ID', 'User Name', 'email', 'note'], 4, true)
    expect(m).toEqual({ id: 0, user_name: 1, Email: 2, age: -1 })
  })
  it('精确匹配优先于归一化匹配，同一文件列只用一次', () => {
    const m = autoMapping(['user_name', 'username'], ['username', 'user_name'], 2, true)
    expect(m).toEqual({ user_name: 1, username: 0 })
  })
  it('没有表头时按位置对应，超出文件列数的为 -1', () => {
    expect(autoMapping(['a', 'b', 'c'], [], 2, false)).toEqual({ a: 0, b: 1, c: -1 })
  })
  it('normName', () => {
    expect(normName(' Order-Id ')).toBe('orderid')
    expect(normName('order.id')).toBe('orderid')
  })
})

describe('templates', () => {
  it('普通标识符不加引号，保留字与特殊字符加方言引号', () => {
    expect(maybeQuote('users', 'mysql')).toBe('users')
    expect(maybeQuote('order', 'mysql')).toBe('`order`')
    expect(maybeQuote('order', 'sqlite')).toBe('"order"')
    expect(maybeQuote('用户', 'mysql')).toBe('`用户`')
    expect(maybeQuote('a`b', 'mysql')).toBe('`a``b`')
    expect(maybeQuote('1st', 'sqlite')).toBe('"1st"')
  })
  it('SELECT 模板列出全部列', () => {
    expect(selectTemplate('order', [col('id'), col('key'), col('amount')], 'mysql')).toBe('SELECT id, `key`, amount\nFROM `order`;')
    expect(selectTemplate('t', [], 'sqlite')).toBe('SELECT *\nFROM t;')
  })
  it('INSERT 模板跳过自增列与生成列', () => {
    const cols = [col('id', { auto_increment: true }), col('name'), col('full', { generated: 'stored' }), col('select')]
    expect(insertTemplate('t', cols, 'sqlite')).toBe('INSERT INTO t (name, "select")\nVALUES (?, ?);')
  })
  it('清空与删除', () => {
    expect(dangerSQL('truncate', 'shop', 'orders', 'mysql')).toBe('TRUNCATE TABLE `shop`.`orders`')
    expect(dangerSQL('truncate', 'main', 'orders', 'sqlite')).toBe('DELETE FROM "orders"')
    expect(dangerSQL('drop', 'main', 'a"b', 'sqlite')).toBe('DROP TABLE "a""b"')
  })
})
