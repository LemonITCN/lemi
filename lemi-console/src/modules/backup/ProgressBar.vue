<script setup lang="ts">
// 运行中的任务进度：阶段、已处理字节、行数、当前表、速度；能算出百分比时显示进度条，否则显示流动条。
import { computed } from 'vue'
import { formatBytes, percent, type Progress } from './api'
import { i18n } from '@/i18n'

const props = defineProps<{ p: Progress; compact?: boolean }>()
const t = i18n.global.t
const pct = computed(() => percent(props.p))
const phase = computed(() => {
  const k = props.p.phase === 'running' && props.p.kind !== 'backup' ? 'running_' + props.p.kind : props.p.phase
  return i18n.global.te('backup.phase.' + k) ? t('backup.phase.' + k) : props.p.phase
})
const detail = computed(() => {
  const p = props.p
  const parts: string[] = []
  const bytes = p.kind === 'backup' ? p.bytes : p.written || p.bytes
  if (bytes) parts.push(formatBytes(bytes))
  if (p.rows) parts.push(t('backup.run.rowsN', { n: p.rows.toLocaleString() }))
  if (p.statements) parts.push(t('backup.restore.statements', { n: p.statements.toLocaleString() }))
  if (p.speed) parts.push(t('backup.run.speed', { v: formatBytes(p.speed) }))
  return parts.join(' · ')
})
</script>

<template>
  <div class="pb" :class="{ compact }">
    <div class="bar"><span :class="{ indet: pct === null }" :style="pct === null ? {} : { width: pct + '%' }" /></div>
    <div class="txt">
      <span class="ph">{{ phase }}<template v-if="pct !== null"> {{ pct }}%</template></span>
      <span v-if="detail" class="lm-faint">{{ detail }}</span>
      <span v-if="!compact && p.table" class="lm-faint lm-ellipsis tb">{{ $t('backup.run.tableAt', { t: (p.database ? p.database + '.' : '') + p.table }) }}</span>
    </div>
  </div>
</template>

<style scoped>
.pb { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.bar { height: 4px; border-radius: 2px; background: var(--bg-active); overflow: hidden; position: relative; }
.bar span { display: block; height: 100%; background: var(--accent); border-radius: 2px; transition: width .4s var(--ease); }
.bar span.indet { width: 30%; position: absolute; animation: slide 1.2s ease-in-out infinite; }
@keyframes slide { from { left: -30%; } to { left: 100%; } }
.txt { display: flex; flex-wrap: wrap; gap: 2px 10px; font-size: 11.5px; min-width: 0; }
.ph { color: var(--accent); font-weight: 500; }
.tb { max-width: 100%; }
.compact .txt { flex-wrap: nowrap; overflow: hidden; }
</style>
