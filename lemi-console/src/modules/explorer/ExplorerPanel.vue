<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import CtxMenu from "@/components/ui/ContextMenu.vue"
import type { MenuItem } from "@/components/ui/Menu.vue"
import ConnectionDialog from './ConnectionDialog.vue'
import { useConns, openers, type Conn, type ObjectInfo } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { useSession } from '@/stores/session'
import { useUI } from '@/stores/ui'
import { useToast } from '@/stores/toast'
import { api } from '@/api/client'
import { errorText, i18n } from '@/i18n'
import { confirmDialog } from '@/lib/confirm'
import { treeActions } from './actions'

const conns = useConns()
const tabs = useTabs()
const session = useSession()
const ui = useUI()
const toast = useToast()
const t = i18n.global.t

const filter = ref('')
const expanded = reactive<Record<string, boolean>>({})
const loadingKeys = reactive<Record<string, boolean>>({})
const errors = reactive<Record<string, string>>({})
const dialog = ref<{ open: boolean; conn?: Conn | null }>({ open: false })
const selected = ref('')

onMounted(() => { if (!conns.list.length) conns.load() })

const groups: { key: keyof import('@/stores/conns').ObjectList; label: string; icon: string; kind: string }[] = [
  { key: 'tables', label: 'explorer.tables', icon: 'table', kind: 'table' },
  { key: 'views', label: 'explorer.views', icon: 'view', kind: 'view' },
  { key: 'procedures', label: 'explorer.procedures', icon: 'procedure', kind: 'procedure' },
  { key: 'functions', label: 'explorer.functions', icon: 'function', kind: 'function' },
  { key: 'triggers', label: 'explorer.triggers', icon: 'trigger', kind: 'trigger' },
  { key: 'events', label: 'explorer.events', icon: 'event', kind: 'event' },
]

const tree = computed(() => {
  const q = filter.value.trim().toLowerCase()
  const byFolder: Record<string, Conn[]> = {}
  for (const c of conns.list) {
    if (q && !c.name.toLowerCase().includes(q)) {
      // 过滤：连接名不匹配时看已加载的对象名
      const hit = Object.entries(conns.objects).some(([k, o]) => k.startsWith(c.id + '\u0000') &&
        [...o.tables, ...o.views].some((x) => x.name.toLowerCase().includes(q)))
      if (!hit) continue
    }
    ;(byFolder[c.folder] ||= []).push(c)
  }
  return Object.keys(byFolder).sort().map((f) => ({ folder: f, items: byFolder[f] }))
})

async function toggle(key: string, load?: () => Promise<any>) {
  expanded[key] = !expanded[key]
  if (expanded[key] && load) {
    loadingKeys[key] = true
    delete errors[key]
    try {
      await load()
    } catch (e: any) {
      errors[key] = errorText(e)
      if (e?.code === 'ssh_host_key_unknown' || e?.code === 'ssh_host_key_mismatch') {
        const c = conns.byId[key.split(':')[1]]
        if (c && c.access === 'manage') dialog.value = { open: true, conn: c }
      }
    } finally {
      loadingKeys[key] = false
    }
  }
}

function dbList(c: Conn) {
  const list = conns.dbs[c.id] || []
  const def = c.params?.database
  return def ? list.filter((d) => d.name === def) : list
}

function objs(c: Conn, db: string) { return conns.objects[c.id + '\u0000' + db] }

function matches(o: ObjectInfo) {
  const q = filter.value.trim().toLowerCase()
  return !q || o.name.toLowerCase().includes(q)
}

function envClass(c: Conn) { return c.env ? 'env-' + c.env : '' }

function connMenu(c: Conn): MenuItem[] {
  const items: MenuItem[] = [
    { label: t('explorer.newConsole'), icon: 'terminal', run: () => { openers.console(tabs, c); if (ui.isMobile) ui.rail = ui.rail } },
    { label: t('common.refresh'), icon: 'refresh', run: () => refreshConn(c) },
    { label: t('explorer.test'), icon: 'plug', run: () => testConn(c) },
    ...treeActions('connection', { conn: c }),
  ]
  if (c.access === 'manage') {
    items.push({ separator: true }, { label: t('common.edit'), icon: 'edit', run: () => (dialog.value = { open: true, conn: c }) },
      { label: t('common.delete'), icon: 'trash', danger: true, run: () => removeConn(c) })
  }
  return items
}

function dbMenu(c: Conn, db: string): MenuItem[] {
  return [
    { label: t('explorer.newConsole'), icon: 'terminal', run: () => openers.console(tabs, c, db) },
    { label: t('common.refresh'), icon: 'refresh', run: () => conns.loadObjects(c.id, db, true) },
    ...treeActions('database', { conn: c, database: db }),
  ]
}

function objMenu(c: Conn, db: string, kind: string, o: ObjectInfo): MenuItem[] {
  const items: MenuItem[] = []
  if (kind === 'table' || kind === 'view') items.push({ label: t('explorer.openData'), icon: 'table', run: () => openers.table(tabs, c, db, o.name) })
  items.push({ label: t('explorer.copyName'), icon: 'copy', run: () => navigator.clipboard.writeText(o.name) })
  items.push(...treeActions(kind, { conn: c, database: db, name: o.name, object: o }))
  return items
}

