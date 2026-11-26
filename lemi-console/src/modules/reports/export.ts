// 报告导出：单文件 HTML（图表渲染成内嵌 SVG，3D 图为 PNG；图为 Mermaid 生成的 SVG；表格内嵌），
// PDF 用浏览器打印同一份 HTML。不带任何脚本，打开即看。
import { renderMarkdown, splitBlocks, cellText } from '@/components/rich/markdown'
import { i18n } from '@/i18n'

const t = (k: string, a?: Record<string, unknown>) => String(i18n.global.t('reports.' + k, a || {}))

export interface ReportData { title: string; body: string; snapshot: any; updated_at?: number }

function esc(s: string) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!)
}

async function chartImage(view: any): Promise<string> {
  const echarts: any = await import('echarts')
  const opt = { ...JSON.parse(JSON.stringify(view.option || {})), animation: false, backgroundColor: '#ffffff' }
  if (!view.gl) {
    const c = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 760, height: 380 })
    c.setOption(opt)
    const svg = c.renderToSVGString()
    c.dispose()
    return svg
  }
  await import('echarts-gl')
  const host = document.createElement('div')
  host.style.cssText = 'position:fixed;left:-10000px;top:0;width:760px;height:380px'
  document.body.appendChild(host)
  try {
    const c = echarts.init(host, null, { renderer: 'canvas' })
    c.setOption(opt)
    await new Promise((r) => setTimeout(r, 600))
    const url = c.getDataURL({ type: 'png', pixelRatio: 2, backgroundColor: '#ffffff' })
    c.dispose()
    return `<img src="${url}" alt="" style="width:100%;max-width:760px">`
  } finally {
    host.remove()
  }
}

async function mermaidSVG(code: string): Promise<string> {
  try {
    const { default: mermaid } = await import('mermaid')
    mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'default', htmlLabels: false, flowchart: { htmlLabels: false } })
    const { svg } = await mermaid.render('ex-' + Math.random().toString(36).slice(2), code)
    return svg
  } catch {
    return `<pre>${esc(code)}</pre>`
  }
}

function tableHTML(t: any): string {
  const cols: any[] = t.columns || []
  const rows: any[][] = t.rows || []
  let h = '<div class="tbl"><table><thead><tr>' + cols.map((c) => `<th>${esc(c.name)}</th>`).join('') + '</tr></thead><tbody>'
  for (const r of rows) h += '<tr>' + r.map((v) => `<td>${esc(cellText(v))}</td>`).join('') + '</tr>'
  h += '</tbody></table></div>'
  if (t.truncated) h += `<p class="note">${esc(t('firstRows', { n: rows.length }))}</p>`
  return h
}

const CSS = `
*{box-sizing:border-box}body{margin:0;background:#fff;color:#2b2b2a;font:15px/1.7 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif}
main{max-width:860px;margin:0 auto;padding:48px 32px 64px}h1{font-size:28px;line-height:1.3;margin:0 0 6px}.meta{color:#9a9994;font-size:13px;margin-bottom:28px}
h2{font-size:21px;margin:1.6em 0 .6em}h3{font-size:17px;margin:1.3em 0 .5em}p{margin:0 0 .8em}ul,ol{padding-left:1.4em}a{color:#2e6bd9}
code{font-family:"SF Mono",Menlo,Consolas,monospace;font-size:.88em;background:rgba(135,131,120,.14);color:#c4554d;padding:1px 4px;border-radius:3px}
pre{background:#f8f8f7;border:1px solid rgba(55,53,47,.1);border-radius:6px;padding:10px 12px;overflow:auto}pre code{background:none;color:inherit;padding:0}
blockquote{margin:0 0 .8em;padding:2px 14px;border-left:3px solid rgba(55,53,47,.18);color:#64635f}
table{border-collapse:collapse;font-size:12.5px;width:100%}th,td{border:1px solid rgba(55,53,47,.14);padding:5px 8px;text-align:left;vertical-align:top}th{background:#f8f8f7;font-weight:600}
.tbl{overflow:auto;margin:12px 0}.fig{margin:16px 0;text-align:center}.fig svg{max-width:100%;height:auto}.cap{color:#64635f;font-size:13px;margin-top:4px}.note{color:#9a9994;font-size:12px}
.missing{color:#d44c47;font-size:13px;border:1px dashed #d44c47;padding:8px 12px;border-radius:6px}
@media print{main{padding:0;max-width:none}.fig,.tbl,table{break-inside:avoid}a{color:inherit}}
`

/** 生成单文件 HTML。 */
export async function buildHTML(r: ReportData): Promise<string> {
  const sn = r.snapshot || {}
  const out: string[] = []
  for (const s of splitBlocks(r.body || '')) {
    if (s.type === 'md') {
      out.push(renderMarkdown(s.text))
    } else if (s.type === 'mermaid') {
      out.push(`<div class="fig">${await mermaidSVG(s.code)}</div>`)
    } else if (s.kind === 'chart') {
      const v = sn.charts?.[s.id]
      out.push(v ? `<div class="fig">${await chartImage(v)}<div class="cap">${esc(s.caption || v.title || '')}</div></div>` : `<p class="missing">${esc(t('missingChart'))}</p>`)
    } else if (s.kind === 'diagram') {
      const v = sn.diagrams?.[s.id]
      out.push(v ? `<div class="fig">${await mermaidSVG(v.code)}<div class="cap">${esc(s.caption || v.title || '')}</div></div>` : `<p class="missing">${esc(t('missingDiagram'))}</p>`)
    } else {
      const v = sn.tables?.[s.id]
      out.push(v ? (s.caption ? `<p class="cap">${esc(s.caption)}</p>` : '') + tableHTML(v) : `<p class="missing">${esc(t('missingTable'))}</p>`)
    }
  }
  const date = r.updated_at ? new Date(r.updated_at).toLocaleString() : ''
  return `<!doctype html><html lang="${i18n.global.locale.value}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>${esc(r.title)}</title><style>${CSS}</style></head>` +
    `<body><main><h1>${esc(r.title)}</h1><div class="meta">${esc(date)}</div>${out.join('\n')}</main></body></html>`
}

export function downloadText(name: string, text: string, type: string) {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([text], { type }))
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 2000)
}

export function safeName(s: string) { return (s || 'report').replace(/[\\/:*?"<>|]+/g, '_').slice(0, 80) }

/** 打印（另存为 PDF）。 */
export function printHTML(html: string) {
  const w = window.open('', '_blank')
  if (!w) return false
  w.document.open()
  w.document.write(html)
  w.document.close()
  setTimeout(() => { w.focus(); w.print() }, 400)
  return true
}
