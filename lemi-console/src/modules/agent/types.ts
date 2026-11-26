// 智能体接口的数据类型（docs/spec.md §24.3–24.4）。

export interface ModelInfo {
  ref: string
  id: string
  name: string
  provider_id: string
  provider_name: string
  context: number
  max_output: number
  vision?: boolean
  efforts?: string[]
  default_effort?: string
  api: string
  featured?: boolean
}

export interface ConvSettings { model: string; effort: string; mode: 'normal' | 'plan' }

export interface ConvUsage { input: number; output: number; cache_read: number; cache_write: number; reasoning: number; requests: number }

export interface Conversation {
  id: string
  workspace_id: string
  kind: 'chat' | 'task' | 'sub'
  title: string
  settings: ConvSettings
  usage: ConvUsage
  created_at: number
  updated_at: number
  archived_at?: number
  running?: boolean
  run_id?: string
  task_id?: string
}

export interface Part {
  type: 'text' | 'reasoning' | 'tool_call' | 'tool_result' | 'image'
  text?: string
  call_id?: string
  name?: string
  args?: any
  is_error?: boolean
  title?: string
  data?: any
  status?: 'done' | 'error' | 'denied' | 'interrupted' | 'skipped' | 'lost' | string
  duration?: number
}

export interface Message {
  id: string
  seq: number
  role: 'user' | 'assistant' | 'tool'
  parts: Part[]
  meta?: any
  created_at: number
}

export interface Todo { content: string; status: 'pending' | 'in_progress' | 'completed' | 'cancelled'; priority?: string }

export interface RunEvent { seq: number; type: string; data: any; at: number }

export interface Interaction {
  id: string
  kind: 'approval' | 'question' | 'doom_loop'
  [k: string]: any
}

/** 正在流式生成的一步。 */
export interface DraftTool {
  call_id: string
  name: string
  title: string
  argsText: string
  args?: any
  status: 'input' | 'pending' | 'running' | 'done' | 'error' | 'denied' | 'interrupted' | 'skipped' | 'lost'
  text?: string
  data?: any
  duration?: number
}

export interface Draft {
  id: string
  text: string
  reasoning: string
  tools: DraftTool[]
}

export interface SubAgent {
  task_id: string
  description: string
  type: string
  done: boolean
  ok?: boolean
  error?: string
  steps: { type: string; name?: string; title?: string; status?: string }[]
}

/** 渲染用的一项。 */
export type Item =
  | { kind: 'user'; key: string; text: string; at: number; steer?: boolean }
  | { kind: 'text'; key: string; text: string }
  | { kind: 'reasoning'; key: string; text: string; live?: boolean }
  | { kind: 'tool'; key: string; call: { call_id: string; name: string; title?: string; args?: any; argsText?: string }; result?: { status: string; text?: string; data?: any; duration?: number; is_error?: boolean } }
  | { kind: 'error'; key: string; text: string }

/** 把存储的消息转成渲染项：工具调用与其结果配对。 */
export function toItems(msgs: Message[]): Item[] {
  const results = new Map<string, Part>()
  for (const m of msgs) if (m.role === 'tool') for (const p of m.parts) if (p.type === 'tool_result' && p.call_id) results.set(p.call_id, p)
  const out: Item[] = []
  for (const m of msgs) {
    if (m.role === 'user') {
      const text = m.parts.filter((p) => p.type === 'text').map((p) => p.text || '').join('\n')
      if (text.trim()) out.push({ kind: 'user', key: m.id, text, at: m.created_at, steer: !!m.meta?.steer })
      continue
    }
    if (m.role !== 'assistant') continue
    m.parts.forEach((p, i) => {
      const key = m.id + ':' + i
      if (p.type === 'text') {
        if (p.status === 'error') out.push({ kind: 'error', key, text: p.data?.error || '' })
        else if (p.text?.trim()) out.push({ kind: 'text', key, text: p.text })
      } else if (p.type === 'reasoning' && p.text?.trim()) {
        out.push({ kind: 'reasoning', key, text: p.text })
      } else if (p.type === 'tool_call' && p.call_id) {
        const r = results.get(p.call_id)
        out.push({ kind: 'tool', key: p.call_id, call: { call_id: p.call_id, name: p.name || '', title: p.title, args: p.args },
          result: r ? { status: r.status || (r.is_error ? 'error' : 'done'), text: r.text, data: r.data, duration: r.duration, is_error: r.is_error } : undefined })
      }
    })
  }
  return out
}

/** 流式草稿转渲染项。 */
export function draftItems(d: Draft | null): Item[] {
  if (!d) return []
  const out: Item[] = []
  if (d.reasoning.trim()) out.push({ kind: 'reasoning', key: d.id + ':r', text: d.reasoning, live: true })
  if (d.text.trim()) out.push({ kind: 'text', key: d.id + ':t', text: d.text })
  for (const t of d.tools) {
    const done = ['done', 'error', 'denied', 'interrupted', 'skipped', 'lost'].includes(t.status)
    out.push({ kind: 'tool', key: t.call_id, call: { call_id: t.call_id, name: t.name, title: t.title, args: t.args, argsText: t.argsText },
      result: done ? { status: t.status, text: t.text, data: t.data, duration: t.duration } : { status: t.status } })
  }
  return out
}
