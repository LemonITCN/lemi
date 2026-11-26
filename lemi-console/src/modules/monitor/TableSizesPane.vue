<script setup lang="ts">
// 表大小排行：MySQL 来自 information_schema.TABLES；SQLite 来自 dbstat（按表与索引分别统计）。
import { computed, ref, watch } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import { formatBytes } from '@/lib/meta'
import { formatClock, formatInt, num } from './format'
import { useMonitor } from './useMonitor'

type Row = Record<string, string | number | null>
type SortKey = 'total_bytes' | 'rows_est' | 'data_bytes' | 'index_bytes' | 'free_bytes' | 'name'

const props = defineProps<{ connId: string; mobile: boolean; sqlite: boolean }>()
const limit = ref(50)
const filter = ref('')
const sortKey = ref<SortKey>('total_bytes')
const m = useMonitor<Row[]>(() => props.connId, 'tables', () => ({ limit: limit.value }))
m.load()
watch(limit, () => m.load())

const rows = computed(() => {
  const q = filter.value.trim().toLowerCase()
  const list = (m.data.value || []).filter((r) => !q || `${r.db ?? ''}.${r.name ?? ''}`.toLowerCase().includes(q))
  const k = sortKey.value
  return list.slice().sort((a, b) => (k === 'name' ? String(a.name).localeCompare(String(b.name)) : num(b[k]) - num(a[k])))
})
const maxTotal = computed(() => Math.max(1, ...(m.data.value || []).map((r) => num(r.total_bytes))))
const sumTotal = computed(() => (m.data.value || []).reduce((s, r) => s + num(r.total_bytes), 0))

function pct(r: Row) { return Math.max(0.5, (num(r.total_bytes) / maxTotal.value) * 100) }
function fragPct(r: Row) {
  const total = num(r.data_bytes) + num(r.index_bytes) + num(r.free_bytes)
  return total > 0 ? (num(r.free_bytes) / total) * 100 : 0
}

// 可排序的数值列
const numCols = computed<{ key: SortKey; label: string }[]>(() => props.sqlite
  ? [{ key: 'total_bytes', label: 'monitor.size' }]
  : [
    { key: 'rows_est', label: 'monitor.rowsEst' }, { key: 'data_bytes', label: 'monitor.data' }, { key: 'index_bytes', label: 'monitor.index' },
    { key: 'free_bytes', label: 'monitor.free' }, { key: 'total_bytes', label: 'monitor.total' },
  ])
</script>

