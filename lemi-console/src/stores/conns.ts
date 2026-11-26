// 连接与结构缓存：当前工作空间的连接列表、驱动描述、各连接的库与对象（按需加载）。
import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import { useSession } from './session'

export interface FormField {
  key: string; label: string; label_en?: string; type: string; group?: string; required?: boolean; secret?: boolean
  default?: any; placeholder?: string; help?: string; options?: { value: string; label: string }[]; show_if?: Record<string, string[]>
}
export interface DriverInfo {
  id: string; name: string; category: string; query_language: string; default_port?: number; file_based?: boolean
  form: FormField[]; capabilities: string[]; network: boolean
}
export interface Conn {
  id: string; workspace_id: string; name: string; driver: string; color: string; folder: string; env: '' | 'prod' | 'test' | 'dev'
  read_only: boolean; ai_level: string; ai_allow: string[]; notes: string; access: 'read' | 'write' | 'manage'
  params: Record<string, any>; network?: Record<string, Record<string, any>>; secrets_set?: string[]; created_at: number; updated_at: number
}
export interface DatabaseInfo { name: string; charset?: string; collation?: string; system?: boolean; size_bytes?: number; tables?: number }
export interface ObjectInfo { name: string; comment?: string; rows?: number; size_bytes?: number; engine?: string; table?: string; detail?: string }
export interface ObjectList { tables: ObjectInfo[]; views: ObjectInfo[]; procedures: ObjectInfo[]; functions: ObjectInfo[]; triggers: ObjectInfo[]; events: ObjectInfo[] }

export const useConns = defineStore('conns', () => {
  const session = useSession()
  const list = ref<Conn[]>([])
  const folders = ref<string[]>([])
  const drivers = ref<DriverInfo[]>([])
  const loading = ref(false)
  const dbs = ref<Record<string, DatabaseInfo[]>>({})
  const objects = ref<Record<string, ObjectList>>({})
  const health = ref<Record<string, { status: string; latency_ms?: number; error?: string }>>({})

  const byId = computed(() => Object.fromEntries(list.value.map((c) => [c.id, c])))

  async function loadDrivers() {
    if (drivers.value.length) return
    drivers.value = (await api.get('/drivers')).drivers
  }

  function driver(id: string) { return drivers.value.find((d) => d.id === id) }

  async function load() {
    if (!session.current) return
    loading.value = true
    try {
      await loadDrivers()
      const r = await api.get(`/workspaces/${session.current.id}/connections`)
      list.value = r.connections
      folders.value = r.folders || []
    } finally {
      loading.value = false
    }
  }

  async function loadDatabases(connId: string, force = false) {
    if (dbs.value[connId] && !force) return dbs.value[connId]
    const r = await api.get(`/connections/${connId}/databases`)
    dbs.value[connId] = r.databases
    return r.databases as DatabaseInfo[]
  }

  async function loadObjects(connId: string, db: string, force = false) {
    const k = connId + '\u0000' + db
    if (objects.value[k] && !force) return objects.value[k]
    const r = await api.get(`/connections/${connId}/databases/${encodeURIComponent(db)}/objects`)
    objects.value[k] = r
    return r as ObjectList
  }

  function invalidate(connId: string) {
    delete dbs.value[connId]
    for (const k of Object.keys(objects.value)) if (k.startsWith(connId + '\u0000')) delete objects.value[k]
  }

  watch(() => session.current?.id, () => {
    list.value = []
    dbs.value = {}
    objects.value = {}
    load()
  })

  return { list, folders, drivers, loading, dbs, objects, health, byId, load, loadDrivers, driver, loadDatabases, loadObjects, invalidate }
})

/** 打开某个数据库对象对应的标签页（各模块与智能体共用）。 */
export const openers = {
  console(t: ReturnType<typeof import('./tabs').useTabs>, c: Conn, database = '', opts: { sql?: string; fromAI?: boolean; title?: string } = {}) {
    return t.open('console', { connectionId: c.id, database, sql: opts.sql || '' }, { title: opts.title || `${c.name}${database ? ' · ' + database : ''}`, connectionId: c.id, fromAI: opts.fromAI })
  },
  table(t: ReturnType<typeof import('./tabs').useTabs>, c: Conn, database: string, table: string, params: Record<string, any> = {}, fromAI = false) {
    return t.open('table', { connectionId: c.id, database, table, ...params }, { title: table, key: `table:${c.id}:${database}:${table}`, connectionId: c.id, fromAI })
  },
}
