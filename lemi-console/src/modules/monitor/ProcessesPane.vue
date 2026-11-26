<script setup lang="ts">
// MySQL 进程列表：按耗时降序，可自动刷新（标签不活动时暂停）、隐藏 Sleep、文字筛选，逐行杀查询 / 杀连接。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { Tab } from '@/modules/registry'
import IconBtn from '@/components/ui/IconBtn.vue'
import Icon from '@/components/icons/Icon.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import Switch from '@/components/ui/Switch.vue'
import { api } from '@/api/client'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { confirmDialog } from '@/lib/confirm'
import { copyText } from '@/lib/cm'
import { errorText, i18n } from '@/i18n'
import { fetchMonitor, formatClock, formatInt, num } from './format'

type Proc = Record<string, string | null>

const props = defineProps<{ tab: Tab; connId: string; connName: string; active: boolean; canKill: boolean; prod: boolean; mobile: boolean }>()
const tabs = useTabs()
const toast = useToast()
const t = i18n.global.t

const rows = ref<Proc[]>([])
const updatedAt = ref(0)
const error = ref('')
const loading = ref(false)
const loaded = ref(false)
const interval = ref<string>(String(props.tab.params.procInterval ?? '5'))
const hideSleep = ref<boolean>(props.tab.params.hideSleep !== false)
const filter = ref('')
const expanded = ref<Record<string, boolean>>({})
let timer: ReturnType<typeof setTimeout> | undefined
let alive = true

watch([interval, hideSleep], ([i, h]) => tabs.setParams(props.tab.id, { procInterval: i, hideSleep: h }))

