<script setup lang="ts">
// 运行记录：左边列表，右边是所选那次运行的完整对话（工具调用、图表、报告与对话面板里看到的一致）。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import Btn from '@/components/ui/Btn.vue'
import RichText from '@/components/rich/RichText.vue'
import MessageList from '@/modules/agent/MessageList.vue'
import { toItems, type Message } from '@/modules/agent/types'
import { openReport } from '@/modules/reports'
import { api } from '@/api/client'
import { formatDate, formatRelative } from '@/lib/time'
import { errorText } from '@/i18n'

const props = defineProps<{ task: any }>()
const emit = defineEmits<{ changed: [] }>()
const route = useRoute()
const router = useRouter()
const runs = ref<any[]>([])
const loaded = ref(false)
const messages = ref<Message[]>([])
const trError = ref('')
const trLoading = ref(false)
const selected = computed(() => String(route.query.run || '') || runs.value[0]?.id || '')
const run = computed(() => runs.value.find((r) => r.id === selected.value))
const items = computed(() => toItems(messages.value))

async function load() {
  try {
    runs.value = (await api.get(`/agent-tasks/${props.task.id}/runs?limit=100`)).runs || []
  } catch { runs.value = [] } finally { loaded.value = true }
}

async function loadTranscript() {
  const id = selected.value
  messages.value = []
  trError.value = ''
  if (!id || !run.value?.conversation_id) return
  trLoading.value = true
  try {
    const r = await api.get(`/agent-tasks/${props.task.id}/runs/${id}/transcript`)
    if (id === selected.value) messages.value = r.messages || []
  } catch (e) { trError.value = errorText(e) } finally { trLoading.value = false }
}

function pick(id: string) { router.replace({ query: { ...route.query, run: id, view: 'runs' } }) }

onMounted(load)
watch(() => props.task.id, () => { loaded.value = false; load() })
watch([selected, () => run.value?.conversation_id], loadTranscript, { immediate: true })

// 有运行中的记录时定时刷新
let timer: number | undefined
const anyRunning = computed(() => props.task.running || runs.value.some((r) => r.status === 'running'))
watch(anyRunning, (on) => {
  window.clearInterval(timer)
  if (!on) return
  timer = window.setInterval(async () => {
    const was = run.value?.status
    await load()
    if (run.value?.status === 'running' || was === 'running') loadTranscript()
    if (!runs.value.some((r) => r.status === 'running')) emit('changed')
  }, 3000)
}, { immediate: true })
onBeforeUnmount(() => window.clearInterval(timer))
defineExpose({ load })

function dur(r: any) {
  if (!r.finished_at) return ''
  const s = Math.max(0, Math.round((r.finished_at - r.started_at) / 1000))
  return s < 60 ? s + 's' : Math.floor(s / 60) + 'm ' + (s % 60) + 's'
}
function goReport(id: string) {
  openReport(id)
  router.push('/')
}
</script>

<template>
  <div class="runs">
    <div class="list">
      <button v-for="r in runs" :key="r.id" class="it" :class="{ on: r.id === selected }" @click="pick(r.id)">
        <span class="dot" :class="r.status" />
        <span class="main">
          <span class="when">{{ formatDate(r.started_at, true) }}</span>
          <span class="sub">{{ $t('tasks.trigger.' + r.trigger) }} · {{ $t('tasks.status.' + r.status) }}<template v-if="dur(r)"> · {{ dur(r) }}</template></span>
        </span>
      </button>
      <div v-if="loaded && !runs.length" class="lm-empty">{{ $t('tasks.noRuns') }}</div>
    </div>
    <div class="detail">
      <template v-if="run">
        <div class="head">
          <span class="lm-badge" :class="{ success: run.status === 'done', danger: run.status === 'error', warning: run.status === 'stopped', accent: run.status === 'running' }">{{ $t('tasks.status.' + run.status) }}</span>
          <span class="lm-muted">{{ formatRelative(run.started_at) }}</span>
          <span class="lm-spacer" />
          <Btn v-if="run.report_id" size="sm" variant="ghost" icon="report" @click="goReport(run.report_id)">{{ $t('tasks.openReport') }}</Btn>
        </div>
        <div v-if="run.error" class="error"><Icon name="warning" :size="14" />{{ run.error }}</div>
        <div v-if="run.summary && run.status !== 'running'" class="summary">
          <div class="cap">{{ $t('tasks.summary') }}</div>
          <RichText :text="run.summary" />
        </div>
        <div class="cap tr-cap">{{ $t('tasks.transcript') }}</div>
        <div v-if="trError" class="lm-empty">{{ trError }}</div>
        <div v-else-if="trLoading && !messages.length" class="lm-empty">{{ $t('common.loading') }}</div>
        <div v-else-if="!run.conversation_id" class="lm-empty">{{ $t('tasks.noTranscript') }}</div>
        <div v-else class="transcript"><MessageList :items="items" /></div>
      </template>
      <div v-else-if="loaded" class="lm-empty">{{ $t('tasks.pickRun') }}</div>
    </div>
  </div>
</template>

<style scoped>
.runs { display: flex; gap: 16px; min-height: 0; align-items: flex-start; }
.list { width: 240px; flex: none; border: 1px solid var(--border); border-radius: var(--radius-l); padding: 4px; max-height: calc(100vh - 220px); overflow: auto; position: sticky; top: 0; }
.it { width: 100%; display: flex; align-items: center; gap: 10px; padding: 6px 8px; border: 0; background: none; border-radius: var(--radius-m); text-align: left; }
.it:hover { background: var(--bg-hover); }
.it.on { background: var(--accent-soft); }
.main { display: flex; flex-direction: column; min-width: 0; }
.when { font-size: 13px; color: var(--text); }
.sub { font-size: 11.5px; color: var(--text-3); }
.dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--text-3); }
.dot.done { background: var(--success); }
.dot.error { background: var(--danger); }
.dot.stopped { background: var(--warning); }
.dot.running { background: var(--accent); animation: pulse 1.2s ease-in-out infinite; }
@keyframes pulse { 50% { opacity: .35; } }
.detail { flex: 1; min-width: 0; }
.head { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; font-size: 12.5px; }
.error { display: flex; gap: 6px; align-items: flex-start; color: var(--danger); background: rgba(212, 76, 71, .08); border-radius: var(--radius-m); padding: 8px 10px; font-size: 13px; margin-bottom: 12px; white-space: pre-wrap; word-break: break-word; }
.summary { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 10px 14px; margin-bottom: 16px; }
.cap { font-size: 11.5px; font-weight: 600; color: var(--text-3); text-transform: uppercase; letter-spacing: .02em; margin-bottom: 6px; }
.tr-cap { margin-top: 4px; }
.transcript { border-top: 1px solid var(--border); padding-top: 8px; }
@media (max-width: 760px) {
  .runs { flex-direction: column; align-items: stretch; }
  .list { width: auto; max-height: 220px; position: static; }
}
</style>
