<script setup lang="ts">
// 结果表格（只读、轻量）：智能体回复、工具卡片与报告里的表格引用用。大结果在主工作区打开再细看。
import { computed } from 'vue'
import { cellText } from './markdown'

const props = withDefaults(defineProps<{ columns: { name: string; kind?: string }[]; rows: any[][]; total?: number; maxRows?: number; maxHeight?: string }>(), { maxRows: 200, maxHeight: '320px' })
const shown = computed(() => props.rows.slice(0, props.maxRows))
const numeric = computed(() => props.columns.map((c) => ['int', 'decimal', 'float', 'year'].includes(c.kind || '')))
const more = computed(() => Math.max(0, (props.total ?? props.rows.length) - shown.value.length))
</script>

<template>
  <div class="rt">
    <div class="scroll" :style="{ maxHeight }">
      <table>
        <thead><tr><th class="n">#</th><th v-for="c in columns" :key="c.name">{{ c.name }}</th></tr></thead>
        <tbody>
          <tr v-for="(r, i) in shown" :key="i">
            <td class="n">{{ i + 1 }}</td>
            <td v-for="(v, j) in r" :key="j" :class="{ num: numeric[j], nil: v === null || v === undefined }" :title="cellText(v).length > 40 ? cellText(v) : undefined">{{ cellText(v) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-if="more" class="more">{{ $t('agent.moreRows', { n: more }) }}</div>
  </div>
</template>

<style scoped>
.rt { border: 1px solid var(--border); border-radius: var(--radius-m); overflow: hidden; background: var(--bg); }
.scroll { overflow: auto; }
table { border-collapse: collapse; font-size: 12px; font-family: var(--font-mono); min-width: 100%; }
th { position: sticky; top: 0; background: var(--bg-soft); font-weight: 500; color: var(--text-2); text-align: left; white-space: nowrap; padding: 4px 8px; border-bottom: 1px solid var(--border); font-family: var(--font-ui); }
td { padding: 3px 8px; border-bottom: 1px solid var(--border); white-space: nowrap; max-width: 280px; overflow: hidden; text-overflow: ellipsis; }
tr:last-child td { border-bottom: 0; }
.n { color: var(--text-3); text-align: right; width: 1%; }
.num { text-align: right; }
.nil { color: var(--text-3); font-style: italic; }
.more { font-size: 11.5px; color: var(--text-3); padding: 4px 8px; border-top: 1px solid var(--border); background: var(--bg-soft); }
</style>
