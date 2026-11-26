// Markdown 渲染：markdown-it 关闭 HTML，再经 DOMPurify 清洗（§5.2）；链接一律新窗口打开并去掉来源。
// 智能体回复与报告里的引用块（```lemi-chart <id>```、```lemi-table <结果ID>```、```lemi-diagram <id>```）与 mermaid 代码块
// 先切出来，由组件渲染。
import MarkdownIt from 'markdown-it'
import DOMPurify from 'dompurify'

const md = new MarkdownIt({ html: false, linkify: true, breaks: false, typographer: false })

// 链接：只允许 http(s)、mailto 与站内路径
const defaultValidate = md.validateLink.bind(md)
md.validateLink = (url: string) => {
  const u = url.trim().toLowerCase()
  if (u.startsWith('javascript:') || u.startsWith('vbscript:') || u.startsWith('data:') || u.startsWith('file:')) return false
  return defaultValidate(url)
}

let hooked = false
function hookPurify() {
  if (hooked) return
  hooked = true
  DOMPurify.addHook('afterSanitizeAttributes', (node: Element) => {
    if (node.tagName === 'A') {
      const href = node.getAttribute('href') || ''
      if (!href.startsWith('/') && !href.startsWith('#')) {
        node.setAttribute('target', '_blank')
        node.setAttribute('rel', 'noopener noreferrer')
      }
    }
  })
}

/** Markdown → 安全的 HTML。 */
export function renderMarkdown(src: string): string {
  hookPurify()
  return DOMPurify.sanitize(md.render(src || ''), { USE_PROFILES: { html: true }, FORBID_TAGS: ['style', 'iframe', 'form', 'input'], FORBID_ATTR: ['style'] })
}

export type Segment =
  | { type: 'md'; text: string }
  | { type: 'lemi'; kind: 'chart' | 'table' | 'diagram'; id: string; caption: string }
  | { type: 'mermaid'; code: string }

const lemiRe = /^```lemi-(chart|table|diagram)[ \t]+([A-Za-z0-9]+)[ \t]*(```)?[ \t]*$/
const fenceOpen = /^(```+|~~~+)\s*([\w-]*)/

/** 把正文切成 Markdown 段、引用块与 mermaid 图（与服务端 reports.ParseBlocks 的规则一致）。 */
export function splitBlocks(src: string): Segment[] {
  const lines = (src || '').split('\n')
  const out: Segment[] = []
  let buf: string[] = []
  const flush = () => {
    if (buf.length && buf.join('\n').trim()) out.push({ type: 'md', text: buf.join('\n') })
    buf = []
  }
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].replace(/\r$/, '')
    const m = lemiRe.exec(line)
    if (m) {
      flush()
      let caption = ''
      if (!m[3]) {
        const cap: string[] = []
        let j = i + 1
        for (; j < lines.length; j++) {
          if (lines[j].trim() === '```') break
          cap.push(lines[j])
        }
        caption = cap.join('\n').trim()
        i = j
      }
      out.push({ type: 'lemi', kind: m[1] as any, id: m[2].toLowerCase(), caption })
      continue
    }
    const f = fenceOpen.exec(line)
    if (f) {
      const fence = f[1]
      const lang = f[2].toLowerCase()
      const body: string[] = []
      let j = i + 1
      for (; j < lines.length; j++) {
        if (lines[j].trim().startsWith(fence[0].repeat(fence.length)) && lines[j].trim().replace(/[`~]/g, '') === '') break
        body.push(lines[j])
      }
      if (lang === 'mermaid') {
        flush()
        out.push({ type: 'mermaid', code: body.join('\n') })
        i = j
        continue
      }
      // 其他代码块原样留给 markdown-it
      buf.push(line, ...body)
      if (j < lines.length) buf.push(lines[j])
      i = j
      continue
    }
    buf.push(line)
  }
  flush()
  return out
}

/** 单元格（服务端编码：字符串 / null / {$bin,len} / {$trunc,len}）转文本。 */
export function cellText(v: any): string {
  if (v === null || v === undefined) return 'NULL'
  if (typeof v === 'object') {
    if ('$trunc' in v) return v.$trunc + '…'
    if ('$bin' in v) return `0x(${v.len} B)`
    return JSON.stringify(v)
  }
  return String(v)
}
