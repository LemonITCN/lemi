<script setup lang="ts">
// MySQL 全局状态：每 5 秒取一次 SHOW GLOBAL STATUS，按相邻两次的差值算每秒速率，保留最近 60 个点画曲线。
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import Segmented from '@/components/ui/Segmented.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import MetricChart, { type MetricSeries } from './MetricChart.vue'
import KvTable from './KvTable.vue'
import { errorText, i18n } from '@/i18n'
import { formatBytes } from '@/lib/meta'
import { fetchMonitor, formatClock, formatCompact, formatInt, formatRate, formatUptime, num } from './format'

const props = defineProps<{ connId: string; active: boolean }>()
const t = i18n.global.t
const INTERVAL = 5000
const KEEP = 60

interface Sample { at: number; qps: number | null; tps: number | null; conn: number | null; running: number | null; hit: number | null; rx: number | null; tx: number | null }

const samples = shallowRef<Sample[]>([])
const latest = shallowRef<Record<string, string> | null>(null)
const updatedAt = ref(0)
const error = ref('')
const loading = ref(false)
const view = ref<'chart' | 'table'>('chart')
let prev: { at: number; s: Record<string, string> } | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let alive = true

function ingest(at: number, s: Record<string, string>) {
  const next: Sample = { at, qps: null, tps: null, conn: num(s.Threads_connected), running: num(s.Threads_running), hit: null, rx: null, tx: null }
  const list = samples.value.slice()
  // 服务器重启（Uptime 变小）时计数器归零，丢弃上一次的基准
  if (prev && at > prev.at && num(s.Uptime) >= num(prev.s.Uptime)) {
    const p = prev.s
    const dt = (at - prev.at) / 1000
    const d = (k: string) => Math.max(0, num(s[k]) - num(p[k]))
    next.qps = d('Questions') / dt
    next.tps = (d('Com_commit') + d('Com_rollback')) / dt
    const req = d('Innodb_buffer_pool_read_requests')
    const rd = d('Innodb_buffer_pool_reads')
    const last = list.length ? list[list.length - 1].hit : null
    next.hit = req > 0 ? Math.min(100, Math.max(0, (1 - rd / req) * 100)) : last
    next.rx = d('Bytes_received') / dt
    next.tx = d('Bytes_sent') / dt
    // 暂停后再取数：中间插一个空点，曲线断开而不是连成斜线
    if (dt * 1000 > INTERVAL * 3) list.push({ at: prev.at + 1, qps: null, tps: null, conn: null, running: null, hit: null, rx: null, tx: null })
  }
  if (next.hit === null) {
    const req = num(s.Innodb_buffer_pool_read_requests)
    if (req > 0) next.hit = Math.min(100, Math.max(0, (1 - num(s.Innodb_buffer_pool_reads) / req) * 100))
  }
  list.push(next)
  samples.value = list.slice(-KEEP)
  prev = { at, s }
}

