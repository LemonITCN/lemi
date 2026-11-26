<script setup lang="ts">
// 运行记录：按连接、计划、状态筛选，倒序分页；运行中的显示实时进度，结束时自动刷新。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import Icon from '@/components/icons/Icon.vue'
import ProgressBar from './ProgressBar.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useConns } from '@/stores/conns'
import { useToast } from '@/stores/toast'
import { formatDate } from '@/lib/time'
import { errorText } from '@/i18n'
import { formatBytes, formatDuration, live, onProgress, statusClass, type Plan, type Run } from './api'

const props = defineProps<{ plans: Plan[]; connFilter: string; planFilter: string }>()
const emit = defineEmits<{ openRun: [id: string]; filter: [f: { connection?: string; plan?: string }] }>()
const session = useSession()
const conns = useConns()
const toast = useToast()
const runs = ref<Run[]>([])
const cursor = ref('')
const loading = ref(false)
const loaded = ref(false)
const status = ref('')

async function load(reset = true) {
  const ws = session.current?.id
  if (!ws) return
  loading.value = true
  try {
    const q = new URLSearchParams({ limit: '50' })
    if (props.connFilter) q.set('connection', props.connFilter)
    if (props.planFilter) q.set('plan', props.planFilter)
    if (status.value) q.set('status', status.value)
    if (!reset && cursor.value) q.set('cursor', cursor.value)
    const r = await api.get(`/workspaces/${ws}/backups/runs?${q}`)
    runs.value = reset ? r.runs || [] : [...runs.value, ...(r.runs || [])]
    cursor.value = r.next_cursor || ''
  } catch (e) { toast.error(errorText(e)) } finally { loading.value = false; loaded.value = true }
}
onMounted(() => load())
watch(() => [props.connFilter, props.planFilter, status.value, session.current?.id], () => load())
defineExpose({ load })

let timer: ReturnType<typeof setTimeout> | null = null
const off = onProgress((p) => {
  if (p.kind !== 'backup') return
  const known = runs.value.some((r) => r.id === p.id)
  if (p.done || !known) {
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => load(), p.done ? 300 : 0)
  }
})
onBeforeUnmount(() => { off(); if (timer) clearTimeout(timer) })
</script>

<template>
  <div class="rp">
    <div class="filters">
      <select :value="connFilter" class="lm-select" @change="emit('filter', { connection: ($event.target as HTMLSelectElement).value, plan: '' })">
        <option value="">{{ $t('backup.allConns') }}</option>
        <option v-for="c in conns.list.filter((x) => x.driver === 'mysql' || x.driver === 'sqlite')" :key="c.id" :value="c.id">{{ c.name }}</option>
      </select>
      <select :value="planFilter" class="lm-select" @change="emit('filter', { connection: connFilter, plan: ($event.target as HTMLSelectElement).value })">
        <option value="">{{ $t('common.all') }} · {{ $t('backup.sections.plans') }}</option>
        <option v-for="p in plans.filter((x) => !connFilter || x.connection_id === connFilter)" :key="p.id" :value="p.id">{{ p.name }}</option>
      </select>
      <select v-model="status" class="lm-select">
        <option value="">{{ $t('common.all') }} · {{ $t('common.status') }}</option>
        <option v-for="s in ['running', 'success', 'failed', 'canceled']" :key="s" :value="s">{{ $t('backup.status.' + s) }}</option>
      </select>
      <Btn size="sm" variant="ghost" icon="refresh" :loading="loading" @click="load()">{{ $t('common.refresh') }}</Btn>
    </div>
    <div v-if="loaded && !runs.length" class="lm-card lm-empty">{{ $t('backup.runs.empty') }}</div>
    <div v-else class="lm-card list">
      <button v-for="r in runs" :key="r.id" type="button" class="row" @click="emit('openRun', r.id)">
        <span class="st"><span class="lm-badge" :class="statusClass[r.status]">{{ $t('backup.status.' + r.status) }}</span></span>
        <span class="main">
          <span class="t1 lm-ellipsis">{{ r.connection_name }}<span v-if="r.plan_name" class="lm-faint"> · {{ r.plan_name }}</span></span>
          <span class="t2 lm-ellipsis">{{ formatDate(r.started_at, true) }} · {{ $t('backup.trigger.' + r.trigger) }} · {{ $t('backup.method.' + r.method) }}<template v-if="r.encrypted"> · <Icon name="lock" :size="11" /></template></span>
          <ProgressBar v-if="r.status === 'running' && live[r.id]" :p="live[r.id]" compact class="prog" />
          <span v-else-if="r.error" class="t3 lm-ellipsis">{{ r.error }}</span>
        </span>
        <span class="meta">
          <span>{{ r.status === 'success' ? formatBytes(r.size) : '' }}</span>
          <span class="lm-faint">{{ r.finished_at ? formatDuration(r.finished_at - r.started_at) : '' }}</span>
        </span>
        <span class="vf">
          <span v-if="r.verify_status" class="lm-badge" :class="statusClass[r.verify_status]">{{ $t('backup.verify.' + r.verify_status) }}</span>
        </span>
        <Icon name="chevron-right" :size="14" class="chev" />
      </button>
      <div v-if="cursor" class="more"><Btn size="sm" variant="ghost" :loading="loading" @click="load(false)">{{ $t('backup.runs.loadMore') }}</Btn></div>
    </div>
  </div>
</template>

<style scoped>
.filters { display: flex; gap: 8px; margin-bottom: 12px; flex-wrap: wrap; align-items: center; }
.filters .lm-select { width: auto; min-width: 150px; max-width: 240px; height: 30px; font-size: 12.5px; }
.row { width: 100%; display: flex; align-items: center; gap: 12px; padding: 10px 12px; border: 0; border-bottom: 1px solid var(--border); background: none; text-align: left; }
.row:hover { background: var(--bg-hover); }
.list > .row:last-of-type { border-bottom: 0; }
.st { width: 64px; flex: none; }
.main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.t1 { font-size: 13px; font-weight: 500; }
.t2 { font-size: 12px; color: var(--text-3); display: flex; align-items: center; gap: 4px; }
.t3 { font-size: 12px; color: var(--danger); }
.prog { margin-top: 4px; max-width: 420px; }
.meta { display: flex; flex-direction: column; align-items: flex-end; font-size: 12px; width: 80px; flex: none; }
.vf { width: 84px; flex: none; text-align: right; }
.chev { color: var(--text-3); flex: none; }
.more { padding: 8px; text-align: center; }
@media (max-width: 760px) {
  .filters .lm-select { flex: 1 1 140px; max-width: none; }
  .row { flex-wrap: wrap; gap: 6px 10px; }
  .st { width: auto; }
  .main { flex-basis: calc(100% - 100px); order: 1; }
  .st { order: 0; }
  .meta { flex-direction: row; gap: 8px; width: auto; order: 2; margin-left: 0; }
  .vf { width: auto; order: 3; }
  .chev { display: none; }
}
</style>
