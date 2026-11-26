// MySQL 权限分组（与服务端 driver/mysql/admin.go 的 privWhitelist 一致）与各授权范围允许的权限。

export type Op = 'create' | 'drop' | 'password' | 'lock' | 'unlock' | 'grant' | 'revoke' | 'readonly'

export type Scope = 'global' | 'db' | 'table'

export const PRIV_GROUPS: { key: 'data' | 'structure' | 'routine' | 'other'; privs: string[] }[] = [
  { key: 'data', privs: ['SELECT', 'INSERT', 'UPDATE', 'DELETE'] },
  { key: 'structure', privs: ['CREATE', 'DROP', 'ALTER', 'INDEX', 'REFERENCES', 'CREATE VIEW', 'SHOW VIEW', 'TRIGGER'] },
  { key: 'routine', privs: ['EXECUTE', 'CREATE ROUTINE', 'ALTER ROUTINE', 'EVENT'] },
  { key: 'other', privs: ['LOCK TABLES', 'CREATE TEMPORARY TABLES', 'PROCESS', 'RELOAD', 'SHOW DATABASES', 'REPLICATION CLIENT', 'REPLICATION SLAVE'] },
]

// 只能全局授予的权限（ON *.*）
const GLOBAL_ONLY = new Set(['PROCESS', 'RELOAD', 'SHOW DATABASES', 'REPLICATION CLIENT', 'REPLICATION SLAVE'])
// 表级允许的权限
const TABLE_OK = new Set(['ALL PRIVILEGES', 'SELECT', 'INSERT', 'UPDATE', 'DELETE', 'CREATE', 'DROP', 'ALTER', 'INDEX', 'REFERENCES', 'CREATE VIEW', 'SHOW VIEW', 'TRIGGER'])

export function scopeOf(database: string, table: string): Scope {
  if (!database || database === '*') return 'global'
  return table ? 'table' : 'db'
}

export function allowedAt(priv: string, scope: Scope): boolean {
  if (scope === 'global') return true
  if (scope === 'db') return !GLOBAL_ONLY.has(priv)
  return TABLE_OK.has(priv)
}
