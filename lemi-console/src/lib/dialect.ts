// 按连接取 SQL 方言（驱动 query_language 为 sql:mysql / sql:sqlite）与能力判断。
import { useConns, type Conn } from '@/stores/conns'
import type { Dialect } from './sql'

export function dialectOf(c: Conn | undefined | null): Dialect {
  if (!c) return 'mysql'
  const ql = useConns().driver(c.driver)?.query_language || ''
  if (ql === 'sql:sqlite' || c.driver === 'sqlite') return 'sqlite'
  return 'mysql'
}

/** 连接是否具备某能力（驱动能力清单）。 */
export function hasCap(c: Conn | undefined | null, cap: string): boolean {
  if (!c) return false
  return !!useConns().driver(c.driver)?.capabilities.includes(cap)
}
