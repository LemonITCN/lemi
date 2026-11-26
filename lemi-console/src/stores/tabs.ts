// 主工作区的标签页：按工作空间分别保存在浏览器里，刷新后恢复。
import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import type { Tab } from '@/modules/registry'
import { useSession } from './session'

let seq = 0
function newId() { return 't' + Date.now().toString(36) + (++seq).toString(36) }

export const useTabs = defineStore('tabs', () => {
  const session = useSession()
  const tabs = ref<Tab[]>([])
  const activeId = ref('')
  let loadedFor = ''

  const active = computed(() => tabs.value.find((t) => t.id === activeId.value) || null)

  function storageKey() { return 'lemi.tabs.' + (session.currentId || 'none') }

  function load() {
    loadedFor = session.currentId
    try {
      const raw = localStorage.getItem(storageKey())
      const d = raw ? JSON.parse(raw) : null
      tabs.value = Array.isArray(d?.tabs) ? d.tabs : []
      activeId.value = d?.active || tabs.value[0]?.id || ''
    } catch {
      tabs.value = []
      activeId.value = ''
    }
  }

  function save() {
    if (loadedFor !== session.currentId) return
    try {
      localStorage.setItem(storageKey(), JSON.stringify({ tabs: tabs.value.map((t) => ({ ...t, dirty: false })), active: activeId.value }))
    } catch {}
  }

  watch(() => session.currentId, load, { immediate: true })
  watch([tabs, activeId], save, { deep: true })

  /** 打开标签；key 相同的已有标签直接激活（并合并参数）。 */
  function open(type: string, params: Record<string, any>, opts: { title: string; key?: string; activate?: boolean; fromAI?: boolean; connectionId?: string; mergeParams?: boolean } ): Tab {
    if (opts.key) {
      const ex = tabs.value.find((t) => t.key === opts.key)
      if (ex) {
        if (opts.mergeParams !== false) ex.params = { ...ex.params, ...params }
        ex.title = opts.title || ex.title
        if (opts.fromAI) ex.fromAI = true
        if (opts.activate !== false) activeId.value = ex.id
        return ex
      }
    }
    const t: Tab = { id: newId(), type, title: opts.title, params, key: opts.key, fromAI: opts.fromAI, connectionId: opts.connectionId }
    const idx = tabs.value.findIndex((x) => x.id === activeId.value)
    tabs.value.splice(idx >= 0 ? idx + 1 : tabs.value.length, 0, t)
    if (opts.activate !== false) activeId.value = t.id
    return t
  }

  function close(id: string) {
    const i = tabs.value.findIndex((t) => t.id === id)
    if (i < 0) return
    tabs.value.splice(i, 1)
    if (activeId.value === id) activeId.value = tabs.value[Math.min(i, tabs.value.length - 1)]?.id || ''
  }

  function closeOthers(id: string) {
    tabs.value = tabs.value.filter((t) => t.id === id)
    activeId.value = id
  }

  function closeAll() {
    tabs.value = []
    activeId.value = ''
  }

  function activate(id: string) { activeId.value = id }

  function update(id: string, patch: Partial<Tab>) {
    const t = tabs.value.find((x) => x.id === id)
    if (t) Object.assign(t, patch)
  }

  function setParams(id: string, params: Record<string, any>) {
    const t = tabs.value.find((x) => x.id === id)
    if (t) t.params = { ...t.params, ...params }
  }

  function move(from: number, to: number) {
    const [t] = tabs.value.splice(from, 1)
    tabs.value.splice(to, 0, t)
  }

  /** 连接被删除时关闭它的标签。 */
  function closeForConnection(connId: string) {
    tabs.value = tabs.value.filter((t) => t.connectionId !== connId)
    if (!tabs.value.find((t) => t.id === activeId.value)) activeId.value = tabs.value[0]?.id || ''
  }

  return { tabs, activeId, active, open, close, closeOthers, closeAll, activate, update, setParams, move, closeForConnection }
})
