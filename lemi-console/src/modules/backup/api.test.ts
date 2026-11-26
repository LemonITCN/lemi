import { describe, expect, it } from 'vitest'
import { formatBytes, formatDuration, percent, type Progress } from './api'

const p = (x: Partial<Progress>): Progress => ({ kind: 'backup', id: 'x', workspace_id: '', connection_id: '', plan_id: '', phase: 'running', bytes: 0, written: 0, total: 0, rows: 0,
  database: '', table: '', tables_done: 0, tables_total: 0, statements: 0, errors: 0, speed: 0, elapsed_ms: 0, ...x })

describe('备份格式化', () => {
  it('字节', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(28529)).toBe('27.9 KB')
    expect(formatBytes(41057055)).toBe('39.2 MB')
    expect(formatBytes(5 * 1024 ** 4)).toBe('5.0 TB')
  })
  it('耗时', () => {
    expect(formatDuration(0)).toBe('—')
    expect(formatDuration(300)).toBe('1s')
    expect(formatDuration(65_000)).toBe('1m 5s')
    expect(formatDuration(3_725_000)).toBe('1h 2m')
  })
  it('进度百分比：有表数按表，否则按字节，算不出为 null', () => {
    expect(percent(undefined)).toBeNull()
    expect(percent(p({ tables_done: 3, tables_total: 4 }))).toBe(75)
    expect(percent(p({ total: 200, bytes: 50 }))).toBe(25)
    expect(percent(p({ kind: 'restore', total: 200, written: 150 }))).toBe(75)
    expect(percent(p({ total: 100, bytes: 500 }))).toBe(100)
    expect(percent(p({}))).toBeNull()
  })
})
