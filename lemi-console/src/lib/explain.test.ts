import { describe, expect, it } from 'vitest'
import { mysqlPlan, sqlitePlan } from './explain'

const mysqlJSON = {
  query_block: {
    select_id: 1,
    cost_info: { query_cost: '521.36' },
    ordering_operation: {
      using_temporary_table: true,
      using_filesort: true,
      grouping_operation: {
        using_filesort: false,
        nested_loop: [
          { table: { table_name: 'c', access_type: 'index', possible_keys: ['PRIMARY', 'idx_email'], key: 'PRIMARY', used_key_parts: ['id'], rows_examined_per_scan: 1980, filtered: '33.33',
            cost_info: { prefix_cost: '203.25' }, attached_condition: "(`shop`.`c`.`level` = 'gold')" } },
          { table: { table_name: 'o', access_type: 'ref', possible_keys: ['idx_cust'], key: 'idx_cust', used_key_parts: ['customer_id'], ref: ['shop.c.id'], rows_examined_per_scan: 2, filtered: '100.00', using_index: true, cost_info: { prefix_cost: '521.36' } } },
        ],
      },
    },
  },
}

describe('mysqlPlan', () => {
  it('嵌套的排序、分组、连接与表', () => {
    const p = mysqlPlan(JSON.stringify(mysqlJSON))!
    expect(p.label).toBe('SELECT #1')
    expect(p.stats).toEqual([['cost', '521.36']])
    const order = p.children[0]
    expect(order.kind).toBe('ordering_operation')
    expect(order.warn).toEqual(['temporary', 'filesort'])
    const group = order.children[0]
    expect(group.kind).toBe('grouping_operation')
    expect(group.warn).toBeUndefined()
    const loop = group.children[0]
    expect(loop.kind).toBe('nested_loop')
    expect(loop.children.map((x) => x.label)).toEqual(['c', 'o'])
    const c = loop.children[0]
    expect(c.access).toBe('index')
    expect(c.condition).toContain("'gold'")
    expect(c.stats).toContainEqual(['key', 'PRIMARY(id)'])
    expect(c.stats).toContainEqual(['rows', '1980'])
    expect(loop.children[1].stats).toContainEqual(['ref', 'shop.c.id'])
  })
  it('全表扫描标记、派生表与 UNION', () => {
    const p = mysqlPlan({
      query_block: {
        union_result: {
          using_temporary_table: true,
          query_specifications: [
            { query_block: { select_id: 1, table: { table_name: 't1', access_type: 'ALL', rows_examined_per_scan: 10 } } },
            { query_block: { select_id: 2, table: { table_name: 'dt', access_type: 'ALL', materialized_from_subquery: { query_block: { select_id: 3, table: { table_name: 't2', access_type: 'range', key: 'k' } } } } } },
          ],
        },
      },
    })!
    const u = p.children[0]
    expect(u.kind).toBe('union_result')
    expect(u.warn).toEqual(['temporary'])
    expect(u.children.map((x) => x.label)).toEqual(['SELECT #1', 'SELECT #2'])
    expect(u.children[0].children[0].warn).toEqual(['full_scan'])
    expect(u.children[1].children[0].children[0].label).toBe('SELECT #3')
  })
  it('解析失败返回 null', () => {
    expect(mysqlPlan('not json')).toBeNull()
    expect(mysqlPlan({})).toBeNull()
  })
})

describe('sqlitePlan', () => {
  it('按 parent 组织成树并标出全表扫描', () => {
    const roots = sqlitePlan([
      ['2', '0', '0', 'SCAN o'],
      ['5', '0', '0', 'SEARCH c USING INTEGER PRIMARY KEY (rowid=?)'],
      ['9', '0', '0', 'USE TEMP B-TREE FOR GROUP BY'],
      ['12', '0', '0', 'CORRELATED SCALAR SUBQUERY 1'],
      ['15', '12', '0', 'SEARCH x USING COVERING INDEX idx (a=?)'],
    ])
    expect(roots.map((r) => r.label)).toEqual(['SCAN o', 'SEARCH c USING INTEGER PRIMARY KEY (rowid=?)', 'USE TEMP B-TREE FOR GROUP BY', 'CORRELATED SCALAR SUBQUERY 1'])
    expect(roots[0].warn).toEqual(['full_scan'])
    expect(roots[1].warn).toBeUndefined()
    expect(roots[2].warn).toEqual(['temporary'])
    expect(roots[3].children[0].label).toContain('COVERING INDEX')
  })
})
