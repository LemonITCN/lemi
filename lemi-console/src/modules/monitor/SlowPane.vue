<script setup lang="ts">
// 慢查询：performance_schema 按语句摘要汇总（按总耗时降序），语句可展开、复制或在控制台打开。
import { computed, ref, watch } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import { useConns, openers } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { copyText } from '@/lib/cm'
import { i18n } from '@/i18n'
import { formatClock, formatInt, formatMs } from './format'
import { useMonitor } from './useMonitor'

type Row = Record<string, string | number | null>

const props = defineProps<{ connId: string; mobile: boolean }>()
const conns = useConns()
const toast = useToast()
const t = i18n.global.t
const limit = ref(50)
const filter = ref('')
const expanded = ref<Record<number, boolean>>({})
const m = useMonitor<Row[]>(() => props.connId, 'slow', () => ({ limit: limit.value }))
m.load()
watch(limit, () => m.load())

const rows = computed(() => {
  const q = filter.value.trim().toLowerCase()
  return (m.data.value || []).map((r, i) => ({ r, i })).filter(({ r }) => !q || `${r.db ?? ''} ${r.query ?? ''}`.toLowerCase().includes(q))
})

function lastSeen(v: unknown) {
  const s = String(v ?? '')
  if (!s) return '—'
  // performance_schema 返回 2026-11-26 10:20:30.123456，只保留到秒
  return s.replace('T', ' ').replace(/\.\d+$/, '')
}

function menu(r: Row): MenuItem[] {
  const sql = String(r.query || '')
  return [
    { label: t('common.copy'), icon: 'copy', run: async () => { if (await copyText(sql)) toast.success(t('common.copied')) } },
    { label: t('monitor.openConsole'), icon: 'terminal', run: () => {
      const c = conns.byId[props.connId]
      if (c) openers.console(useTabs(), c, String(r.db || ''), { sql })
    } },
  ]
}
</script>

