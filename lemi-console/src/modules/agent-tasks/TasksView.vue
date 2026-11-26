<script setup lang="ts">
// 定时智能体任务：左边任务列表，右边是所选任务的运行记录（含完整对话）与设置。
// 地址 /agent-tasks/:id?run=<运行 ID> 也是通知里的链接。
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import Segmented from '@/components/ui/Segmented.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import TaskEditor from './TaskEditor.vue'
import TaskRuns from './TaskRuns.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { confirmDialog } from '@/lib/confirm'
import { formatDate, formatRelative } from '@/lib/time'
import { errorText, i18n } from '@/i18n'
import { describe } from './schedule'

const route = useRoute()
const router = useRouter()
const session = useSession()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t

const tasks = ref<any[]>([])
const loaded = ref(false)
const task = ref<any>(null)
const taskError = ref('')
const runsRef = ref<InstanceType<typeof TaskRuns> | null>(null)
const id = computed(() => String(route.params.id || ''))
const isNew = computed(() => id.value === 'new')
const canEdit = computed(() => session.atLeast('admin'))
const view = computed(() => (route.query.view as string) || 'runs')
const viewOptions = computed(() => [{ value: 'runs', label: t('tasks.runs') }, { value: 'settings', label: t('tasks.settings') }])

async function loadList() {
  if (!session.current) return
  try {
    tasks.value = (await api.get(`/workspaces/${session.current.id}/agent-tasks`)).tasks || []
  } catch (e) { toast.error(errorText(e)) } finally { loaded.value = true }
}

async function loadTask() {
  taskError.value = ''
  if (!id.value || isNew.value) { task.value = null; return }
  try {
    const r = await api.get(`/agent-tasks/${id.value}`)
    task.value = r.task
  } catch (e) { task.value = null; taskError.value = errorText(e) }
}

onMounted(() => { loadList(); loadTask() })
watch(id, loadTask)
watch(() => session.current?.id, () => { loadList(); if (task.value && task.value.workspace_id !== session.current?.id) router.replace('/agent-tasks') })

function open(tid: string) { router.push('/agent-tasks/' + tid) }
function setView(v: string) { router.replace({ query: { ...route.query, view: v } }) }

async function onSaved(tk: any) {
  await loadList()
  if (isNew.value) router.replace({ path: '/agent-tasks/' + tk.id, query: { view: 'runs' } })
  else task.value = { ...task.value, ...tk }
}

async function runNow() {
  try {
    const r = await api.post(`/agent-tasks/${task.value.id}/run`)
    task.value = { ...task.value, running: true }
    await runsRef.value?.load()
    router.replace({ query: { view: 'runs', run: r.run_id } })
    loadList()
  } catch (e) { toast.error(errorText(e)) }
}

async function stop() {
  try {
    await api.post(`/agent-tasks/${task.value.id}/stop`)
    toast.info(t('tasks.stopping'))
  } catch (e) { toast.error(errorText(e)) }
}

async function toggleEnabled() {
  const tk = task.value
  try {
    const r = await api.put(`/agent-tasks/${tk.id}`, {
      name: tk.name, prompt: tk.prompt, schedule: tk.schedule, model: tk.model, effort: tk.effort, connections: tk.connections,
      channels: tk.channels, notify_on: tk.notify_on, enabled: !tk.enabled, owner_id: tk.owner_id,
    })
    task.value = { ...tk, ...r.task }
    loadList()
  } catch (e) { toast.error(errorText(e)) }
}

async function remove() {
  if (!(await confirmDialog({ title: t('tasks.deleteTitle'), message: t('tasks.deleteConfirm', { name: task.value.name }), danger: true, okText: t('common.delete') }))) return
  try {
    await api.del(`/agent-tasks/${task.value.id}`)
    await loadList()
    router.replace('/agent-tasks')
  } catch (e) { toast.error(errorText(e)) }
}

async function onRunsChanged() {
  await loadTask()
  loadList()
}

const showList = computed(() => !ui.isMobile || !id.value)
</script>

