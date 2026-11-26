// 标签状态登记：表格、控制台等标签把「当前看到的内容」登记在这里，供智能体的 workspace_read_view 等浏览器端工具读取。
const getters = new Map<string, () => any>()
const actions = new Map<string, Record<string, (...args: any[]) => any>>()

export function registerTabState(tabId: string, get: () => any, acts: Record<string, (...args: any[]) => any> = {}) {
  getters.set(tabId, get)
  actions.set(tabId, acts)
}

export function unregisterTabState(tabId: string) {
  getters.delete(tabId)
  actions.delete(tabId)
}

export function readTabState(tabId: string): any {
  return getters.get(tabId)?.()
}

export function tabAction(tabId: string, name: string): ((...args: any[]) => any) | undefined {
  return actions.get(tabId)?.[name]
}
