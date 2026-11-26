<script setup lang="ts">
// 工具调用卡片：一行摘要（状态、工具名、参数要点、耗时），展开后看 SQL、结果表格、图表、报告等。
import { computed, ref } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import SqlView from '@/components/code/SqlView.vue'
import ResultTable from '@/components/rich/ResultTable.vue'
import LemiBlock from '@/components/rich/LemiBlock.vue'
import RichText from '@/components/rich/RichText.vue'
import { useConns, openers } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { dialectOf } from '@/lib/dialect'
import { i18n } from '@/i18n'

const props = defineProps<{
  call: { call_id: string; name: string; title?: string; args?: any; argsText?: string }
  result?: { status: string; text?: string; data?: any; duration?: number; is_error?: boolean }
}>()
const t = i18n.global.t
const conns = useConns()
const toast = useToast()

const ICONS: Record<string, string> = {
  list_connections: 'server', list_databases: 'database', list_tables: 'table', describe_tables: 'columns', search_schema: 'search',
  run_query: 'play', read_result: 'table', explain_query: 'explain', execute_sql: 'edit', create_chart: 'chart', create_diagram: 'flowchart',
  er_diagram: 'erd', report_write: 'report', report_read: 'report', workspace_open_table: 'table', workspace_run_sql: 'terminal',
  workspace_update_view: 'filter', workspace_read_view: 'eye', list_backups: 'archive', backup_status: 'archive', health_status: 'pulse',
  task: 'robot', ask_user: 'question', todo_write: 'list-check',
}

const args = computed<any>(() => {
  if (props.call.args && typeof props.call.args === 'object') return props.call.args
  try { return JSON.parse(props.call.argsText || '{}') } catch { return {} }
})
const status = computed(() => props.result?.status || 'pending')
const busy = computed(() => ['input', 'pending', 'running'].includes(status.value))
const rich = computed(() => ['create_chart', 'create_diagram', 'er_diagram', 'report_write'].includes(props.call.name))
const open = ref(false)
const expanded = computed(() => open.value || (rich.value && status.value === 'done'))
const conn = computed(() => conns.byId[args.value?.connection_id])
const dialect = computed(() => dialectOf(conn.value))
const sql = computed(() => (typeof args.value?.sql === 'string' ? args.value.sql : ''))
const data = computed(() => props.result?.data || {})

/** 摘要：参数要点。 */
const brief = computed(() => {
  const a = args.value || {}
  switch (props.call.name) {
    case 'run_query': case 'execute_sql': case 'explain_query': case 'workspace_run_sql':
      return (a.title || sql.value || props.call.argsText || '').replace(/\s+/g, ' ').slice(0, 120)
    case 'describe_tables': return [a.db, (a.tables || []).join(', ')].filter(Boolean).join(' · ')
    case 'list_tables': return [a.db, a.pattern].filter(Boolean).join(' · ')
    case 'search_schema': return a.query || ''
    case 'create_chart': case 'create_diagram': case 'report_write': return a.title || ''
    case 'er_diagram': return a.db || ''
    case 'task': return a.description || ''
    case 'workspace_open_table': return [a.db, a.table].filter(Boolean).join('.')
    case 'read_result': return a.result_id || ''
    default: return ''
  }
})

const statusText = computed(() => {
  switch (status.value) {
    case 'input': return t('agent.tool.writing')
    case 'pending': case 'running': return t('agent.tool.running')
    case 'denied': return t('agent.tool.denied')
    case 'interrupted': return t('agent.tool.interrupted')
    case 'skipped': return t('agent.tool.skipped')
    case 'lost': return t('agent.tool.lost')
    case 'error': return t('agent.tool.failed')
  }
  return props.result?.duration !== undefined ? formatMs(props.result.duration) : ''
})

