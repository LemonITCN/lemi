<script setup lang="ts">
// 备份页（/backups/:section?）：计划、运行记录、存储目标、还原记录。
// 查询参数：connection 只看某连接；plan 只看某计划的运行记录；run 打开某次备份详情（通知里的链接）；new=1 新建计划。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Btn from '@/components/ui/Btn.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Segmented from '@/components/ui/Segmented.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import PlansPane from './PlansPane.vue'
import RunsPane from './RunsPane.vue'
import RestoresPane from './RestoresPane.vue'
import TargetsPane from './TargetsPane.vue'
import PlanEditor from './PlanEditor.vue'
import TargetEditor from './TargetEditor.vue'
import RunDetail from './RunDetail.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useConns } from '@/stores/conns'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { confirmDialog } from '@/lib/confirm'
import { errorText, i18n } from '@/i18n'
import { formatBytes, live, onProgress, watchProgress, type Plan, type Run, type Target, type TargetKind } from './api'

const route = useRoute()
const router = useRouter()
const session = useSession()
const conns = useConns()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t

const section = computed(() => (route.params.section as string) || 'plans')
const connFilter = computed(() => (route.query.connection as string) || '')
const planFilter = computed(() => (route.query.plan as string) || '')
const sections = computed(() => ['plans', 'runs', 'targets', 'restores'].map((s) => ({ value: s, label: t('backup.sections.' + s) })))

const plans = ref<Plan[]>([])
const targets = ref<Target[]>([])
const kinds = ref<TargetKind[]>([])
const recent = ref<Run[]>([])
const loaded = ref(false)
const planEdit = ref<Plan | null>(null)
const planOpen = ref(false)
const planConn = ref('')
const targetEdit = ref<Target | null>(null)
const targetOpen = ref(false)
const runOpen = ref(false)
const runId = ref('')
const runsRef = ref<InstanceType<typeof RunsPane> | null>(null)

async function load() {
  const ws = session.current?.id
  if (!ws) return
  try {
    const [p, tg, k, r] = await Promise.all([
      api.get(`/workspaces/${ws}/backups/plans`), api.get(`/workspaces/${ws}/backups/targets`),
      kinds.value.length ? Promise.resolve({ kinds: kinds.value }) : api.get(`/workspaces/${ws}/backups/kinds`),
      api.get(`/workspaces/${ws}/backups/runs?limit=200`),
    ])
    plans.value = p.plans || []
    targets.value = tg.targets || []
    kinds.value = k.kinds || []
    recent.value = r.runs || []
  } catch (e) { toast.error(errorText(e)) } finally { loaded.value = true }
}

const lastRuns = computed(() => {
  const out: Record<string, Run> = {}
  for (const r of recent.value) if (r.plan_id && !out[r.plan_id]) out[r.plan_id] = r
  return out
})
const summary = computed(() => {
  const day = Date.now() - 86400000
  const last = recent.value.filter((r) => r.started_at >= day)
  return {
    plans: plans.value.length,
    running: Object.values(live).filter((p) => p.kind === 'backup' && !p.done).length,
    ok: last.filter((r) => r.status === 'success').length,
    fail: last.filter((r) => r.status === 'failed').length,
    bytes: targets.value.reduce((a, tg) => a + (tg.usage?.bytes || 0), 0),
    files: targets.value.reduce((a, tg) => a + (tg.usage?.files || 0), 0),
  }
})

let reloadTimer: ReturnType<typeof setTimeout> | null = null
const off = onProgress((p) => {
  if (p.done && (p.kind === 'backup' || p.kind === 'verify')) {
    if (reloadTimer) clearTimeout(reloadTimer)
    reloadTimer = setTimeout(load, 400)
  }
})
onMounted(async () => {
  watchProgress()
  await conns.load().catch(() => {})
  await load()
  applyQuery()
})
onBeforeUnmount(() => { off(); if (reloadTimer) clearTimeout(reloadTimer) })
watch(() => session.current?.id, () => { kinds.value = []; load() })
watch(() => route.query.run, applyQuery)

function applyQuery() {
  const q = route.query
  if (q.run) { runId.value = String(q.run); runOpen.value = true }
  if (q.new === '1' && session.atLeast('admin')) { openPlan(null, connFilter.value); router.replace({ query: { ...q, new: undefined } }) }
}
watch(runOpen, (o) => { if (!o && route.query.run) router.replace({ query: { ...route.query, run: undefined } }) })

function go(s: string) { router.push({ path: '/backups/' + s, query: { connection: connFilter.value || undefined } }) }
function setFilter(f: { connection?: string; plan?: string }) {
  router.replace({ query: { ...route.query, connection: f.connection || undefined, plan: f.plan || undefined } })
}
function openPlan(p: Plan | null, connId = '') {
  planEdit.value = p
  planConn.value = connId
  planOpen.value = true
}
function openTarget(tg: Target | null) {
  targetEdit.value = tg
  targetOpen.value = true
}
function openRun(id: string) {
  runId.value = id
  runOpen.value = true
}
async function runPlan(p: Plan) {
  try {
    const r = await api.post(`/workspaces/${session.current?.id}/backups/plans/${p.id}/run`)
    toast.success(t('backup.started'))
    plans.value = plans.value.map((x) => (x.id === p.id ? { ...x, busy: r.id } : x))
    recent.value = [r, ...recent.value]
  } catch (e) { toast.error(errorText(e)) }
}
async function removePlan(p: Plan) {
  if (!(await confirmDialog({ title: t('backup.deletePlan'), message: t('backup.deletePlanConfirm', { name: p.name }), danger: true, okText: t('common.delete') }))) return
  try {
    await api.del(`/workspaces/${session.current?.id}/backups/plans/${p.id}`)
    load()
  } catch (e) { toast.error(errorText(e)) }
}
function showRuns(p: Plan) { router.push({ path: '/backups/runs', query: { connection: p.connection_id, plan: p.id } }) }
async function onTargetSaved() {
  await load()
}
</script>

