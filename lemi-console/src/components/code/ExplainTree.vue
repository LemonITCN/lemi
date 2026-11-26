<script setup lang="ts">
// 执行计划树：接 /sessions/{sid}/explain 的返回 {format: 'json'|'table', result}。MySQL 的 JSON 计划与 SQLite 的 QUERY PLAN 都画成树，
// 全表扫描、临时表、文件排序标出来；也可以切回原始内容。
import { computed, ref } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import Segmented from '@/components/ui/Segmented.vue'
import { mysqlPlan, sqlitePlan, type PlanNode } from '@/lib/explain'
import { i18n } from '@/i18n'

const props = defineProps<{ format: string; result: { columns?: { name: string }[]; rows?: any[][] } }>()
const t = i18n.global.t
const mode = ref<'tree' | 'raw'>('tree')

const raw = computed(() => {
  const rows = props.result?.rows || []
  if (props.format === 'json') {
    const s = String(rows[0]?.[0] ?? '')
    try { return JSON.stringify(JSON.parse(s), null, 2) } catch { return s }
  }
  return rows.map((r) => r.map((x) => (x === null ? 'NULL' : String(x))).join('  ')).join('\n')
})

const roots = computed<PlanNode[]>(() => {
  const rows = props.result?.rows || []
  if (props.format === 'json') {
    const p = mysqlPlan(String(rows[0]?.[0] ?? ''))
    return p ? [p] : []
  }
  return sqlitePlan(rows as any)
})

interface Line { node: PlanNode; depth: number; last: boolean[] }
const lines = computed<Line[]>(() => {
  const out: Line[] = []
  const walk = (ns: PlanNode[], depth: number, last: boolean[]) => ns.forEach((n, i) => {
    const l = [...last, i === ns.length - 1]
    out.push({ node: n, depth, last: l })
    walk(n.children, depth + 1, l)
  })
  walk(roots.value, 0, [])
  return out
})

const warnText = (w: string) => t('explain.warn.' + w)
const statName = (k: string) => (i18n.global.te('explain.stat.' + k) ? t('explain.stat.' + k) : k)
</script>

<template>
  <div class="ex">
    <div class="ex-bar">
      <Segmented v-model="mode" :options="[{ value: 'tree', label: $t('explain.tree') }, { value: 'raw', label: $t('explain.raw') }]" />
    </div>
    <div v-if="mode === 'tree' && lines.length" class="ex-tree">
      <div v-for="(l, i) in lines" :key="i" class="ex-row" :style="{ paddingLeft: 12 + l.depth * 18 + 'px' }">
        <span v-if="l.depth" class="ex-elbow" :style="{ left: l.depth * 18 - 6 + 'px' }" />
        <div class="ex-main">
          <Icon :name="l.node.kind === 'table' ? 'table' : l.node.kind === 'query_block' ? 'code' : l.node.kind === 'step' ? 'chevron-right' : 'explain'" :size="14" class="ex-ic" />
          <span class="ex-label" :class="{ tbl: l.node.kind === 'table', mono: l.node.kind === 'step' }">{{ l.node.label }}</span>
          <span v-if="l.node.access" class="lm-badge" :class="{ danger: l.node.access === 'ALL', warning: l.node.access === 'index' }">{{ l.node.access }}</span>
          <span v-for="w in l.node.warn || []" :key="w" class="lm-badge danger">{{ warnText(w) }}</span>
          <span v-for="[k, v] in l.node.stats" :key="k" class="ex-stat"><span class="k">{{ statName(k) }}</span> {{ v }}</span>
        </div>
        <div v-if="l.node.condition" class="ex-cond">{{ l.node.condition }}</div>
      </div>
    </div>
    <pre v-else class="ex-raw">{{ raw }}</pre>
  </div>
</template>

<style scoped>
.ex { display: flex; flex-direction: column; min-height: 0; height: 100%; }
.ex-bar { display: flex; padding: 8px 12px; flex: none; }
.ex-tree { flex: 1; overflow: auto; padding: 4px 0 16px; font-size: 13px; }
.ex-row { position: relative; padding-top: 5px; padding-bottom: 5px; padding-right: 12px; }
.ex-row:hover { background: var(--bg-hover); }
.ex-elbow { position: absolute; top: 0; height: 15px; width: 10px; border-left: 1px solid var(--border-strong); border-bottom: 1px solid var(--border-strong); border-bottom-left-radius: 4px; }
.ex-main { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; }
.ex-ic { color: var(--text-3); }
.ex-label { font-weight: 600; }
.ex-label.tbl { font-family: var(--font-mono); }
.ex-label.mono { font-family: var(--font-mono); font-weight: 500; font-size: 12.5px; }
.ex-stat { font-size: 12px; color: var(--text-2); font-family: var(--font-mono); white-space: nowrap; }
.ex-stat .k { color: var(--text-3); font-family: var(--font-ui); }
.ex-cond { margin: 3px 0 0 22px; font-family: var(--font-mono); font-size: 12px; color: var(--text-2); word-break: break-word; }
.ex-raw { flex: 1; overflow: auto; margin: 0; padding: 8px 12px 16px; font-family: var(--font-mono); font-size: 12px; white-space: pre; color: var(--text); }
</style>
