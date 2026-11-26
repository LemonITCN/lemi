<script setup lang="ts">
// 引用块：```lemi-chart <id>```、```lemi-table <结果ID>```、```lemi-diagram <id>```。
// 报告里用保存时的快照；聊天里按 ID 现取（图表可「重新执行刷新」）。
import { computed, onMounted, ref } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import ChartView from './ChartView.vue'
import MermaidView from './MermaidView.vue'
import ResultTable from './ResultTable.vue'
import { api } from '@/api/client'
import { errorText, i18n } from '@/i18n'
import { useToast } from '@/stores/toast'
import { useConns, openers } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'

export interface Snapshot {
  charts?: Record<string, any>
  tables?: Record<string, any>
  diagrams?: Record<string, any>
}

const props = defineProps<{ kind: 'chart' | 'table' | 'diagram'; id: string; caption?: string; snapshot?: Snapshot | null; publicView?: boolean }>()
const toast = useToast()
const t = i18n.global.t
const data = ref<any>(null)
const error = ref('')
const loading = ref(false)
const refreshing = ref(false)
const chartRef = ref<InstanceType<typeof ChartView>>()
const mmRef = ref<InstanceType<typeof MermaidView>>()

const live = computed(() => !props.snapshot)
const title = computed(() => data.value?.title || props.caption || (props.kind === 'table' ? t('agent.block.table') : props.kind === 'chart' ? t('agent.block.chart') : t('agent.block.diagram')))
const icon = computed(() => (props.kind === 'table' ? 'table' : props.kind === 'chart' ? 'chart' : 'flowchart'))

async function load() {
  error.value = ''
  if (props.snapshot) {
    const src = props.kind === 'chart' ? props.snapshot.charts : props.kind === 'table' ? props.snapshot.tables : props.snapshot.diagrams
    data.value = src?.[props.id] || null
    if (!data.value) error.value = t('agent.block.missing')
    return
  }
  loading.value = true
  try {
    if (props.kind === 'table') {
      const r = await api.get(`/agent/results/${props.id}?limit=500`)
      data.value = { title: r.result?.title, sql: r.result?.sql, connection_id: r.result?.connection_id, db: r.result?.db, columns: r.columns, rows: r.rows, total: r.total, truncated: r.result?.truncated }
    } else {
      data.value = (await api.get(`/charts/${props.id}`)).chart
    }
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
}

async function refresh() {
  refreshing.value = true
  try {
    data.value = (await api.post(`/charts/${props.id}/refresh`)).chart
    toast.success(t('agent.block.refreshed'))
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    refreshing.value = false
  }
}

function download() {
  const a = document.createElement('a')
  if (props.kind === 'chart') {
    a.href = chartRef.value?.dataURL() || ''
    a.download = (title.value || 'chart') + '.png'
  } else {
    const svg = mmRef.value?.svgText() || ''
    a.href = URL.createObjectURL(new Blob([svg], { type: 'image/svg+xml' }))
    a.download = (title.value || 'diagram') + '.svg'
  }
  if (!a.href) return
  a.click()
}

async function openInWorkspace() {
  const conns = useConns()
  if (!conns.list.length) await conns.load()
  const c = conns.byId[data.value?.connection_id]
  if (!c) { toast.error(t('agent.connGone')); return }
  openers.console(useTabs(), c, data.value.db || '', { sql: data.value.sql, fromAI: true })
}

onMounted(load)
</script>

<template>
  <div class="lb">
    <div class="head">
      <Icon :name="icon" :size="14" />
      <span class="title lm-ellipsis">{{ title }}</span>
      <span v-if="kind === 'table' && data" class="lm-badge">{{ $t('agent.rowsN', { n: data.total ?? data.rows?.length ?? 0 }) }}</span>
      <span class="lm-spacer" />
      <IconBtn v-if="kind === 'chart' && live && data?.refreshable" icon="refresh" :size="14" :label="$t('agent.block.refresh')" :disabled="refreshing" @click="refresh" />
      <IconBtn v-if="kind === 'table' && live && data?.sql && !publicView" icon="terminal" :size="14" :label="$t('agent.openInWorkspace')" @click="openInWorkspace" />
      <IconBtn v-if="kind !== 'table' && data" icon="download" :size="14" :label="$t('common.download')" @click="download" />
    </div>
    <div class="body">
      <div v-if="loading" class="state">{{ $t('common.loading') }}</div>
      <div v-else-if="error" class="state err">{{ error }}</div>
      <template v-else-if="data">
        <ChartView v-if="kind === 'chart'" ref="chartRef" :option="data.option" :gl="data.gl" />
        <MermaidView v-else-if="kind === 'diagram'" ref="mmRef" :code="data.code" />
        <ResultTable v-else :columns="data.columns || []" :rows="data.rows || []" :total="data.total" />
      </template>
      <div v-if="caption && kind !== 'table'" class="cap">{{ caption }}</div>
    </div>
  </div>
</template>

<style scoped>
.lb { border: 1px solid var(--border); border-radius: var(--radius-l); background: var(--bg); margin: 10px 0; overflow: hidden; }
.head { display: flex; align-items: center; gap: 6px; height: 32px; padding: 0 6px 0 10px; border-bottom: 1px solid var(--border); background: var(--bg-soft); color: var(--text-2); font-size: 12.5px; }
.title { font-weight: 500; color: var(--text); min-width: 0; }
.body { padding: 8px; }
.state { font-size: 12.5px; color: var(--text-3); padding: 16px; text-align: center; }
.state.err { color: var(--danger); }
.cap { font-size: 12px; color: var(--text-2); padding: 4px 2px 0; }
</style>