<template>
  <div class="sizes">
    <div class="bar">
      <div class="search"><Icon name="search" :size="14" /><input v-model="filter" :placeholder="$t('monitor.filterTables')" spellcheck="false" /></div>
      <label class="lim"><span class="lm-faint">{{ $t('monitor.top') }}</span>
        <select v-model.number="limit" class="sel"><option :value="50">50</option><option :value="200">200</option><option :value="1000">1000</option></select></label>
      <span class="lm-spacer" />
      <span v-if="m.at.value" class="lm-faint upd">{{ formatClock(m.at.value) }}</span>
      <IconBtn icon="refresh" :size="16" :label="$t('common.refresh')" :disabled="m.loading.value" @click="m.load()" />
    </div>

    <div v-if="m.error.value" class="err-box" role="alert">
      <p>{{ m.error.value }}</p>
      <Btn size="sm" icon="refresh" @click="m.load()">{{ $t('common.retry') }}</Btn>
    </div>
    <div v-else-if="!m.data.value" class="lm-empty">{{ $t('common.loading') }}</div>
    <div v-else-if="!rows.length" class="lm-empty">{{ (m.data.value || []).length ? $t('monitor.noMatch') : $t('monitor.noTables') }}</div>
    <template v-else>
      <div class="sum lm-faint">{{ $t('monitor.sizeSum', { n: (m.data.value || []).length, size: formatBytes(sumTotal) }) }}</div>
      <div v-if="!mobile" class="scroll-x" :class="{ dim: m.loading.value }">
        <table class="lm-table grid" :class="{ lite: sqlite }">
          <thead>
            <tr>
              <th v-if="!sqlite" class="w-db">{{ $t('monitor.db') }}</th>
              <th :class="{ sorted: sortKey === 'name' }">
                <button type="button" class="th-btn" @click="sortKey = 'name'">{{ $t(sqlite ? 'monitor.object' : 'monitor.table') }}<Icon v-if="sortKey === 'name'" name="arrow-down" :size="12" /></button>
              </th>
              <th v-if="!sqlite" class="w-eng">{{ $t('monitor.engine') }}</th>
              <th v-for="c in numCols" :key="c.key" class="r" :class="{ sorted: sortKey === c.key }">
                <button type="button" class="th-btn" @click="sortKey = c.key">{{ $t(c.label) }}<Icon v-if="sortKey === c.key" name="arrow-down" :size="12" /></button>
              </th>
              <th class="w-bar" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="`${r.db}.${r.name}`">
              <td v-if="!sqlite" class="ell" :title="String(r.db ?? '')">{{ r.db }}</td>
              <td class="ell lm-mono" :title="String(r.name ?? '')">{{ r.name }}</td>
              <td v-if="!sqlite" class="ell lm-faint">{{ r.engine || '—' }}</td>
              <template v-if="!sqlite">
                <td class="r num">{{ formatInt(r.rows_est) }}</td>
                <td class="r num">{{ formatBytes(num(r.data_bytes)) }}</td>
                <td class="r num">{{ formatBytes(num(r.index_bytes)) }}</td>
                <td class="r num" :class="{ warn: fragPct(r) >= 20 && num(r.free_bytes) > 64 * 1024 * 1024 }" :title="fragPct(r).toFixed(1) + '%'">{{ formatBytes(num(r.free_bytes)) }}</td>
              </template>
              <td class="r num strong">{{ formatBytes(num(r.total_bytes)) }}</td>
              <td class="w-bar"><div class="meter" role="img" :aria-label="`${pct(r).toFixed(0)}%`"><span :style="{ width: pct(r) + '%' }" /></div></td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="cards">
        <div v-for="r in rows" :key="`${r.db}.${r.name}`" class="card">
          <div class="c-head"><span class="lm-mono ell name">{{ r.name }}</span><b class="num">{{ formatBytes(num(r.total_bytes)) }}</b></div>
          <div class="meter"><span :style="{ width: pct(r) + '%' }" /></div>
          <div v-if="!sqlite" class="c-meta lm-faint">
            {{ r.db }} · {{ r.engine || '—' }} · {{ $t('monitor.rowsEst') }} {{ formatInt(r.rows_est) }}<br />
            {{ $t('monitor.data') }} {{ formatBytes(num(r.data_bytes)) }} · {{ $t('monitor.index') }} {{ formatBytes(num(r.index_bytes)) }} · {{ $t('monitor.free') }} {{ formatBytes(num(r.free_bytes)) }}
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.sizes { min-width: 0; }
.bar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.search { display: flex; align-items: center; gap: 6px; height: 30px; padding: 0 8px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); color: var(--text-3); flex: 0 1 280px; min-width: 0; }
.search:focus-within { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.search input { flex: 1; min-width: 0; border: 0; outline: none; background: transparent; font-size: 13px; color: var(--text); }
.lim { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; }
.sel { height: 28px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); background: var(--bg); padding: 0 4px; font-size: 12.5px; color: var(--text); }
.upd { font-size: 12px; font-variant-numeric: tabular-nums; }
.err-box { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 10px 12px; border-radius: var(--radius-m); background: var(--danger-bg); }
.err-box p { margin: 0; color: var(--danger); font-size: 13px; word-break: break-word; }
.sum { font-size: 12px; margin-bottom: 6px; }
.scroll-x { overflow-x: auto; border: 1px solid var(--border); border-radius: var(--radius-l); transition: opacity .15s; }
.dim { opacity: .55; }
.grid { table-layout: fixed; min-width: 900px; }
.grid.lite { min-width: 480px; }
.grid td { padding: 5px 10px; }
.w-db { width: 14%; }
.w-eng { width: 80px; }
.w-bar { width: 18%; }
.r { text-align: right !important; }
.th-btn { border: 0; background: none; padding: 0; color: inherit; font: inherit; display: inline-flex; align-items: center; gap: 2px; }
th.sorted .th-btn { color: var(--text); }
.num { font-variant-numeric: tabular-nums; white-space: nowrap; }
.strong { color: var(--text); font-weight: 500; }
.warn { color: var(--warning); }
.ell { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.meter { height: 6px; border-radius: 3px; background: var(--viz-track); overflow: hidden; }
.meter span { display: block; height: 100%; border-radius: 3px; background: var(--viz-1); }
.cards { display: flex; flex-direction: column; gap: 8px; }
.card { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 8px 10px; min-width: 0; display: flex; flex-direction: column; gap: 6px; }
.c-head { display: flex; align-items: baseline; gap: 8px; min-width: 0; }
.c-head .name { flex: 1; min-width: 0; }
.c-meta { font-size: 12px; line-height: 1.5; word-break: break-word; }
</style>
