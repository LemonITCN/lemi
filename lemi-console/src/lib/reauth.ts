// 敏感操作的二次验证：接口返回 reauth_required 时弹出验证框，验证通过后自动重试一次。
import { reactive } from 'vue'
import { ApiError } from '@/api/client'

export const reauthState = reactive({ open: false })
let waiter: ((ok: boolean) => void) | null = null

export function askReauth(): Promise<boolean> {
  waiter?.(false)
  reauthState.open = true
  return new Promise((res) => { waiter = res })
}

export function finishReauth(ok: boolean) {
  reauthState.open = false
  const w = waiter
  waiter = null
  w?.(ok)
}

/** 执行 fn；遇到 reauth_required 时先验证身份再重试。 */
export async function withReauth<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn()
  } catch (e) {
    if (e instanceof ApiError && e.code === 'reauth_required') {
      if (await askReauth()) return await fn()
    }
    throw e
  }
}