<template>
  <div class="tv">
    <nav v-if="showList" class="nav">
      <div class="lm-panel-head">
        <button class="back" :title="$t('common.back')" @click="router.push('/')"><Icon name="chevron-left" :size="16" /></button>
        <span>{{ $t('tasks.title') }}</span>
        <InfoTip :width="300" :text="$t('tasks.intro')" />
        <span class="lm-spacer" />
        <IconBtn v-if="canEdit" icon="plus" :size="15" :label="$t('tasks.new')" @click="open('new')" />
        <IconBtn icon="refresh" :size="15" :label="$t('common.refresh')" @click="loadList" />
      </div>
      <div class="items">
        <button v-for="tk in tasks" :key="tk.id" class="it" :class="{ on: tk.id === id, off: !tk.enabled }" @click="open(tk.id)">
          <span class="dot" :class="tk.running ? 'running' : tk.last_status" />
          <span class="main">
            <span class="name lm-ellipsis">{{ tk.name }}</span>
            <span class="sub lm-ellipsis">{{ describe(tk.schedule) }}<template v-if="!tk.enabled"> · {{ $t('tasks.paused') }}</template></span>
          </span>
        </button>
        <div v-if="loaded && !tasks.length" class="lm-empty">{{ canEdit ? $t('tasks.emptyAdmin') : $t('tasks.empty') }}</div>
      </div>
    </nav>

    <section v-if="!showList || !ui.isMobile" class="body">
      <div v-if="ui.isMobile" class="m-back"><button class="back" @click="router.push('/agent-tasks')"><Icon name="chevron-left" :size="16" />{{ $t('tasks.title') }}</button></div>
      <div v-if="isNew" class="inner">
        <h2>{{ $t('tasks.new') }}</h2>
        <TaskEditor :task="null" :can-edit="canEdit" @saved="onSaved" />
      </div>
      <div v-else-if="task" class="inner wide">
        <div class="title">
          <h2 class="lm-ellipsis">{{ task.name }}</h2>
          <span v-if="task.running" class="lm-badge accent">{{ $t('tasks.status.running') }}</span>
          <span v-else-if="!task.enabled" class="lm-badge">{{ $t('tasks.paused') }}</span>
          <span class="lm-spacer" />
          <template v-if="canEdit">
            <Btn v-if="task.running" size="sm" icon="stop" @click="stop">{{ $t('tasks.stop') }}</Btn>
            <Btn v-else size="sm" variant="primary" icon="play" @click="runNow">{{ $t('tasks.runNow') }}</Btn>
            <Btn size="sm" variant="ghost" @click="toggleEnabled">{{ task.enabled ? $t('tasks.pause') : $t('tasks.resume') }}</Btn>
            <IconBtn icon="trash" :label="$t('common.delete')" @click="remove" />
          </template>
        </div>
        <div class="facts">
          <span><Icon name="clock" :size="13" />{{ describe(task.schedule) }}</span>
          <span v-if="task.enabled && task.next_run_at">{{ $t('tasks.nextRun', { t: formatDate(task.next_run_at, true) }) }}</span>
          <span v-if="task.last_run_at">{{ $t('tasks.lastRun', { t: formatRelative(task.last_run_at) }) }}</span>
        </div>
        <Segmented :model-value="view" :options="viewOptions" class="seg" @update:model-value="setView" />
        <TaskRuns v-if="view === 'runs'" ref="runsRef" :task="task" @changed="onRunsChanged" />
        <div v-else class="settings-wrap"><TaskEditor :task="task" :can-edit="canEdit" @saved="onSaved" /></div>
      </div>
      <div v-else-if="taskError" class="lm-empty">{{ taskError }}</div>
      <div v-else class="lm-empty pick">
        <Icon name="task" :size="28" />
        <div>{{ tasks.length ? $t('tasks.pick') : (canEdit ? $t('tasks.emptyAdmin') : $t('tasks.empty')) }}</div>
        <Btn v-if="canEdit" size="sm" icon="plus" @click="open('new')">{{ $t('tasks.new') }}</Btn>
      </div>
    </section>
  </div>
</template>

<style scoped>
.tv { flex: 1; display: flex; min-height: 0; }
.nav { width: 270px; flex: none; border-right: 1px solid var(--border); background: var(--bg-soft); display: flex; flex-direction: column; min-height: 0; }
.back { border: 0; background: none; display: inline-flex; align-items: center; gap: 4px; padding: 4px; border-radius: 6px; color: var(--text-2); }
.back:hover { background: var(--bg-hover); }
.items { flex: 1; overflow: auto; padding: 4px 6px 8px; }
.it { width: 100%; display: flex; align-items: center; gap: 10px; padding: 6px 8px; border: 0; background: none; border-radius: var(--radius-m); text-align: left; }
.it:hover { background: var(--bg-hover); }
.it.on { background: var(--accent-soft); }
.it.off .name { color: var(--text-3); }
.main { display: flex; flex-direction: column; min-width: 0; flex: 1; }
.name { font-size: 13px; color: var(--text); }
.sub { font-size: 11.5px; color: var(--text-3); }
.dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--border-strong); }
.dot.done { background: var(--success); }
.dot.error { background: var(--danger); }
.dot.stopped { background: var(--warning); }
.dot.running { background: var(--accent); animation: pulse 1.2s ease-in-out infinite; }
@keyframes pulse { 50% { opacity: .35; } }
.body { flex: 1; min-width: 0; overflow: auto; }
.inner { max-width: 760px; padding: 24px 32px 48px; }
.inner.wide { max-width: 1100px; }
.inner h2 { font-size: 20px; font-weight: 650; margin: 0 0 18px; }
.title { display: flex; align-items: center; gap: 8px; min-width: 0; }
.title h2 { margin: 0; min-width: 0; }
.facts { display: flex; flex-wrap: wrap; gap: 6px 16px; font-size: 12.5px; color: var(--text-2); margin: 8px 0 16px; }
.facts span { display: inline-flex; align-items: center; gap: 5px; }
.seg { margin-bottom: 16px; }
.settings-wrap { max-width: 760px; }
.pick { display: flex; flex-direction: column; align-items: center; gap: 10px; margin-top: 12vh; }
.m-back { padding: 8px 12px 0; }
@media (max-width: 760px) {
  .nav { width: auto; flex: 1; border-right: 0; }
  .inner { padding: 12px 16px 40px; }
}
</style>