async function load() {
  clearTimeout(timer)
  if (!alive || loading.value) return
  loading.value = true
  try {
    const r = await fetchMonitor<Proc[]>(props.connId, 'processes')
    if (!alive) return
    rows.value = r.data || []
    updatedAt.value = r.at
    error.value = ''
    loaded.value = true
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
  schedule()
}

function schedule() {
  clearTimeout(timer)
  const s = Number(interval.value)
  if (alive && props.active && s > 0) timer = setTimeout(load, s * 1000)
}

watch(() => props.active, (a) => { if (a) load(); else clearTimeout(timer) }, { immediate: true })
watch(interval, () => schedule())
onBeforeUnmount(() => { alive = false; clearTimeout(timer) })

const intervals = computed(() => [
  { value: '0', label: t('monitor.off') }, { value: '2', label: '2s' }, { value: '5', label: '5s' }, { value: '10', label: '10s' },
])

const shown = computed(() => {
  const q = filter.value.trim().toLowerCase()
  return rows.value
    .filter((p) => !hideSleep.value || String(p.command || '').toLowerCase() !== 'sleep')
    .filter((p) => !q || ['id', 'user', 'host', 'db', 'command', 'state', 'info'].some((k) => String(p[k] ?? '').toLowerCase().includes(q)))
    .slice()
    .sort((a, b) => num(b.time) - num(a.time))
})

function idOf(p: Proc) { return String(p.id ?? '') }

async function kill(p: Proc, queryOnly: boolean) {
  const id = idOf(p)
  const who = `${p.user || '?'}@${p.host || '?'}`
  const stmt = String(p.info || '').slice(0, 200)
  const lines = [t(queryOnly ? 'monitor.killQueryHint' : 'monitor.killConnHint', { who })]
  if (stmt) lines.push(stmt)
  if (props.prod) lines.push(t('monitor.prodWarn', { name: props.connName }))
  const ok = await confirmDialog({
    title: t(queryOnly ? 'monitor.killQueryTitle' : 'monitor.killConnTitle', { id }),
    message: lines.join('\n\n'),
    danger: true,
    okText: t(queryOnly ? 'monitor.killQuery' : 'monitor.killConn'),
  })
  if (!ok) return
  try {
    await api.post(`/connections/${props.connId}/monitor/kill`, { id, query_only: queryOnly })
    toast.success(t('monitor.killed', { id }))
    load()
  } catch (e) {
    toast.error(errorText(e))
  }
}

function menu(p: Proc): MenuItem[] {
  const items: MenuItem[] = []
  if (p.info) items.push({ label: t('monitor.copySql'), icon: 'copy', run: async () => { if (await copyText(String(p.info))) toast.success(t('common.copied')) } })
  if (props.canKill) {
    if (items.length) items.push({ separator: true })
    items.push({ label: t('monitor.killQuery'), icon: 'stop', danger: true, disabled: !p.info, run: () => kill(p, true) })
    items.push({ label: t('monitor.killConn'), icon: 'close', danger: true, run: () => kill(p, false) })
  }
  return items
}

function toggle(p: Proc) {
  const id = idOf(p)
  if (p.info) expanded.value[id] = !expanded.value[id]
}

function timeClass(p: Proc) {
  const s = num(p.time)
  // 空闲连接与后台线程（事件调度器、复制线程）的「耗时」只是存活时长，不算慢
  const cmd = String(p.command || '').toLowerCase()
  if (cmd === 'sleep' || cmd === 'daemon' || cmd.startsWith('binlog dump') || cmd === 'connect') return ''
  return s >= 30 ? 'slow' : s >= 5 ? 'warm' : ''
}
</script>

<template>
  <div class="procs">
    <div class="bar">
      <div class="search"><Icon name="search" :size="14" /><input v-model="filter" :placeholder="$t('monitor.filterProc')" spellcheck="false" /></div>
      <label class="sw"><Switch v-model="hideSleep" /><span>{{ $t('monitor.hideSleep') }}</span></label>
      <span class="lm-spacer" />
      <label class="iv"><Icon name="clock" :size="14" />
        <select v-model="interval" class="iv-sel" :aria-label="$t('monitor.autoRefresh')"><option v-for="o in intervals" :key="o.value" :value="o.value">{{ o.label }}</option></select>
      </label>
      <span v-if="updatedAt" class="lm-faint upd">{{ formatClock(updatedAt) }}</span>
      <IconBtn icon="refresh" :size="16" :label="$t('common.refresh')" :disabled="loading" @click="load" />
    </div>
    <p v-if="error" class="err" role="alert">{{ error }}</p>
    <div v-if="!loaded && !error" class="lm-empty">{{ $t('common.loading') }}</div>
    <div v-else-if="loaded && !shown.length" class="lm-empty">{{ rows.length ? $t('monitor.noMatch') : $t('monitor.noProcesses') }}</div>

    <template v-else-if="shown.length">
      <div class="count lm-faint">{{ $t('monitor.procCount', { n: shown.length, total: rows.length }) }}</div>
      <!-- 电脑：表格 -->
      <div v-if="!mobile" class="scroll-x">
        <table class="lm-table grid">
          <thead>
            <tr><th class="w-id">ID</th><th>{{ $t('monitor.user') }}</th><th>{{ $t('monitor.host') }}</th><th>{{ $t('monitor.db') }}</th><th>{{ $t('monitor.command') }}</th>
              <th class="r">{{ $t('monitor.timeSec') }}</th><th>{{ $t('monitor.state') }}</th><th class="w-sql">{{ $t('monitor.statement') }}</th><th class="w-act" /></tr>
          </thead>
          <tbody>
            <template v-for="p in shown" :key="idOf(p)">
              <tr :class="{ open: expanded[idOf(p)] }">
                <td class="lm-mono">{{ p.id }}</td>
                <td class="ell" :title="p.user || ''">{{ p.user }}</td>
                <td class="ell lm-mono" :title="p.host || ''">{{ p.host }}</td>
                <td class="ell" :title="p.db || ''">{{ p.db || '—' }}</td>
                <td>{{ p.command }}</td>
                <td class="r num" :class="timeClass(p)">{{ formatInt(p.time) }}</td>
                <td class="ell" :title="p.state || ''">{{ p.state || '—' }}</td>
                <td class="sql" @click="toggle(p)"><span class="lm-mono ell1" :class="{ clickable: !!p.info }" :title="p.info ? $t('monitor.expand') : ''">{{ p.info || '—' }}</span></td>
                <td class="w-act"><Menu v-if="menu(p).length" :items="menu(p)" align="end"><IconBtn icon="more" :size="15" :label="$t('common.more')" /></Menu></td>
              </tr>
              <tr v-if="expanded[idOf(p)]" class="detail"><td colspan="9"><pre class="lm-mono full-sql">{{ p.info }}</pre></td></tr>
            </template>
          </tbody>
        </table>
      </div>
      <!-- 手机：卡片 -->
      <div v-else class="cards">
        <div v-for="p in shown" :key="idOf(p)" class="card">
          <div class="c-head">
            <span class="lm-mono cid">#{{ p.id }}</span>
            <span class="ell who">{{ p.user }}@{{ p.host }}</span>
            <span class="lm-badge" :class="{ danger: timeClass(p) === 'slow', warning: timeClass(p) === 'warm' }">{{ formatInt(p.time) }}s</span>
            <Menu v-if="menu(p).length" :items="menu(p)" align="end"><IconBtn icon="more" :size="16" :label="$t('common.more')" /></Menu>
          </div>
          <div class="c-meta lm-faint">{{ [p.db, p.command, p.state].filter(Boolean).join(' · ') }}</div>
          <pre v-if="p.info" class="lm-mono c-sql" :class="{ clamp: !expanded[idOf(p)] }" @click="toggle(p)">{{ p.info }}</pre>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.procs { min-width: 0; }
.bar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.search { display: flex; align-items: center; gap: 6px; height: 30px; padding: 0 8px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); color: var(--text-3); flex: 0 1 280px; min-width: 0; }
.search:focus-within { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.search input { flex: 1; min-width: 0; border: 0; outline: none; background: transparent; font-size: 13px; color: var(--text); }
.sw { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-2); cursor: pointer; white-space: nowrap; }
.iv { display: inline-flex; align-items: center; gap: 4px; color: var(--text-3); }
.iv-sel { height: 28px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); background: var(--bg); padding: 0 4px; font-size: 12.5px; color: var(--text); }
.upd { font-size: 12px; font-variant-numeric: tabular-nums; }
.err { margin: 0 0 10px; padding: 8px 10px; border-radius: var(--radius-m); background: var(--danger-bg); color: var(--danger); font-size: 13px; word-break: break-word; }
.count { font-size: 12px; margin-bottom: 6px; }
.scroll-x { overflow-x: auto; border: 1px solid var(--border); border-radius: var(--radius-l); }
.grid { table-layout: fixed; min-width: 980px; }
.grid td { padding: 5px 10px; }
.w-id { width: 80px; }
.w-sql { width: 34%; }
.w-act { width: 44px; text-align: right; }
.r { text-align: right !important; }
.num { font-variant-numeric: tabular-nums; }
.num.warm { color: var(--warning); font-weight: 600; }
.num.slow { color: var(--danger); font-weight: 600; }
.ell { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sql { min-width: 0; }
.ell1 { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.clickable { cursor: pointer; }
.clickable:hover { color: var(--accent); }
tr.open > td { background: var(--bg-soft); }
tr.detail > td { background: var(--bg-soft); }
.full-sql { margin: 0; white-space: pre-wrap; word-break: break-all; max-height: 240px; overflow: auto; }
.cards { display: flex; flex-direction: column; gap: 8px; }
.card { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 8px 10px; min-width: 0; }
.c-head { display: flex; align-items: center; gap: 8px; min-width: 0; }
.cid { color: var(--text-2); flex: none; }
.who { flex: 1; min-width: 0; font-size: 13px; }
.c-meta { font-size: 12px; margin: 2px 0 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.c-sql { margin: 0; white-space: pre-wrap; word-break: break-all; background: var(--bg-sunken); border-radius: var(--radius-s); padding: 6px 8px; }
.c-sql.clamp { display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
</style>