<template>
  <div class="lm-page">
  <div class="bv">
    <div class="head">
      <button v-if="ui.isMobile" class="back" :title="$t('common.back')" @click="router.push('/')"><Icon name="chevron-left" :size="16" /></button>
      <h2>{{ $t('backup.title') }}</h2>
      <InfoTip :width="320" :text="$t('backup.intro')" />
      <span class="lm-spacer" />
      <template v-if="session.atLeast('admin')">
        <Btn v-if="section === 'targets'" size="sm" variant="primary" icon="plus" @click="openTarget(null)">{{ $t('backup.targets.new') }}</Btn>
        <Btn v-else-if="section === 'plans'" size="sm" variant="primary" icon="plus" @click="openPlan(null, connFilter)">{{ $t('backup.newPlan') }}</Btn>
      </template>
      <IconBtn icon="refresh" :size="16" :label="$t('common.refresh')" @click="load(); runsRef?.load()" />
    </div>

    <div class="stats">
      <div class="stat"><span class="k">{{ $t('backup.summary.plans') }}</span><b>{{ summary.plans }}</b></div>
      <div class="stat"><span class="k">{{ $t('backup.summary.running') }}</span><b :class="{ accent: summary.running }">{{ summary.running }}</b></div>
      <div class="stat"><span class="k">{{ $t('backup.summary.ok24') }}</span><b class="ok">{{ summary.ok }}</b></div>
      <div class="stat"><span class="k">{{ $t('backup.summary.fail24') }}</span><b :class="{ bad: summary.fail }">{{ summary.fail }}</b></div>
      <div class="stat"><span class="k">{{ $t('backup.summary.storage') }}</span><b>{{ formatBytes(summary.bytes) }}</b><span class="sub">{{ $t('backup.summary.files', { n: summary.files }) }}</span></div>
    </div>

    <div class="bar">
      <Segmented :model-value="section" :options="sections" class="seg" @update:model-value="go" />
      <select v-if="section === 'plans'" :value="connFilter" class="lm-select cf" :aria-label="$t('backup.filterConn')" @change="setFilter({ connection: ($event.target as HTMLSelectElement).value })">
        <option value="">{{ $t('backup.allConns') }}</option>
        <option v-for="c in conns.list.filter((x) => x.driver === 'mysql' || x.driver === 'sqlite')" :key="c.id" :value="c.id">{{ c.name }}</option>
      </select>
    </div>

    <PlansPane v-if="section === 'plans'" :plans="plans" :last-runs="lastRuns" :conn-filter="connFilter" :loaded="loaded"
      @edit="(p) => openPlan(p)" @create="(c) => openPlan(null, c)" @run="runPlan" @remove="removePlan" @open-run="openRun" @show-runs="showRuns" />
    <RunsPane v-else-if="section === 'runs'" ref="runsRef" :plans="plans" :conn-filter="connFilter" :plan-filter="planFilter" @open-run="openRun" @filter="setFilter" />
    <TargetsPane v-else-if="section === 'targets'" :targets="targets" :kinds="kinds" :plans="plans" :loaded="loaded" @edit="openTarget" @changed="load" />
    <RestoresPane v-else @open-run="openRun" />

    <PlanEditor v-model:open="planOpen" :plan="planEdit" :connection-id="planConn" :targets="targets" @saved="load" @new-target="openTarget(null)" />
    <TargetEditor v-model:open="targetOpen" :target="targetEdit" :kinds="kinds" @saved="onTargetSaved" />
    <RunDetail v-model:open="runOpen" :run-id="runId" :targets="targets" @changed="load(); runsRef?.load()" />
  </div>
  </div>
</template>

<style scoped>
.bv { max-width: 1120px; width: 100%; margin: 0 auto; }
.head { display: flex; align-items: center; gap: 6px; margin-bottom: 14px; }
.head h2 { font-size: 20px; font-weight: 650; margin: 0; }
.back { border: 0; background: none; display: inline-flex; padding: 4px; border-radius: 6px; color: var(--text-2); }
.stats { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 10px; margin-bottom: 18px; }
.stat { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 10px 12px; display: flex; flex-direction: column; gap: 2px; background: var(--bg); min-width: 0; }
.stat .k { font-size: 11.5px; color: var(--text-3); }
.stat b { font-size: 18px; font-weight: 600; font-variant-numeric: tabular-nums; }
.stat b.ok { color: var(--success); }
.stat b.bad { color: var(--danger); }
.stat b.accent { color: var(--accent); }
.stat .sub { font-size: 11.5px; color: var(--text-3); }
.bar { display: flex; align-items: center; gap: 10px; margin-bottom: 14px; flex-wrap: wrap; }
.seg { max-width: 100%; overflow-x: auto; }
.cf { width: auto; min-width: 160px; max-width: 260px; height: 30px; font-size: 12.5px; margin-left: auto; }
@media (max-width: 760px) {
  .stats { display: flex; overflow-x: auto; gap: 8px; margin: 0 -16px 16px; padding: 0 16px; scrollbar-width: none; }
  .stats .stat { flex: none; min-width: 108px; padding: 8px 10px; }
  .stat b { font-size: 16px; }
  .cf { margin-left: 0; flex: 1; max-width: none; }
  .head h2 { font-size: 18px; }
}
</style>
