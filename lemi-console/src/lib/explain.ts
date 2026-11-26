// 执行计划转成树：MySQL EXPLAIN FORMAT=JSON 与 SQLite EXPLAIN QUERY PLAN（id, parent, notused, detail）。纯函数，vitest 直接测。

export interface PlanNode {
  /** 节点类型：query_block / table / nested_loop / ordering_operation / … ，SQLite 为 step */
  kind: string
  /** 主文字：表名、操作名或 SQLite 的 detail */
  label: string
  /** 访问方式（MySQL access_type：ALL / index / range / ref / eq_ref / const …） */
  access?: string
  /** 关键数值：使用的索引、扫描行数、过滤比例、代价等，按显示顺序 */
  stats: [string, string][]
  /** 附加条件（WHERE 下推后的条件） */
  condition?: string
  /** 值得注意：全表扫描、全索引扫描、临时表、文件排序 */
  warn?: string[]
  children: PlanNode[]
}

const OPS: Record<string, string> = {
  ordering_operation: 'ORDER BY', grouping_operation: 'GROUP BY', duplicates_removal: 'DISTINCT', windowing: 'WINDOW',
  nested_loop: 'NESTED LOOP', union_result: 'UNION', buffer_result: 'BUFFER',
}

function num(v: any): string { return v === undefined || v === null ? '' : String(v) }

function tableNode(t: any): PlanNode {
  const n: PlanNode = { kind: 'table', label: t.table_name || '?', access: t.access_type, stats: [], children: [], warn: [] }
  if (t.key) n.stats.push(['key', t.key + (t.used_key_parts ? `(${t.used_key_parts.join(', ')})` : '')])
  else if (t.possible_keys?.length) n.stats.push(['possible_keys', t.possible_keys.join(', ')])
  if (t.ref?.length) n.stats.push(['ref', t.ref.join(', ')])
  if (t.rows_examined_per_scan !== undefined) n.stats.push(['rows', num(t.rows_examined_per_scan)])
  if (t.filtered !== undefined) n.stats.push(['filtered', num(t.filtered) + '%'])
  if (t.cost_info?.prefix_cost) n.stats.push(['cost', num(t.cost_info.prefix_cost)])
  if (t.using_index) n.stats.push(['using_index', ''])
  if (t.attached_condition) n.condition = t.attached_condition
  if (t.access_type === 'ALL') n.warn!.push('full_scan')
  if (t.access_type === 'index' && !t.attached_condition && !t.using_index) n.warn!.push('full_index')
  // 派生表 / 子查询
  if (t.materialized_from_subquery?.query_block) n.children.push(queryBlock(t.materialized_from_subquery.query_block))
  for (const k of ['attached_subqueries', 'optimized_away_subqueries']) for (const q of t[k] || []) if (q.query_block) n.children.push(queryBlock(q.query_block))
  if (!n.warn!.length) delete n.warn
  return n
}

/** 把一个对象里认识的子结构依次展开成子节点。 */
function expand(o: any, into: PlanNode[]) {
  if (!o || typeof o !== 'object') return
  if (o.table) into.push(tableNode(o.table))
  if (Array.isArray(o.nested_loop)) {
    const n: PlanNode = { kind: 'nested_loop', label: OPS.nested_loop, stats: [], children: [] }
    for (const x of o.nested_loop) expand(x, n.children)
    into.push(n)
  }
  for (const k of ['ordering_operation', 'grouping_operation', 'duplicates_removal', 'windowing', 'buffer_result']) {
    const v = o[k]
    if (!v || typeof v !== 'object') continue
    const n: PlanNode = { kind: k, label: OPS[k], stats: [], children: [], warn: [] }
    if (v.using_temporary_table) n.warn!.push('temporary')
    if (v.using_filesort) n.warn!.push('filesort')
    if (!n.warn!.length) delete n.warn
    expand(v, n.children)
    into.push(n)
  }
  if (o.union_result) {
    const u = o.union_result
    const n: PlanNode = { kind: 'union_result', label: OPS.union_result + (u.table_name ? ` ${u.table_name}` : ''), stats: [], children: [] }
    if (u.using_temporary_table) n.warn = ['temporary']
    for (const q of u.query_specifications || []) if (q.query_block) n.children.push(queryBlock(q.query_block))
    into.push(n)
  }
  for (const k of ['optimized_away_subqueries', 'select_list_subqueries', 'having_subqueries']) for (const q of o[k] || []) if (q.query_block) into.push(queryBlock(q.query_block))
}

function queryBlock(qb: any): PlanNode {
  const n: PlanNode = { kind: 'query_block', label: qb.select_id !== undefined ? 'SELECT #' + qb.select_id : 'QUERY', stats: [], children: [] }
  if (qb.cost_info?.query_cost) n.stats.push(['cost', num(qb.cost_info.query_cost)])
  if (qb.message) n.stats.push(['message', qb.message])
  expand(qb, n.children)
  return n
}

/** MySQL EXPLAIN FORMAT=JSON（字符串或已解析的对象）。解析失败返回 null。 */
export function mysqlPlan(input: string | object): PlanNode | null {
  let j: any = input
  if (typeof input === 'string') {
    try { j = JSON.parse(input) } catch { return null }
  }
  if (!j?.query_block) return null
  return queryBlock(j.query_block)
}

/** SQLite EXPLAIN QUERY PLAN 的行（id, parent, notused, detail，均为文本）→ 树。 */
export function sqlitePlan(rows: (string | null)[][]): PlanNode[] {
  const byId = new Map<string, PlanNode>()
  const roots: PlanNode[] = []
  for (const r of rows) {
    const [id, parent, , detail] = r.map((x) => (x === null || x === undefined ? '' : String(x)))
    const d = detail || ''
    const n: PlanNode = { kind: 'step', label: d, stats: [], children: [] }
    if (/^SCAN\b/i.test(d) && !/\bUSING\b.*\bINDEX\b/i.test(d)) n.warn = ['full_scan']
    if (/USE TEMP B-TREE/i.test(d)) n.warn = [...(n.warn || []), 'temporary']
    byId.set(id, n)
    const p = byId.get(parent)
    if (p && parent !== '0') p.children.push(n)
    else roots.push(n)
  }
  return roots
}