function formatMs(ms: number) { return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s` }

const taskText = computed(() => (props.result?.text || '').replace(/^<task_result[^>]*>\s*/, '').replace(/\s*<\/task_result>\s*$/, ''))

async function openSql() {
  if (!conns.list.length) await conns.load()
  const c = conns.byId[args.value?.connection_id || data.value?.connection_id]
  if (!c) { toast.error(t('agent.connGone')); return }
  openers.console(useTabs(), c, args.value?.db || data.value?.db || '', { sql: sql.value || data.value?.sql, fromAI: true })
}

async function openTable(table: string) {
  if (!conns.list.length) await conns.load()
  const c = conns.byId[args.value?.connection_id]
  if (!c) return
  openers.table(useTabs(), c, args.value?.db || 'main', table, {}, true)
}

function openReport() {
  const id = data.value?.report_id
  if (!id) return
  useTabs().open('report', { reportId: id }, { title: data.value?.title || t('agent.block.report'), key: 'report:' + id })
}
</script>

<template>
  <div class="tc" :class="['s-' + status, { open: expanded }]">
    <button class="row" @click="open = !expanded">
      <span class="st">
        <span v-if="busy" class="spin" />
        <Icon v-else-if="status === 'done'" name="check" :size="13" />
        <Icon v-else-if="status === 'denied' || status === 'skipped'" name="minus" :size="13" />
        <Icon v-else name="close" :size="13" />
      </span>
      <Icon :name="ICONS[call.name] || 'plug'" :size="14" class="ti" />
      <span class="name">{{ call.title || call.name }}</span>
      <span class="brief lm-ellipsis">{{ brief }}</span>
      <span class="meta">{{ statusText }}</span>
      <Icon :name="expanded ? 'chevron-up' : 'chevron-down'" :size="13" class="chev" />
    </button>
    <div v-if="expanded" class="body">
      <!-- 参数 -->
      <SqlView v-if="sql" :code="sql" :dialect="dialect" :line-numbers="false" max-height="220px" />
      <div v-if="args?.reason && call.name === 'execute_sql'" class="reason">{{ args.reason }}</div>
      <!-- 结果 -->
      <template v-if="result && !busy">
        <template v-if="call.name === 'run_query' && data.columns">
          <ResultTable :columns="data.columns" :rows="data.rows || []" :total="data.row_count" :max-rows="20" max-height="260px" />
          <div class="actions">
            <span class="lm-badge">{{ $t('agent.rowsN', { n: data.row_count }) }}<template v-if="data.truncated">+</template></span>
            <span class="rid lm-mono">{{ data.result_id }}</span>
            <span class="lm-spacer" />
            <button class="link" @click="openSql"><Icon name="terminal" :size="13" />{{ $t('agent.openInWorkspace') }}</button>
          </div>
        </template>
        <LemiBlock v-else-if="call.name === 'create_chart' && data.chart_id" kind="chart" :id="data.chart_id" />
        <LemiBlock v-else-if="(call.name === 'create_diagram' || call.name === 'er_diagram') && data.diagram_id" kind="diagram" :id="data.diagram_id" />
        <div v-else-if="call.name === 'report_write' && data.report_id" class="report">
          <Icon name="report" :size="18" />
          <div class="rinfo"><div class="rtitle">{{ data.title }}</div><div class="rsub">{{ data.created ? $t('agent.reportCreated') : $t('agent.reportUpdated') }}</div></div>
          <button class="btn" @click="openReport">{{ $t('common.open') }}</button>
        </div>
        <div v-else-if="call.name === 'task'" class="task"><RichText :text="taskText" /></div>
        <template v-else>
          <div v-if="call.name === 'describe_tables' && args.tables?.length" class="chips">
            <button v-for="tb in args.tables" :key="tb" class="chip" @click="openTable(tb)"><Icon name="table" :size="12" />{{ tb }}</button>
          </div>
          <pre class="out" :class="{ err: result.is_error || status === 'error' }">{{ result.text }}</pre>
          <div v-if="(call.name === 'execute_sql' || call.name === 'explain_query') && sql" class="actions">
            <span class="lm-spacer" /><button class="link" @click="openSql"><Icon name="terminal" :size="13" />{{ $t('agent.openInWorkspace') }}</button>
          </div>
        </template>
      </template>
      <pre v-else-if="call.argsText && !sql" class="out">{{ call.argsText }}</pre>
    </div>
  </div>
</template>

<style scoped>
.tc { border: 1px solid var(--border); border-radius: var(--radius-m); background: var(--bg); margin: 6px 0; }
.row { width: 100%; display: flex; align-items: center; gap: 7px; height: 30px; padding: 0 8px; border: 0; background: none; text-align: left; font-size: 12.5px; color: var(--text-2); border-radius: var(--radius-m); }
.row:hover { background: var(--bg-hover); }
.st { width: 16px; height: 16px; display: flex; align-items: center; justify-content: center; flex: none; }
.s-done .st { color: var(--success); }
.s-error .st, .s-lost .st { color: var(--danger); }
.s-denied .st, .s-interrupted .st, .s-skipped .st { color: var(--warning); }
.ti { color: var(--text-3); }
.name { font-weight: 500; color: var(--text); flex: none; }
.brief { flex: 1; min-width: 0; color: var(--text-3); font-family: var(--font-mono); font-size: 11.5px; }
.meta { flex: none; font-size: 11.5px; color: var(--text-3); }
.s-error .meta, .s-lost .meta { color: var(--danger); }
.chev { color: var(--text-3); flex: none; }
.body { border-top: 1px solid var(--border); padding: 8px; display: flex; flex-direction: column; gap: 8px; }
.reason { font-size: 12px; color: var(--text-2); }
.out { margin: 0; font-family: var(--font-mono); font-size: 11.5px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; max-height: 260px; overflow: auto; color: var(--text-2); background: var(--bg-soft); border-radius: var(--radius-s); padding: 6px 8px; }
.out.err { color: var(--danger); }
.actions { display: flex; align-items: center; gap: 8px; font-size: 12px; }
.rid { color: var(--text-3); font-size: 11px; }
.link { display: inline-flex; align-items: center; gap: 4px; border: 0; background: none; color: var(--accent); font-size: 12px; padding: 2px 4px; border-radius: var(--radius-s); }
.link:hover { background: var(--accent-soft); }
.report { display: flex; align-items: center; gap: 10px; padding: 4px 2px; color: var(--accent); }
.rinfo { flex: 1; min-width: 0; }
.rtitle { font-weight: 500; color: var(--text); font-size: 13px; }
.rsub { font-size: 11.5px; color: var(--text-3); }
.btn { border: 1px solid var(--border-strong); background: var(--bg); border-radius: var(--radius-m); height: 26px; padding: 0 10px; font-size: 12px; }
.btn:hover { background: var(--bg-hover); }
.task { font-size: 12.5px; }
.chips { display: flex; flex-wrap: wrap; gap: 4px; }
.chip { display: inline-flex; align-items: center; gap: 4px; height: 22px; padding: 0 8px; border-radius: 11px; border: 1px solid var(--border); background: var(--bg-soft); font-size: 11.5px; color: var(--text-2); }
.chip:hover { border-color: var(--accent); color: var(--accent); }
.spin { width: 11px; height: 11px; border: 1.5px solid var(--border-strong); border-top-color: var(--accent); border-radius: 50%; animation: sp .8s linear infinite; }
@keyframes sp { to { transform: rotate(360deg); } }
</style>