function openObject(c: Conn, db: string, kind: string, o: ObjectInfo) {
  selected.value = `${c.id}:${db}:${kind}:${o.name}`
  if (kind === 'table' || kind === 'view') openers.table(tabs, c, db, o.name)
  else tabs.open('ddl', { connectionId: c.id, database: db, kind, name: o.name }, { title: o.name, key: `ddl:${c.id}:${db}:${kind}:${o.name}`, connectionId: c.id })
}

async function refreshConn(c: Conn) {
  conns.invalidate(c.id)
  if (expanded['c:' + c.id]) {
    try { await conns.loadDatabases(c.id, true) } catch (e) { toast.error(errorText(e)) }
  }
}

async function testConn(c: Conn) {
  try {
    const r = await api.post(`/connections/${c.id}/test`)
    toast.success(t('explorer.testOk', { product: r.server.product, version: r.server.version, ms: r.latency_ms }))
  } catch (e) {
    toast.error(errorText(e))
  }
}

async function removeConn(c: Conn) {
  if (!(await confirmDialog({ title: t('explorer.deleteTitle', { name: c.name }), message: t('explorer.deleteHint'), danger: true, okText: t('common.delete') }))) return
  try {
    await api.del(`/connections/${c.id}`)
    tabs.closeForConnection(c.id)
    await conns.load()
  } catch (e) {
    toast.error(errorText(e))
  }
}

function onSaved() {
  dialog.value = { open: false }
  conns.load()
}

const canCreate = computed(() => session.atLeast('admin'))
const fmtSize = (n?: number) => {
  if (!n) return ''
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return v.toFixed(v < 10 && i > 0 ? 1 : 0) + ' ' + u[i]
}
</script>

<template>
  <div class="explorer">
    <div class="lm-panel-head">
      <span>{{ $t('explorer.title') }}</span>
      <span class="lm-spacer" />
      <IconBtn v-if="canCreate" icon="plus" :label="$t('explorer.newConnection')" :size="15" @click="dialog = { open: true, conn: null }" />
      <IconBtn icon="refresh" :label="$t('common.refresh')" :size="15" @click="conns.load()" />
    </div>
    <div class="search">
      <Icon name="search" :size="14" />
      <input v-model="filter" :placeholder="$t('explorer.filter')" spellcheck="false" />
    </div>
    <div class="tree" role="tree">
      <div v-if="!conns.list.length && !conns.loading" class="lm-empty">
        <p>{{ $t('explorer.empty') }}</p>
        <button v-if="canCreate" class="link" @click="dialog = { open: true, conn: null }">{{ $t('explorer.newConnection') }}</button>
      </div>
      <template v-for="g in tree" :key="g.folder">
        <div v-if="g.folder" class="node folder" @click="toggle('f:' + g.folder)">
          <Icon :name="expanded['f:' + g.folder] === false ? 'chevron-right' : 'chevron-down'" :size="12" class="tw" />
          <Icon name="folder" :size="15" />
          <span class="lm-ellipsis">{{ g.folder }}</span>
        </div>
        <template v-if="!g.folder || expanded['f:' + g.folder] !== false">
          <div v-for="c in g.items" :key="c.id" class="conn" :class="envClass(c)">
            <CtxMenu :items="connMenu(c)" :width="220">
              <div class="node" :style="{ paddingLeft: g.folder ? '18px' : '6px' }" :class="{ prod: c.env === 'prod' }"
                @click="toggle('c:' + c.id, () => conns.loadDatabases(c.id))">
                <Icon :name="expanded['c:' + c.id] ? 'chevron-down' : 'chevron-right'" :size="12" class="tw" />
                <span class="cdot" :style="{ background: c.color || 'var(--text-3)' }" />
                <Icon :name="c.driver === 'sqlite' ? 'file' : 'server'" :size="15" />
                <span class="lm-ellipsis name">{{ c.name }}</span>
                <span v-if="c.env" class="lm-badge" :class="{ danger: c.env === 'prod', warning: c.env === 'test', success: c.env === 'dev' }">{{ $t('explorer.env.' + c.env) }}</span>
                <Icon v-if="c.read_only || c.access === 'read'" name="lock" :size="12" class="ro" />
                <span v-if="conns.health[c.id]" class="hdot" :class="conns.health[c.id].status" />
                <span v-if="loadingKeys['c:' + c.id]" class="spin" />
              </div>
            </CtxMenu>
            <div v-if="errors['c:' + c.id]" class="err" :style="{ paddingLeft: (g.folder ? 40 : 28) + 'px' }">{{ errors['c:' + c.id] }}</div>
            <template v-if="expanded['c:' + c.id]">
              <div v-for="d in dbList(c)" :key="d.name">
                <CtxMenu :items="dbMenu(c, d.name)" :width="220">
                  <div class="node" :style="{ paddingLeft: (g.folder ? 34 : 22) + 'px' }" :class="{ sys: d.system }"
                    @click="toggle(`d:${c.id}:${d.name}`, () => conns.loadObjects(c.id, d.name))">
                    <Icon :name="expanded[`d:${c.id}:${d.name}`] ? 'chevron-down' : 'chevron-right'" :size="12" class="tw" />
                    <Icon name="database" :size="15" />
                    <span class="lm-ellipsis name">{{ d.name }}</span>
                    <span class="meta">{{ fmtSize(d.size_bytes) }}</span>
                    <span v-if="loadingKeys[`d:${c.id}:${d.name}`]" class="spin" />
                  </div>
                </CtxMenu>
                <template v-if="expanded[`d:${c.id}:${d.name}`] && objs(c, d.name)">
                  <template v-for="grp in groups" :key="grp.key">
                    <template v-if="objs(c, d.name)[grp.key]?.length">
                      <div class="node" :style="{ paddingLeft: (g.folder ? 50 : 38) + 'px' }" @click="toggle(`g:${c.id}:${d.name}:${grp.key}`)">
                        <Icon :name="expanded[`g:${c.id}:${d.name}:${grp.key}`] === false ? 'chevron-right' : 'chevron-down'" :size="12" class="tw" />
                        <Icon name="folder" :size="14" />
                        <span class="lm-ellipsis">{{ $t(grp.label) }}</span>
                        <span class="meta">{{ objs(c, d.name)[grp.key].length }}</span>
                      </div>
                      <template v-if="expanded[`g:${c.id}:${d.name}:${grp.key}`] !== false">
                        <CtxMenu v-for="o in objs(c, d.name)[grp.key].filter(matches)" :key="o.name" :items="objMenu(c, d.name, grp.kind, o)" :width="220">
                          <div class="node leaf" :class="{ sel: selected === `${c.id}:${d.name}:${grp.kind}:${o.name}` }" :style="{ paddingLeft: (g.folder ? 66 : 54) + 'px' }"
                            :title="o.comment || o.name" @dblclick="openObject(c, d.name, grp.kind, o)" @click="selected = `${c.id}:${d.name}:${grp.kind}:${o.name}`; ui.isMobile && openObject(c, d.name, grp.kind, o)">
                            <Icon :name="grp.icon" :size="14" />
                            <span class="lm-ellipsis name">{{ o.name }}</span>
                            <span v-if="o.comment" class="meta cmt lm-ellipsis">{{ o.comment }}</span>
                          </div>
                        </CtxMenu>
                      </template>
                    </template>
                  </template>
                </template>
              </div>
            </template>
          </div>
        </template>
      </template>
    </div>
    <ConnectionDialog v-if="dialog.open" v-model:open="dialog.open" :conn="dialog.conn || null" @saved="onSaved" />
  </div>
