<script setup lang="ts">
// SQLite 文件状态：文件大小、页数与页大小、空闲页（freelist），以及日志模式等 PRAGMA。
import { computed } from 'vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import { formatBytes } from '@/lib/meta'
import { formatClock, formatInt, num } from './format'
import { useMonitor } from './useMonitor'

const props = defineProps<{ connId: string }>()
const m = useMonitor<Record<string, string>>(() => props.connId, 'status')
m.load()

const s = computed(() => m.data.value || {})
const pageSize = computed(() => num(s.value.page_size))
const pages = computed(() => num(s.value.page_count))
const free = computed(() => num(s.value.freelist_count))
const freePct = computed(() => (pages.value > 0 ? (free.value / pages.value) * 100 : 0))
const vacuumMode: Record<string, string> = { '0': 'NONE', '1': 'FULL', '2': 'INCREMENTAL' }

const rows = computed(() => {
  const v = s.value
  const out: [string, string][] = []
  if (v.journal_mode !== undefined) out.push(['journal_mode', String(v.journal_mode).toUpperCase()])
  if (v.auto_vacuum !== undefined) out.push(['auto_vacuum', vacuumMode[v.auto_vacuum] || v.auto_vacuum])
  if (v.encoding !== undefined) out.push(['encoding', v.encoding])
  if (v.page_size !== undefined) out.push(['page_size', formatInt(v.page_size)])
  if (v.page_count !== undefined) out.push(['page_count', formatInt(v.page_count)])
  if (v.freelist_count !== undefined) out.push(['freelist_count', formatInt(v.freelist_count)])
  if (v.user_version !== undefined) out.push(['user_version', v.user_version])
  if (v.schema_version !== undefined) out.push(['schema_version', v.schema_version])
  return out
})
</script>

<template>
  <div class="sq">
    <div class="bar">
      <span class="lm-spacer" />
      <span v-if="m.at.value" class="lm-faint upd">{{ formatClock(m.at.value) }}</span>
      <IconBtn icon="refresh" :size="16" :label="$t('common.refresh')" :disabled="m.loading.value" @click="m.load()" />
    </div>
    <div v-if="m.error.value" class="err-box" role="alert">
      <p>{{ m.error.value }}</p>
      <Btn size="sm" icon="refresh" @click="m.load()">{{ $t('common.retry') }}</Btn>
    </div>
    <div v-else-if="!m.data.value" class="lm-empty">{{ $t('common.loading') }}</div>
    <template v-else>
      <div class="tiles" :class="{ dim: m.loading.value }">
        <div class="tile">
          <span class="label">{{ $t('monitor.fileSize') }}</span>
          <span class="value">{{ s.file_size !== undefined ? formatBytes(num(s.file_size)) : '—' }}</span>
        </div>
        <div class="tile">
          <span class="label">{{ $t('monitor.pages') }}</span>
          <span class="value">{{ formatInt(pages) }}</span>
          <span class="sub">{{ $t('monitor.pageSizeOf', { size: formatBytes(pageSize) }) }}</span>
        </div>
        <div class="tile">
          <span class="label">{{ $t('monitor.freePages') }}<InfoTip :text="$t('monitor.freelistTip')" /></span>
          <span class="value">{{ formatInt(free) }}</span>
          <span class="sub">{{ formatBytes(free * pageSize) }} · {{ freePct.toFixed(freePct < 10 ? 1 : 0) }}%</span>
          <div class="meter" role="img" :aria-label="freePct.toFixed(1) + '%'"><span :style="{ width: Math.min(100, freePct) + '%' }" /></div>
        </div>
      </div>
      <div class="kv">
        <div v-for="[k, v] in rows" :key="k" class="kv-row"><span class="lm-mono k">{{ k }}</span><span class="v">{{ v }}</span></div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.sq { min-width: 0; max-width: 960px; }
.bar { display: flex; align-items: center; gap: 10px; margin-bottom: 10px; }
.upd { font-size: 12px; font-variant-numeric: tabular-nums; }
.err-box { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 10px 12px; border-radius: var(--radius-m); background: var(--danger-bg); }
.err-box p { margin: 0; color: var(--danger); font-size: 13px; word-break: break-word; }
.tiles { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; margin-bottom: 16px; transition: opacity .15s; }
.dim { opacity: .55; }
.tile { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 12px 14px; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.label { font-size: 12.5px; color: var(--text-2); display: inline-flex; align-items: center; gap: 4px; }
.value { font-size: 22px; font-weight: 600; color: var(--text); line-height: 1.3; }
.sub { font-size: 12px; color: var(--text-3); }
.meter { height: 6px; border-radius: 3px; background: var(--viz-track); overflow: hidden; margin-top: 6px; }
.meter span { display: block; height: 100%; border-radius: 3px; background: var(--viz-1); }
.kv { border: 1px solid var(--border); border-radius: var(--radius-l); overflow: hidden; }
.kv-row { display: flex; justify-content: space-between; gap: 12px; padding: 6px 12px; border-bottom: 1px solid var(--border); font-size: 13px; }
.kv-row:last-child { border-bottom: 0; }
.k { color: var(--text-2); }
.v { font-variant-numeric: tabular-nums; word-break: break-all; text-align: right; }
</style>