<template>
  <div class="slow">
    <div class="bar">
      <div class="search"><Icon name="search" :size="14" /><input v-model="filter" :placeholder="$t('monitor.filterSlow')" spellcheck="false" /></div>
      <label class="lim"><span class="lm-faint">{{ $t('monitor.top') }}</span>
        <select v-model.number="limit" class="sel"><option :value="50">50</option><option :value="200">200</option><option :value="500">500</option></select></label>
      <InfoTip :text="$t('monitor.slowTip')" :width="320" />
      <span class="lm-spacer" />
      <span v-if="m.at.value" class="lm-faint upd">{{ formatClock(m.at.value) }}</span>
      <IconBtn icon="refresh" :size="16" :label="$t('common.refresh')" :disabled="m.loading.value" @click="m.load()" />
    </div>

    <div v-if="m.error.value" class="err-box" role="alert">
      <p>{{ m.error.value }}</p>
      <Btn size="sm" icon="refresh" @click="m.load()">{{ $t('common.retry') }}</Btn>
    </div>
    <div v-else-if="!m.data.value" class="lm-empty">{{ $t('common.loading') }}</div>
    <div v-else-if="!rows.length" class="lm-empty">{{ (m.data.value || []).length ? $t('monitor.noMatch') : $t('monitor.noSlow') }}</div>
    <template v-else>
      <div v-if="!mobile" class="scroll-x" :class="{ dim: m.loading.value }">
        <table class="lm-table grid">
          <thead>
            <tr>
              <th class="w-db">{{ $t('monitor.db') }}</th><th>{{ $t('monitor.statement') }}</th>
              <th class="r w-n">{{ $t('monitor.calls') }}</th><th class="r w-n">{{ $t('monitor.totalTime') }}</th><th class="r w-n">{{ $t('monitor.avgTime') }}</th>
              <th class="r w-n">{{ $t('monitor.maxTime') }}</th><th class="r w-n">{{ $t('monitor.rowsExamined') }}</th><th class="r w-n">{{ $t('monitor.rowsSent') }}</th>
              <th class="w-last">{{ $t('monitor.lastSeen') }}</th><th class="w-act" />
            </tr>
          </thead>
          <tbody>
            <tr v-for="{ r, i } in rows" :key="i">
              <td class="ell" :title="String(r.db ?? '')">{{ r.db || '—' }}</td>
              <td class="sql"><pre class="lm-mono q" :class="{ clamp: !expanded[i] }" :title="expanded[i] ? '' : $t('monitor.expand')" @click="expanded[i] = !expanded[i]">{{ r.query }}</pre></td>
              <td class="r num">{{ formatInt(r.calls) }}</td>
              <td class="r num strong">{{ formatMs(r.total_ms) }}</td>
              <td class="r num">{{ formatMs(r.avg_ms) }}</td>
              <td class="r num">{{ formatMs(r.max_ms) }}</td>
              <td class="r num">{{ formatInt(r.rows_examined) }}</td>
              <td class="r num">{{ formatInt(r.rows_sent) }}</td>
              <td class="num lm-faint last">{{ lastSeen(r.last_seen) }}</td>
              <td class="w-act"><Menu :items="menu(r)" align="end"><IconBtn icon="more" :size="15" :label="$t('common.more')" /></Menu></td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="cards">
        <div v-for="{ r, i } in rows" :key="i" class="card">
          <div class="c-head">
            <span class="ell lm-faint">{{ r.db || '—' }}</span>
            <span class="lm-spacer" />
            <b class="num">{{ formatMs(r.total_ms) }}</b>
            <Menu :items="menu(r)" align="end"><IconBtn icon="more" :size="16" :label="$t('common.more')" /></Menu>
          </div>
          <pre class="lm-mono q box" :class="{ clamp: !expanded[i] }" @click="expanded[i] = !expanded[i]">{{ r.query }}</pre>
          <div class="c-meta lm-faint">
            {{ $t('monitor.calls') }} {{ formatInt(r.calls) }} · {{ $t('monitor.avgTime') }} {{ formatMs(r.avg_ms) }} · {{ $t('monitor.maxTime') }} {{ formatMs(r.max_ms) }}<br />
            {{ $t('monitor.rowsExamined') }} {{ formatInt(r.rows_examined) }} · {{ $t('monitor.rowsSent') }} {{ formatInt(r.rows_sent) }} · {{ lastSeen(r.last_seen) }}
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.slow { min-width: 0; }
.bar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; flex-wrap: wrap; }
.search { display: flex; align-items: center; gap: 6px; height: 30px; padding: 0 8px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); color: var(--text-3); flex: 0 1 280px; min-width: 0; }
.search:focus-within { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.search input { flex: 1; min-width: 0; border: 0; outline: none; background: transparent; font-size: 13px; color: var(--text); }
.lim { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; }
.sel { height: 28px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); background: var(--bg); padding: 0 4px; font-size: 12.5px; color: var(--text); }
.upd { font-size: 12px; font-variant-numeric: tabular-nums; }
.err-box { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 10px 12px; border-radius: var(--radius-m); background: var(--danger-bg); }
.err-box p { margin: 0; color: var(--danger); font-size: 13px; word-break: break-word; white-space: pre-wrap; }
.scroll-x { overflow-x: auto; border: 1px solid var(--border); border-radius: var(--radius-l); transition: opacity .15s; }
.dim { opacity: .55; }
.grid { table-layout: fixed; min-width: 1100px; }
.grid td { padding: 5px 10px; vertical-align: top; }
.w-db { width: 110px; }
.w-n { width: 84px; }
.w-last { width: 150px; }
.w-act { width: 44px; text-align: right; }
.r { text-align: right !important; }
.num { font-variant-numeric: tabular-nums; white-space: nowrap; }
.strong { color: var(--text); font-weight: 500; }
.last { font-size: 12px; }
.ell { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.q { margin: 0; white-space: pre-wrap; word-break: break-all; cursor: pointer; }
.q.clamp { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.q.box { background: var(--bg-sunken); border-radius: var(--radius-s); padding: 6px 8px; }
.q.box.clamp { -webkit-line-clamp: 4; }
.cards { display: flex; flex-direction: column; gap: 8px; }
.card { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 8px 10px; min-width: 0; display: flex; flex-direction: column; gap: 6px; }
.c-head { display: flex; align-items: center; gap: 8px; min-width: 0; }
.c-meta { font-size: 12px; line-height: 1.5; word-break: break-word; }
</style>
