// 实时通道：WebSocket 收服务端事件（健康状态、备份进度、智能体事件），并执行智能体发来的浏览器端工具调用。
import { setConnId } from '@/api/client'
import { clientTool } from '@/modules/registry'

type Handler = (data: any) => void
const handlers = new Map<string, Set<Handler>>()
let ws: WebSocket | null = null
let retry = 0
let stopped = true
let workspace = ''

export function on(type: string, fn: Handler): () => void {
  if (!handlers.has(type)) handlers.set(type, new Set())
  handlers.get(type)!.add(fn)
  return () => handlers.get(type)?.delete(fn)
}

function emit(type: string, data: any) {
  handlers.get(type)?.forEach((fn) => { try { fn(data) } catch (e) { console.error(e) } })
}

function send(t: string, d?: any) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ t, d }))
}

function presence() { send('presence', { visible: document.visibilityState === 'visible', workspace }) }

export function setWorkspace(id: string) {
  workspace = id
  presence()
}

async function onClientCall(d: { id: string; tool: string; args: any }) {
  const fn = clientTool(d.tool)
  if (!fn) {
    send('client_result', { id: d.id, error: '浏览器不支持工具 ' + d.tool })
    return
  }
  try {
    const result = await fn(d.args || {})
    send('client_result', { id: d.id, result })
  } catch (e: any) {
    send('client_result', { id: d.id, error: e?.message || String(e) })
  }
}

function connect() {
  if (stopped) return
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  ws = new WebSocket(`${proto}//${location.host}/api/v1/ws`)
  ws.onopen = () => {
    retry = 0
    presence()
    emit('open', null)
  }
  ws.onmessage = (ev) => {
    let m: any
    try { m = JSON.parse(ev.data) } catch { return }
    if (m.t === 'hello') {
      setConnId(m.d?.conn_id || '')
      return
    }
    if (m.t === 'client_call') {
      onClientCall(m.d)
      return
    }
    emit(m.t, m.d)
  }
  ws.onclose = () => {
    ws = null
    setConnId('')
    emit('close', null)
    if (stopped) return
    const delay = Math.min(15000, 500 * 2 ** retry++)
    setTimeout(connect, delay)
  }
}

export function startRealtime() {
  if (!stopped) return
  stopped = false
  connect()
  document.addEventListener('visibilitychange', presence)
  setInterval(() => send('ping'), 25000)
}

export function stopRealtime() {
  stopped = true
  ws?.close()
}
