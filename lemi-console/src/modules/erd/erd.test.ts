import { describe, expect, it } from 'vitest'
import type { ColumnInfo, TableInfo } from '@/lib/meta'
import { attrName, attrType, buildErd, entityName, quoteText } from './erd'

function col(name: string, type: string, extra: Partial<ColumnInfo> = {}): ColumnInfo {
  return { name, type, data_type: type.split('(')[0], kind: 'string', nullable: false, default: null, ...extra }
}

function table(name: string, columns: ColumnInfo[], extra: Partial<TableInfo> = {}): TableInfo {
  const pk = columns.filter((c) => c.primary_key).map((c) => c.name)
  return {
    database: 'app', name, kind: 'table', columns,
    indexes: pk.length ? [{ name: 'PRIMARY', columns: pk, unique: true, primary: true }] : [],
    foreign_keys: [], referenced_by: [], triggers: [], key: pk, ...extra,
  }
}

const users = table('users', [
  col('id', 'bigint unsigned', { primary_key: true }),
  col('email', 'varchar(255)', { comment: '登录 "邮箱"' }),
  col('org_id', 'int', { nullable: true }),
], {
  indexes: [
    { name: 'PRIMARY', columns: ['id'], unique: true, primary: true },
    { name: 'uk_email', columns: ['email'], unique: true, primary: false },
  ],
  foreign_keys: [{ name: 'fk_users_org', table: 'users', columns: ['org_id'], ref_database: 'app', ref_table: 'orgs', ref_columns: ['id'] }],
})
const orgs = table('orgs', [col('id', 'int', { primary_key: true }), col('name', 'varchar(64)')])
const orders = table('orders', [
  col('id', 'bigint', { primary_key: true }),
  col('user_id', 'bigint unsigned'),
  col('amount', 'decimal(10,2) unsigned'),
  col('status', "enum('new','paid')"),
], {
  foreign_keys: [{ name: 'fk_orders_user', table: 'orders', columns: ['user_id'], ref_database: 'app', ref_table: 'users', ref_columns: ['id'] }],
})

describe('名称与类型处理', () => {
  it('实体名只保留字母数字下划线', () => {
    expect(entityName('users')).toBe('users')
    expect(entityName('order-items')).toBe('order_items')
    expect(entityName('user list')).toBe('user_list')
    expect(entityName('_tmp')).toBe('_tmp')
    expect(entityName('订单')).toBe('table')
    expect(entityName('订单_v2')).toBe('_v2')
  })
  it('数字开头与关键字加前缀', () => {
    expect(entityName('2024_logs')).toBe('t_2024_logs')
    expect(entityName('end')).toBe('t_end')
    expect(entityName('Class')).toBe('t_Class')
    expect(entityName('one')).toBe('t_one')
  })
  it('属性名避开 PK / FK / UK', () => {
    expect(attrName('pk')).toBe('c_pk')
    expect(attrName('FK')).toBe('c_FK')
    expect(attrName('名称')).toBe('col')
    expect(attrName('1st')).toBe('c_1st')
    expect(attrName('created at')).toBe('created_at')
  })
  it('类型去掉括号与空格', () => {
    expect(attrType('varchar(64)')).toBe('varchar_64')
    expect(attrType('decimal(10,2) unsigned')).toBe('decimal_10_2_unsigned')
    expect(attrType("enum('a','b')")).toBe('enum')
    expect(attrType('SET(\'x\')')).toBe('set')
    expect(attrType('')).toBe('any')
    expect(attrType('double precision')).toBe('double_precision')
  })
  it('引号文本去掉双引号与换行', () => {
    expect(quoteText('a "b"\nc')).toBe('a b c')
    expect(quoteText('x'.repeat(100), 10)).toBe('x'.repeat(9) + '…')
  })
})

describe('buildErd', () => {
  it('生成实体、键标记与注释', () => {
    const src = buildErd([users, orgs])
    expect(src.startsWith('erDiagram\n')).toBe(true)
    expect(src).toContain('  users {')
    expect(src).toContain('    bigint_unsigned id PK\n')
    expect(src).toContain('    varchar_255 email UK "登录 邮箱"')
    expect(src).toContain('    int org_id FK\n')
    expect(src).toContain('    varchar_64 name\n')
  })

  it('外键可空时用零或一', () => {
    const src = buildErd([users, orgs])
    expect(src).toContain('  orgs |o--o{ users : "fk_users_org"')
  })

  it('外键非空时用恰好一', () => {
    const src = buildErd([users, orders])
    expect(src).toContain('  users ||--o{ orders : "fk_orders_user"')
  })

  it('只画选中的表之间的关系', () => {
    const src = buildErd([users, orders])
    expect(src).not.toContain('orgs')
    const only = buildErd([orders])
    expect(only).not.toContain('--')
  })

  it('跨库外键不画', () => {
    const t = table('a', [col('id', 'int', { primary_key: true }), col('b_id', 'int')], {
      foreign_keys: [{ name: 'fk_x', table: 'a', columns: ['b_id'], ref_database: 'other', ref_table: 'b', ref_columns: ['id'] }],
    })
    const b = table('b', [col('id', 'int', { primary_key: true })])
    expect(buildErd([t, b])).not.toContain('fk_x')
  })

  it('原名不合法时用别名并去重', () => {
    const a = table('订单', [col('编号', 'int', { primary_key: true, comment: '主键' })])
    const b = table('明细', [col('id', 'int')])
    const src = buildErd([a, b])
    expect(src).toContain('  table["订单"] {')
    expect(src).toContain('  table_2["明细"] {')
    expect(src).toContain('    int col PK "编号 · 主键"')
  })

  it('自引用与关系去重', () => {
    const emp = table('employees', [col('id', 'int', { primary_key: true }), col('manager_id', 'int', { nullable: true })], {
      foreign_keys: [
        { name: 'fk_mgr', table: 'employees', columns: ['manager_id'], ref_database: 'app', ref_table: 'employees', ref_columns: ['id'] },
        { name: 'fk_mgr', table: 'employees', columns: ['manager_id'], ref_database: 'app', ref_table: 'employees', ref_columns: ['id'] },
      ],
    })
    const src = buildErd([emp])
    expect(src.match(/employees \|o--o\{ employees : "fk_mgr"/g)?.length).toBe(1)
  })

  it('只显示键列、关闭注释', () => {
    const src = buildErd([users, orders], { keysOnly: true, comments: false })
    expect(src).not.toContain('amount')
    expect(src).not.toContain('status')
    expect(src).toContain('    varchar_255 email UK\n')
    expect(src).toContain('    bigint_unsigned user_id FK\n')
  })

  it('SQLite 没有声明类型的列', () => {
    const t = table('kv', [col('k', '', { primary_key: true }), col('v', '')], { database: 'main' })
    expect(buildErd([t])).toContain('    any k PK\n')
  })
})
