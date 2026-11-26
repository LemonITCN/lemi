import { describe, expect, it } from 'vitest'
import { splitBlocks } from './markdown'

const ID = '01m8c0j6w5x0r1pbq2n3k4h5g6'

describe('splitBlocks', () => {
  it('切出完整的引用块', () => {
    const segs = splitBlocks(`前文\n\n\`\`\`lemi-chart ${ID}\`\`\`\n\n后文`)
    expect(segs.map((s) => s.type)).toEqual(['md', 'lemi', 'md'])
    expect(segs[1]).toMatchObject({ kind: 'chart', id: ID })
  })

  it('多行形式带说明', () => {
    const segs = splitBlocks(`\`\`\`lemi-table ${ID}\n各城市用户数\n\`\`\``)
    expect(segs).toEqual([{ type: 'lemi', kind: 'table', id: ID, caption: '各城市用户数' }])
  })

  it('流式输出中最后一行是半截引用块时先不显示', () => {
    for (const tail of ['`', '``', '```', '```le', '```lemi-', '```lemi-ch', '```lemi-chart', '```lemi-chart ', '```lemi-chart 01', '```lemi-chart 01m8c0j6w5']) {
      const segs = splitBlocks('统计如下：\n\n' + tail)
      expect(segs, tail).toEqual([{ type: 'md', text: '统计如下：\n' }])
    }
  })

  it('最后一行已经是完整 ID 或已闭合时照常显示', () => {
    expect(splitBlocks(`\`\`\`lemi-chart ${ID}`)).toMatchObject([{ type: 'lemi', id: ID }])
    expect(splitBlocks('```lemi-chart 01ab```')).toMatchObject([{ type: 'lemi', id: '01ab' }])
  })

  it('不是最后一行时短 ID 也照常切出（报告与测试数据）', () => {
    expect(splitBlocks('```lemi-diagram 01QQQ\n```\n')).toMatchObject([{ type: 'lemi', kind: 'diagram', id: '01qqq' }])
  })

  it('普通代码块与 mermaid 不受影响', () => {
    const segs = splitBlocks('```sql\nselect 1\n```\n```mermaid\ngraph TD\nA-->B\n```')
    expect(segs.map((s) => s.type)).toEqual(['md', 'mermaid'])
  })
})
