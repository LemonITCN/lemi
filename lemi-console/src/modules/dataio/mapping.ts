// 导入时表列与文件列的自动对应（纯函数）。

/** 归一化列名：小写，去掉空白、下划线、连字符，方便 user_name 对上 "User Name"。 */
export function normName(s: string): string {
  return s.trim().toLowerCase().replace(/[\s_\-.]+/g, '')
}

/**
 * 按名称（有表头时）或按位置（没有表头时）把表列对应到文件列序号；对不上的为 -1。
 * 同一个文件列只对应一次：先精确（忽略大小写）匹配，再归一化匹配。
 */
export function autoMapping(tableCols: string[], fileHeader: string[], fileColCount: number, hasHeader: boolean): Record<string, number> {
  const out: Record<string, number> = {}
  for (const c of tableCols) out[c] = -1
  if (!hasHeader) {
    tableCols.forEach((c, i) => { if (i < fileColCount) out[c] = i })
    return out
  }
  const used = new Set<number>()
  const pass = (key: (s: string) => string) => {
    for (const c of tableCols) {
      if (out[c] >= 0) continue
      const k = key(c)
      const i = fileHeader.findIndex((h, j) => !used.has(j) && h !== undefined && key(h) === k)
      if (i >= 0) { out[c] = i; used.add(i) }
    }
  }
  pass((s) => s.trim().toLowerCase())
  pass(normName)
  return out
}
