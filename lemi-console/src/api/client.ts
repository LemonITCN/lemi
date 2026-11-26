// 统一的接口调用：自动带 CSRF 令牌与实时连接 ID，错误统一成 ApiError。
let csrf = ''
let connId = ''

export function setCsrf(t: string | undefined) { csrf = t || '' }
export function getCsrf() { return csrf }
export function setConnId(id: string) { connId = id }
export function getConnId() { return connId }

export class ApiError extends Error {
  code: string
  status: number
  extra?: any
  constructor(status: number, code: string, message: string, extra?: any) {
    super(message)
    this.status = status
    this.code = code
    this.extra = extra
  }
}

type Listener = (e: ApiError) => void
const authListeners: Listener[] = []
/** 会话失效、需要二次验证等全局错误的监听。 */
export function onAuthError(fn: Listener) { authListeners.push(fn) }

export interface RequestOptions {
  signal?: AbortSignal
  raw?: boolean
  headers?: Record<string, string>
}

const BASE = '/api/v1'

export async function request<T = any>(method: string, path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
  const url = path.startsWith('/api/') || path.startsWith('/oauth') ? path : BASE + path
  const headers: Record<string, string> = { Accept: 'application/json', 'X-Requested-With': 'lemi', ...(opts.headers || {}) }
  let payload: BodyInit | undefined
  if (body instanceof FormData) {
    payload = body
    headers['X-Lemi-Upload'] = '1'
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  if (method !== 'GET' && csrf) headers['X-Lemi-CSRF'] = csrf
  if (connId) headers['X-Lemi-Conn'] = connId
  let res: Response
  try {
    res = await fetch(url, { method, headers, body: payload, credentials: 'same-origin', signal: opts.signal })
  } catch (e: any) {
    if (e?.name === 'AbortError') throw e
    throw new ApiError(0, 'network', '无法连接服务器，请检查网络')
  }
  if (opts.raw) return res as any
  let data: any = null
  const text = await res.text()
  if (text) {
    try { data = JSON.parse(text) } catch { data = null }
  }
  if (!res.ok) {
    const err = data?.error || {}
    const e = new ApiError(res.status, err.code || 'http_' + res.status, err.message || `请求失败（${res.status}）`, err.extra)
    const global = ['session_invalid', 'unauthorized', 'reauth_required', 'must_change_password', 'totp_setup_required']
    if (global.includes(e.code)) authListeners.forEach((fn) => fn(e))
    throw e
  }
  return data as T
}

export const api = {
  get: <T = any>(p: string, o?: RequestOptions) => request<T>('GET', p, undefined, o),
  post: <T = any>(p: string, b?: unknown, o?: RequestOptions) => request<T>('POST', p, b ?? {}, o),
  put: <T = any>(p: string, b?: unknown, o?: RequestOptions) => request<T>('PUT', p, b ?? {}, o),
  patch: <T = any>(p: string, b?: unknown, o?: RequestOptions) => request<T>('PATCH', p, b ?? {}, o),
  del: <T = any>(p: string, b?: unknown, o?: RequestOptions) => request<T>('DELETE', p, b, o),
}

/** 触发浏览器原生下载（一次性下载链接）。 */
export function downloadURL(url: string) {
  const a = document.createElement('a')
  a.href = url
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  a.remove()
}
