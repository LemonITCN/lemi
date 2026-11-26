// 通知渠道缓存：当前空间的渠道清单与渠道类型描述（备份计划、健康检查选渠道时共用）。
import { defineStore } from 'pinia'
import { ref, watch } from 'vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { isZh } from '@/i18n'

export interface DynField {
  key: string; label: string; label_en?: string; type: string; required?: boolean; secret?: boolean; default?: any
  placeholder?: string; help?: string; options?: (string | { value: string; label: string })[]; show_if?: Record<string, string[]>
}
export interface ChannelKind { id: string; name: string; name_en: string; help?: string; fields: DynField[] }
export interface EventInfo { id: string; name: string; name_en: string }
export interface Channel {
  id: string; workspace_id: string; name: string; kind: string; enabled: boolean; events: string[]
  config?: Record<string, any>; secrets_set?: string[]; url_host?: string; created_at: number; updated_at: number
}

export function kindName(k: { name: string; name_en: string } | undefined) {
  if (!k) return ''
  return isZh() ? k.name : k.name_en || k.name
}

export const useChannels = defineStore('notify-channels', () => {
  const session = useSession()
  const list = ref<Channel[]>([])
  const kinds = ref<ChannelKind[]>([])
  const events = ref<EventInfo[]>([])
  const systemSmtp = ref(false)
  const loaded = ref('')

  async function load(force = false) {
    const ws = session.current?.id
    if (!ws) return
    if (!force && loaded.value === ws) return
    const [k, c] = await Promise.all([api.get(`/workspaces/${ws}/notify/kinds`), api.get(`/workspaces/${ws}/notify/channels`)])
    kinds.value = k.kinds || []
    events.value = k.events || []
    systemSmtp.value = !!k.system_smtp
    list.value = c.channels || []
    loaded.value = ws
  }

  function kind(id: string) { return kinds.value.find((k) => k.id === id) }
  function byId(id: string) { return list.value.find((c) => c.id === id) }

  watch(() => session.current?.id, () => { loaded.value = ''; list.value = [] })

  return { list, kinds, events, systemSmtp, load, kind, byId }
})