async function poll() {
  clearTimeout(timer)
  // 上一次请求还没回来时不重复发，它回来后会接着排下一次
  if (!alive || loading.value) return
  loading.value = true
  try {
    const r = await fetchMonitor<Record<string, string>>(props.connId, 'status')
    if (!alive) return
    latest.value = r.data || {}
    updatedAt.value = r.at
    error.value = ''
    ingest(r.at, r.data || {})
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
  clearTimeout(timer)
  if (alive && props.active) timer = setTimeout(poll, INTERVAL)
}

// 标签不活动时暂停取数
watch(() => props.active, (a) => { if (a) poll(); else clearTimeout(timer) }, { immediate: true })
onBeforeUnmount(() => { alive = false; clearTimeout(timer) })

function series(key: keyof Sample, name: string, slot: 1 | 2 = 1): MetricSeries {
  return { name, slot, data: samples.value.map((x) => [x.at, x[key] as number | null]) }
}
function lastOf(key: keyof Sample): number | null {
  for (let i = samples.value.length - 1; i >= 0; i--) {
    const v = samples.value[i][key]
    if (v !== null && v !== undefined) return v as number
  }
  return null
}

const fmtRate = (v: number) => formatCompact(v)
const fmtPct = (v: number) => v.toFixed(v >= 99.95 || v < 10 ? 1 : 2) + '%'
const fmtInt = (v: number) => formatCompact(v, 0)
/** 坐标轴刻度：小于 10 的小数保留到能区分相邻刻度的位数（0.05 / 0.1 / 0.15），避免出现两个「0.2」。 */
const fmtAxis = (v: number) => (Math.abs(v) < 10 && v % 1 ? String(Math.round(v * 1000) / 1000) : formatCompact(v))

interface Card { key: string; title: string; tip?: string; series: MetricSeries[]; values: { name: string; slot: 1 | 2; value: string }[]; format: (v: number) => string; axisFormat?: (v: number) => string; min?: number; max?: number }

const cards = computed<Card[]>(() => {
  const show = (v: number | null, f: (n: number) => string) => (v === null ? '—' : f(v))
  return [
    { key: 'qps', title: t('monitor.qps'), tip: t('monitor.qpsTip'), series: [series('qps', 'QPS')], values: [{ name: 'QPS', slot: 1, value: show(lastOf('qps'), fmtRate) }], format: fmtRate, axisFormat: fmtAxis, min: 0 },
    { key: 'tps', title: t('monitor.tps'), tip: t('monitor.tpsTip'), series: [series('tps', 'TPS')], values: [{ name: 'TPS', slot: 1, value: show(lastOf('tps'), fmtRate) }], format: fmtRate, axisFormat: fmtAxis, min: 0 },
    {
      key: 'conn', title: t('monitor.connections'), series: [series('conn', t('monitor.connected'), 1), series('running', t('monitor.running'), 2)],
      values: [{ name: t('monitor.connected'), slot: 1, value: show(lastOf('conn'), fmtInt) }, { name: t('monitor.running'), slot: 2, value: show(lastOf('running'), fmtInt) }],
      format: (v) => formatInt(v), axisFormat: fmtInt, min: 0,
    },
    { key: 'hit', title: t('monitor.hitRatio'), tip: t('monitor.hitTip'), series: [series('hit', t('monitor.hitRatio'))], values: [{ name: '', slot: 1, value: show(lastOf('hit'), fmtPct) }], format: fmtPct, axisFormat: (v) => v.toFixed(v % 1 ? 1 : 0) + '%', max: 100 },
    {
      key: 'net', title: t('monitor.network'), series: [series('rx', t('monitor.received'), 1), series('tx', t('monitor.sent'), 2)],
      values: [{ name: t('monitor.received'), slot: 1, value: show(lastOf('rx'), formatRate) }, { name: t('monitor.sent'), slot: 2, value: show(lastOf('tx'), formatRate) }],
      format: formatRate, min: 0,
    },
  ]
})

const keyRows = computed(() => {
  const s = latest.value
  if (!s) return []
  const bytes = (k: string) => formatBytes(num(s[k]))
  const int = (k: string) => formatInt(s[k])
  const rows: [string, string][] = [
    ['Uptime', s.Uptime !== undefined ? formatUptime(s.Uptime) : '—'],
    ['Threads_connected', int('Threads_connected')], ['Threads_running', int('Threads_running')], ['Max_used_connections', int('Max_used_connections')],
    ['Connections', int('Connections')], ['Aborted_connects', int('Aborted_connects')], ['Aborted_clients', int('Aborted_clients')],
    ['Questions', int('Questions')], ['Slow_queries', int('Slow_queries')], ['Com_select', int('Com_select')], ['Com_insert', int('Com_insert')],
    ['Com_update', int('Com_update')], ['Com_delete', int('Com_delete')], ['Innodb_row_lock_waits', int('Innodb_row_lock_waits')],
    ['Created_tmp_disk_tables', int('Created_tmp_disk_tables')], ['Select_full_join', int('Select_full_join')], ['Open_tables', int('Open_tables')],
    ['Opened_tables', int('Opened_tables')], ['Table_locks_waited', int('Table_locks_waited')], ['Bytes_received', bytes('Bytes_received')], ['Bytes_sent', bytes('Bytes_sent')],
  ]
  return rows.filter(([k]) => k === 'Uptime' || s[k] !== undefined)
})

const tableRows = computed(() => samples.value.filter((x) => x.conn !== null).slice().reverse())
const viewOptions = computed(() => [{ value: 'chart', label: t('monitor.viewChart') }, { value: 'table', label: t('monitor.viewTable') }])
const showAll = ref(false)
</script>

<template>
  <div class="status lm-monitor">
    <div class="bar">
      <Segmented v-model="view" :options="viewOptions" />
      <span class="lm-spacer" />
      <span v-if="updatedAt" class="lm-faint upd" :class="{ busy: loading }">{{ $t('monitor.updated', { time: formatClock(updatedAt) }) }} · {{ $t('monitor.every5s') }}</span>
    </div>
    <p v-if="error" class="err" role="alert">{{ error }}</p>
    <div v-if="!samples.length && !error" class="lm-empty">{{ $t('common.loading') }}</div>

    <template v-if="samples.length">
      <div v-if="view === 'chart'" class="cards">
        <section v-for="c in cards" :key="c.key" class="card">
          <header class="card-head">
            <span class="card-title">{{ c.title }}<InfoTip v-if="c.tip" :text="c.tip" /></span>
            <div v-if="c.values.length === 1" class="big">{{ c.values[0].value }}</div>
            <div v-else class="legend">
              <span v-for="v in c.values" :key="v.name" class="lg"><i class="key" :class="'s' + v.slot" /><b>{{ v.value }}</b><span class="lg-name">{{ v.name }}</span></span>
            </div>
          </header>
          <MetricChart :series="c.series" :format="c.format" :axis-format="c.axisFormat" :min="c.min" :max="c.max" />
        </section>
      </div>
      <div v-else class="scroll-x">
        <table class="lm-table nums">
          <thead><tr><th>{{ $t('monitor.time') }}</th><th>QPS</th><th>TPS</th><th>{{ $t('monitor.connected') }}</th><th>{{ $t('monitor.running') }}</th>
            <th>{{ $t('monitor.hitRatio') }}</th><th>{{ $t('monitor.received') }}</th><th>{{ $t('monitor.sent') }}</th></tr></thead>
          <tbody>
            <tr v-for="r in tableRows" :key="r.at">
              <td>{{ formatClock(r.at) }}</td><td>{{ r.qps === null ? '—' : fmtRate(r.qps) }}</td><td>{{ r.tps === null ? '—' : fmtRate(r.tps) }}</td>
              <td>{{ r.conn }}</td><td>{{ r.running }}</td><td>{{ r.hit === null ? '—' : fmtPct(r.hit) }}</td>
              <td>{{ r.rx === null ? '—' : formatRate(r.rx) }}</td><td>{{ r.tx === null ? '—' : formatRate(r.tx) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <h3 class="h">{{ $t('monitor.keyStatus') }}</h3>
      <div class="keys">
        <div v-for="[k, v] in keyRows" :key="k" class="key-row"><span class="lm-mono k">{{ k }}</span><span class="v">{{ v }}</span></div>
      </div>

      <div class="all">
        <Btn size="sm" variant="ghost" :icon="showAll ? 'chevron-down' : 'chevron-right'" @click="showAll = !showAll">{{ $t('monitor.allStatus') }}</Btn>
        <KvTable v-if="showAll" :data="latest" :placeholder="$t('monitor.searchStatus')" />
      </div>
    </template>
  </div>
</template>

<style scoped>
.status { min-width: 0; }
.bar { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; flex-wrap: wrap; }
.upd { font-size: 12px; transition: opacity .2s; }
.upd.busy { opacity: .6; }
.err { margin: 0 0 12px; padding: 8px 10px; border-radius: var(--radius-m); background: var(--danger-bg); color: var(--danger); font-size: 13px; word-break: break-word; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
.card { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 12px 12px 8px; background: var(--viz-surface); min-width: 0; }
.card-head { display: flex; flex-direction: column; gap: 2px; margin-bottom: 6px; min-width: 0; }
.card-title { font-size: 12.5px; color: var(--text-2); display: inline-flex; align-items: center; gap: 4px; }
.big { font-size: 22px; font-weight: 600; color: var(--text); line-height: 1.3; }
.legend { display: flex; flex-wrap: wrap; gap: 4px 16px; min-height: 29px; align-items: center; }
.lg { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; }
.lg b { font-size: 16px; font-weight: 600; color: var(--text); }
.lg-name { color: var(--text-2); }
.key { width: 12px; height: 2px; border-radius: 1px; display: inline-block; }
.key.s1 { background: var(--viz-1); }
.key.s2 { background: var(--viz-2); }
.scroll-x { overflow-x: auto; border: 1px solid var(--border); border-radius: var(--radius-l); max-height: 360px; overflow-y: auto; }
.nums td { font-variant-numeric: tabular-nums; white-space: nowrap; }
.h { font-size: 13px; font-weight: 600; margin: 20px 0 8px; }
.keys { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); border: 1px solid var(--border); border-radius: var(--radius-l); overflow: hidden; }
.key-row { display: flex; justify-content: space-between; gap: 12px; padding: 6px 12px; border-bottom: 1px solid var(--border); font-size: 13px; min-width: 0; }
.key-row .k { color: var(--text-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
.key-row .v { font-variant-numeric: tabular-nums; white-space: nowrap; }
.all { margin-top: 16px; display: flex; flex-direction: column; gap: 8px; align-items: flex-start; }
.all > :deep(.kv) { align-self: stretch; }
@media (max-width: 760px) { .cards { grid-template-columns: 1fr; } }
</style>