</template>

<style scoped>
.explorer { flex: 1; display: flex; flex-direction: column; min-height: 0; }
.search { display: flex; align-items: center; gap: 6px; margin: 8px; padding: 0 8px; height: 28px; border-radius: var(--radius-m); background: var(--bg); border: 1px solid var(--border); color: var(--text-3); }
.search input { flex: 1; border: 0; background: none; outline: none; font-size: 12.5px; color: var(--text); min-width: 0; }
.tree { flex: 1; overflow: auto; padding-bottom: 24px; font-size: 13px; }
.node { display: flex; align-items: center; gap: 5px; height: 24px; padding-right: 8px; cursor: default; user-select: none; white-space: nowrap; color: var(--text); }
.node:hover { background: var(--bg-hover); }
.node.sel { background: var(--accent-soft); }
.node.sys { color: var(--text-2); }
.node .lm-icon { color: var(--text-2); flex: none; }
.tw { color: var(--text-3) !important; }
.node.leaf { padding-left: 54px; }
.name { min-width: 0; }
.meta { margin-left: auto; color: var(--text-3); font-size: 11px; padding-left: 8px; }
.meta.cmt { max-width: 45%; }
.folder { padding-left: 6px; font-weight: 500; }
.cdot { width: 3px; height: 14px; border-radius: 2px; flex: none; }
.node.prod { box-shadow: inset 2px 0 0 var(--danger); }
.ro { color: var(--text-3) !important; }
.hdot { width: 7px; height: 7px; border-radius: 50%; margin-left: 4px; background: var(--text-3); flex: none; }
.hdot.up { background: var(--success); }
.hdot.down { background: var(--danger); }
.err { color: var(--danger); font-size: 12px; padding: 2px 8px 6px; white-space: normal; }
.spin { width: 11px; height: 11px; border-radius: 50%; border: 2px solid var(--text-3); border-right-color: transparent; animation: spin .7s linear infinite; margin-left: 4px; flex: none; }
@keyframes spin { to { transform: rotate(360deg); } }
.link { border: 0; background: none; color: var(--accent); }
@media (max-width: 760px) { .node { height: 36px; font-size: 14px; } }
</style>
