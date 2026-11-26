import { describe, expect, it } from 'vitest'
import { isReadOnlySQL, stripLeading } from './classify'

describe('isReadOnlySQL', () => {
  it('只读语句', () => {
    for (const s of ['SELECT 1', '  select * from t', 'WITH x AS (SELECT 1) SELECT * FROM x', 'SHOW TABLES', 'DESC users', 'EXPLAIN SELECT 1',
      'VALUES ROW(1)', 'TABLE t', 'USE app', '-- c\nSELECT 1', '/* x */ SELECT 1', '# c\nshow databases', 'PRAGMA table_info(users)', 'pragma main.index_list("t")', 'PRAGMA journal_mode']) {
      expect(isReadOnlySQL(s), s).toBe(true)
    }
  })
  it('写入或有副作用的语句', () => {
    for (const s of ['INSERT INTO t VALUES (1)', 'update t set a=1', 'DELETE FROM t', 'REPLACE INTO t VALUES (1)', 'CREATE TABLE t (a int)', 'DROP TABLE t',
      'TRUNCATE t', 'ALTER TABLE t ADD c int', 'SET @a = 1', 'BEGIN', 'CALL p()', 'WITH x AS (SELECT 1) DELETE FROM t', 'SELECT * FROM t FOR UPDATE',
      "SELECT * INTO OUTFILE '/tmp/x' FROM t", 'SELECT 1 INTO @v', 'PRAGMA journal_mode = WAL', 'PRAGMA optimize', '/*!40101 SET NAMES utf8 */', '']) {
      expect(isReadOnlySQL(s), s).toBe(false)
    }
  })
  it('去掉开头注释', () => {
    expect(stripLeading('  -- a\n /* b */\n SELECT 1')).toBe('SELECT 1')
    expect(stripLeading('/*!50001 x */')).toBe('/*!50001 x */')
  })
})
