// 智能体面板状态：会话列表、当前会话、消息、流式草稿、待处理的审批与提问、计划清单、用量。
// 运行事件用 SSE（/agent/runs/{id}/events?after=seq）订阅，断线后按序号续传、去重；WebSocket 的 run_event 只用来发现
// 别处发起的新一轮（另一个浏览器标签、排队消息）。
import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import { on } from '@/lib/realtime'
import { useSession } from '@/stores/session'
import { useTabs } from '@/stores/tabs'
import { errorText } from '@/i18n'
import type { Conversation, ConvUsage, Draft, DraftTool, Interaction, Message, ModelInfo, RunEvent, SubAgent, Todo } from './types'

export interface Usage { context: number; limit: number; window: number; total?: ConvUsage }

const DONE_STATES = ['done', 'error', 'stopped']

export const useAgent = defineStore('agent', () => {
  const session = useSession()
  const conversations = ref<Conversation[]>([])
  const listLoaded = ref(false)
  const convId = ref('')
  const conv = ref<Conversation | null>(null)
  const messages = ref<Message[]>([])
  const todos = ref<Todo[]>([])
  const usage = ref<Usage | null>(null)
  const summary = ref('')
  const runId = ref('')
  const runState = ref('')
  const runError = ref('')
  const draft = ref<Draft | null>(null)
  const pending = ref<Interaction[]>([])
  const subagents = ref<SubAgent[]>([])
  const notice = ref<{ level: string; text: string } | null>(null)
  const retrying = ref<any>(null)
  const compacting = ref(false)
  const queuedTexts = ref<string[]>([])
  const models = ref<ModelInfo[]>([])
  const defaultModel = ref('')
  const configured = ref(true)
  const modelErrors = ref<string[]>([])
  const loading = ref(false)

  const running = computed(() => !!runId.value && !DONE_STATES.includes(runState.value))
  const wsId = computed(() => session.current?.id || '')

  let es: EventSource | null = null
  let lastSeq = 0
  let reloadTimer: any = null

  // ---------- 模型 ----------
  async function loadModels() {
    if (!wsId.value) return
    try {
      const r = await api.get(`/workspaces/${wsId.value}/ai/models`)
      models.value = r.models || []
      defaultModel.value = r.default || ''
      configured.value = !!r.configured
      modelErrors.value = r.errors || []
    } catch (e) {
      modelErrors.value = [errorText(e)]
    }
  }

  // ---------- 会话 ----------
  async function loadList() {
    if (!wsId.value) return
    const r = await api.get(`/workspaces/${wsId.value}/agent/conversations?limit=200`)
    conversations.value = r.conversations || []
    listLoaded.value = true
  }

  function closeStream() {
    es?.close()
    es = null
  }

  function resetView() {
    closeStream()
    messages.value = []
    todos.value = []
    usage.value = null
    summary.value = ''
    runId.value = ''
    runState.value = ''
    runError.value = ''
    draft.value = null
    pending.value = []
    subagents.value = []
    notice.value = null
    retrying.value = null
    compacting.value = false
    queuedTexts.value = []
    lastSeq = 0
  }

  /** 打开会话；有正在执行的一轮时从当前这一步开头续传。 */
  async function open(id: string) {
    resetView()
    convId.value = id
    conv.value = null
    if (!id) return
    loading.value = true
    try {
      const r = await api.get(`/agent/conversations/${id}`)
      if (convId.value !== id) return
      conv.value = r.conversation
      messages.value = r.messages || []
      todos.value = r.todos || []
      summary.value = r.summary || ''
      if (r.context) usage.value = { context: r.context, limit: 0, window: 0, total: r.conversation.usage }
      if (r.active_run) {
        pending.value = (r.active_run.pending || []) as Interaction[]
        subscribe(r.active_run.id, r.active_run.resume_from || 0)
      }
    } finally {
      loading.value = false
    }
  }

  async function reloadMessages() {
    const id = convId.value
    if (!id) return
    const r = await api.get(`/agent/conversations/${id}`)
    if (convId.value !== id) return
    conv.value = r.conversation
    messages.value = r.messages || []
    todos.value = r.todos || []
    summary.value = r.summary || ''
  }

  function scheduleReload(clearDraft: boolean) {
    clearTimeout(reloadTimer)
    const d = draft.value
    reloadTimer = setTimeout(async () => {
      try { await reloadMessages() } catch {}
      if (clearDraft && draft.value === d) draft.value = null
    }, 60)
  }

  async function create(settings?: Partial<Conversation['settings']>): Promise<Conversation> {
    const r = await api.post(`/workspaces/${wsId.value}/agent/conversations`, { settings: settings || {} })
    const c = r.conversation as Conversation
    conversations.value.unshift(c)
    return c
  }

  /** 回到空白新对话；forget 为真时（用户主动点「新对话」）也不再自动恢复上次的会话。 */
  function newChat(forget = false) {
    resetView()
    convId.value = ''
    conv.value = null
    if (forget && wsId.value) {
      try { localStorage.removeItem('lemi.agent.conv.' + wsId.value) } catch { /* 忽略 */ }
    }
  }

  async function patch(id: string, body: any) {
    const r = await api.patch(`/agent/conversations/${id}`, body)
    const c = r.conversation as Conversation
    const i = conversations.value.findIndex((x) => x.id === id)
    if (c.archived_at) {
      if (i >= 0) conversations.value.splice(i, 1)
    } else if (i >= 0) conversations.value[i] = c
    if (conv.value?.id === id) conv.value = c
    return c
  }

  async function remove(id: string) {
    await api.del(`/agent/conversations/${id}`)
    conversations.value = conversations.value.filter((c) => c.id !== id)
    if (convId.value === id) newChat()
  }

  // ---------- 发送与运行 ----------
  function clientContext() {
    const tabs = useTabs()
    return {
      open_tabs: tabs.tabs.slice(0, 20).map((t) => ({ id: t.id, kind: t.type, connection_id: t.params?.connectionId || t.connectionId || '', db: t.params?.database || '', table: t.params?.table || '', title: t.title })),
      active_tab: tabs.activeId,
    }
  }

  async function send(text: string, opts: { delivery?: 'steer' | 'queue'; model?: string; effort?: string; mode?: string } = {}) {
    let id = convId.value
    if (!id) {
      const c = await create({ model: opts.model || '', effort: opts.effort || '', mode: (opts.mode as any) || 'normal' })
      convId.value = id = c.id
      conv.value = c
    }
    const r = await api.post(`/agent/conversations/${id}/messages`, { text, delivery: opts.delivery || 'steer', model: opts.model, effort: opts.effort, mode: opts.mode, context: clientContext() })
    if (r.message) {
      if (!messages.value.some((m) => m.id === r.message.id)) messages.value.push(r.message)
    }
    if (r.steered || r.queued) {
      queuedTexts.value.push(text)
      return
    }
    if (r.run_id && r.run_id !== runId.value) subscribe(r.run_id, 0)
    // 会话出现在列表最上面
    const i = conversations.value.findIndex((c) => c.id === id)
    if (i > 0) conversations.value.unshift(conversations.value.splice(i, 1)[0])
  }

  async function stop() {
    if (runId.value) await api.post(`/agent/runs/${runId.value}/stop`)
  }

  async function respond(id: string, reply: 'once' | 'always' | 'reject', message = '', answers?: any[]) {
    if (!runId.value) return
    await api.post(`/agent/runs/${runId.value}/respond`, { id, reply, message, answers })
    pending.value = pending.value.filter((p) => p.id !== id)
  }

  function subscribe(id: string, after: number) {
    closeStream()
    runId.value = id
    runState.value = 'running'
    runError.value = ''
    lastSeq = after
    const src = new EventSource(`/api/v1/agent/runs/${id}/events?after=${after}`)
    es = src
    src.onmessage = (m) => {
      let ev: RunEvent
      try { ev = JSON.parse(m.data) } catch { return }
      if (ev.seq <= lastSeq) return
      lastSeq = ev.seq
      handle(ev)
    }
    src.onerror = () => {
      // 浏览器会带着 Last-Event-ID 自动重连；运行已经不存在时（404）停在 CLOSED
      if (src.readyState === EventSource.CLOSED && es === src) {
        es = null
        if (running.value) {
          runState.value = 'done'
          scheduleReload(true)
        }
      }
    }
  }

  function tool(callId: string): DraftTool | undefined {
    return draft.value?.tools.find((t) => t.call_id === callId)
  }

  function ensureDraft(id = ''): Draft {
    if (!draft.value) draft.value = { id: id || 'd' + Date.now(), text: '', reasoning: '', tools: [] }
    return draft.value
  }

  function handle(ev: RunEvent) {
    const d = ev.data || {}
    switch (ev.type) {
      case 'run_start':
        runState.value = 'running'
        break
      case 'user_message':
        if (d?.id && !messages.value.some((m) => m.id === d.id)) {
          messages.value.push(d)
          if (d.meta?.steer || queuedTexts.value.length) {
            const txt = (d.parts || []).map((p: any) => p.text || '').join('')
            queuedTexts.value = queuedTexts.value.filter((q) => q !== txt)
          }
        }
        break
      case 'step_start':
        retrying.value = null
        draft.value = { id: 'd' + ev.seq, text: '', reasoning: '', tools: [] }
        break
      case 'assistant_start':
        ensureDraft(d.id)
        break
      case 'text':
        ensureDraft().text += d.text || ''
        retrying.value = null
        break
      case 'reasoning':
        ensureDraft().reasoning += d.text || ''
        break
      case 'tool_input_start':
        ensureDraft().tools.push({ call_id: d.call_id, name: d.name, title: d.title, argsText: '', status: 'input' })
        break
      case 'tool_input_delta': {
        const t = tool(d.call_id)
        if (t) t.argsText += d.text || ''
        break
      }
      case 'tool_call': {
        let t = tool(d.call_id)
        if (!t) {
          t = { call_id: d.call_id, name: d.name, title: d.title, argsText: '', status: 'pending' }
          ensureDraft().tools.push(t)
        }
        Object.assign(t, { name: d.name, title: d.title, args: d.args, status: 'pending' })
        break
      }
      case 'tool_running': {
        const t = tool(d.call_id)
        if (t) t.status = 'running'
        break
      }
      case 'tool_result': {
        const t = tool(d.call_id)
        if (t) Object.assign(t, { status: d.status, text: d.text, data: d.data, duration: d.duration })
        if (d.name === 'report_write' && d.data?.report_id) window.dispatchEvent(new CustomEvent('lemi:report-changed', { detail: d.data.report_id }))
        break
      }
      case 'step_reset':
        draft.value = { id: 'd' + ev.seq, text: '', reasoning: '', tools: [] }
        break
      case 'retry':
        retrying.value = d
        break
      case 'message_saved':
        if (d.role === 'tool' || (d.role === 'assistant' && !draft.value?.tools.length)) scheduleReload(true)
        else if (d.role === 'user') scheduleReload(false)
        break
      case 'usage':
        usage.value = d
        if (conv.value && d.total) conv.value.usage = d.total
        break
      case 'approval':
      case 'question':
      case 'doom_loop':
        if (!pending.value.some((p) => p.id === d.id)) pending.value.push(d)
        break
      case 'interaction_done':
        pending.value = pending.value.filter((p) => p.id !== d.id)
        break
      case 'todos':
        todos.value = d.todos || []
        break
      case 'compaction_start':
        compacting.value = true
        break
      case 'compaction_done':
        compacting.value = false
        if (d.ok) notice.value = { level: 'info', text: 'compacted' }
        break
      case 'subagent_start':
        subagents.value.push({ task_id: d.task_id, description: d.description, type: d.type, done: false, steps: [] })
        break
      case 'subagent_event': {
        const s = subagents.value.find((x) => x.task_id === d.task_id)
        if (s && (d.type === 'tool_call' || d.type === 'tool_result')) {
          if (d.type === 'tool_call') s.steps.push({ type: 'call', name: d.data?.name, title: d.data?.title })
          else {
            const st = [...s.steps].reverse().find((x) => x.name === d.data?.name && !x.status)
            if (st) st.status = d.data?.status
          }
        }
        break
      }
      case 'subagent_done': {
        const s = subagents.value.find((x) => x.task_id === d.task_id)
        if (s) Object.assign(s, { done: true, ok: d.ok, error: d.error })
        break
      }
      case 'notice':
        notice.value = { level: d.level, text: d.text }
        break
      case 'title':
        if (conv.value) conv.value.title = d.title
        {
          const c = conversations.value.find((x) => x.id === convId.value)
          if (c) c.title = d.title
        }
        break
      case 'next_run':
        if (d.run_id) setTimeout(() => subscribe(d.run_id, 0), 0)
        break
      case 'run_done':
        runState.value = d.status
        runError.value = d.status === 'error' ? d.error || '' : ''
        retrying.value = null
        compacting.value = false
        pending.value = []
        closeStream()
        scheduleReload(true)
        loadList().catch(() => {})
        break
    }
  }

  // 别处发起的新一轮（另一个标签、排队消息）也接上
  on('run_event', (d: any) => {
    if (!d || d.conversation_id !== convId.value) return
    if (d.type === 'title' && !d.run_id) {
      if (conv.value) conv.value.title = d.data?.title
      return
    }
    if (d.type === 'run_start' && d.run_id && d.run_id !== runId.value && !es) subscribe(d.run_id, 0)
  })

  watch(wsId, () => {
    newChat()
    conversations.value = []
    listLoaded.value = false
    models.value = []
  })

  // 记住每个空间最后打开的会话，刷新页面或重新打开面板时接着看
  const lastKey = () => 'lemi.agent.conv.' + (wsId.value || '')
  watch(convId, (id) => {
    if (!wsId.value || !id) return
    try { localStorage.setItem(lastKey(), id) } catch { /* 存储不可用时不记 */ }
  })
  async function restore() {
    if (convId.value || !wsId.value) return
    let id = ''
    try { id = localStorage.getItem(lastKey()) || '' } catch { /* 忽略 */ }
    if (!id) return
    try { await open(id) } catch {
      newChat()
      try { localStorage.removeItem(lastKey()) } catch { /* 忽略 */ }
    }
  }

  return {
    conversations, listLoaded, convId, conv, messages, todos, usage, summary, runId, runState, runError, draft, pending, subagents, notice, retrying,
    compacting, queuedTexts, models, defaultModel, configured, modelErrors, loading, running,
    loadModels, loadList, open, newChat, create, patch, remove, send, stop, respond, reloadMessages, restore,
  }
})
