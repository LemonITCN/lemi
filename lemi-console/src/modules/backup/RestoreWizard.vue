<script setup lang="ts">
// 还原向导：选目标连接与读取的存储目标 → 库名映射 → 预检（目标非空要输入库名确认）→ 执行（实时进度）→ 结果。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import Icon from '@/components/icons/Icon.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import ProgressBar from './ProgressBar.vue'
import { api, ApiError } from '@/api/client'
import { useSession } from '@/stores/session'
import { useConns } from '@/stores/conns'
import { errorText, i18n } from '@/i18n'
import { live, onProgress, type Run, type Target } from './api'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ run: Run; targets: Target[]; defaultConn?: string }>()
const emit = defineEmits<{ done: [] }>()
const session = useSession()
const conns = useConns()
const t = i18n.global.t

interface Item { source: string; target: string; exists: boolean; tables: number; need_confirm: boolean; confirm_text?: string }
const step = ref(1)
const connId = ref('')
const source = ref('')
const method = ref('')
const mapping = ref<Record<string, string>>({})
const items = ref<Item[]>([])
const confirm = ref<Record<string, string>>({})
const checking = ref(false)
const err = ref('')
const rec = ref<any>(null)
const starting = ref(false)
const ws = computed(() => session.current?.id || '')

const choices = computed(() => conns.list.filter((c) => c.driver === props.run.driver && !c.read_only && (c.access === 'write' || c.access === 'manage')))
const okFiles = computed(() => (props.run.files || []).filter((f) => f.status === 'ok'))
const isMySQL = computed(() => props.run.driver === 'mysql')
const methodOptions = computed(() => {
  if (props.run.method === 'mysqldump') return ['mysql', 'lemi']
  if (props.run.method === 'sqlite_file') return ['sqlite_file']
  return ['lemi']
})
const methodLabel = (m: string) => ({ mysql: t('backup.restore.methodMysql'), lemi: t('backup.restore.methodLemi'), sqlite_file: t('backup.restore.methodFile') } as Record<string, string>)[m] || m

watch(open, (o) => {
  if (!o) return
  step.value = 1
  connId.value = props.defaultConn && choices.value.some((c) => c.id === props.defaultConn) ? props.defaultConn : choices.value[0]?.id || ''
  source.value = ''
  method.value = methodOptions.value[0]
  mapping.value = Object.fromEntries(props.run.databases.map((d) => [d, d]))
  items.value = []
  confirm.value = {}
  err.value = ''
  rec.value = null
}, { immediate: true })

function body() {
  return { connection_id: connId.value, database_map: isMySQL.value ? mapping.value : {}, method: method.value, target_id: source.value, confirm: confirm.value }
}
async function precheck() {
  checking.value = true
  err.value = ''
  try {
    const r = await api.post(`/workspaces/${ws.value}/backups/runs/${props.run.id}/restore/precheck`, body())
    items.value = r.items || []
    if (r.method_error) err.value = r.method_error
    step.value = 2
  } catch (e) { err.value = errorText(e) } finally { checking.value = false }
}
const confirmed = computed(() => items.value.every((it) => !it.need_confirm || (confirm.value[it.target] || '').trim() === it.confirm_text))

async function start() {
  starting.value = true
  err.value = ''
  try {
    rec.value = await api.post(`/workspaces/${ws.value}/backups/runs/${props.run.id}/restore`, body())
    step.value = 3
  } catch (e) {
    if (e instanceof ApiError && e.code === 'restore_confirm_required' && Array.isArray(e.extra)) items.value = e.extra
    err.value = errorText(e)
  } finally { starting.value = false }
}
const prog = computed(() => (rec.value ? live[rec.value.id] : undefined))
const off = onProgress(async (p) => {
  if (rec.value && p.id === rec.value.id && p.done) {
    try { rec.value = await api.get(`/workspaces/${ws.value}/backups/restores/${rec.value.id}`) } catch { /* 留着进度里的状态 */ }
    emit('done')
  }
})
onBeforeUnmount(off)
const finished = computed(() => rec.value && rec.value.status !== 'running')
const importRes = computed(() => rec.value?.result?.import || null)
async function cancelRestore() {
  try { await api.post(`/workspaces/${ws.value}/backups/tasks/${rec.value.id}/cancel`) } catch (e) { err.value = errorText(e) }
}
</script>

<template>
  <Modal v-model:open="open" :title="$t('backup.restore.title')" :width="640">
    <div class="steps">
      <span v-for="n in [1, 2, 3]" :key="n" class="st" :class="{ on: step === n, done: step > n }"><b>{{ n }}</b>{{ $t('backup.restore.step.' + n) }}</span>
    </div>

    <div v-if="step === 1" class="lm-form">
      <div class="lm-field full">
        <span class="lm-label">{{ $t('backup.restore.target') }}<InfoTip :width="280" :text="$t('backup.restore.targetHelp')" /></span>
        <select v-model="connId" class="lm-select">
          <option v-for="c in choices" :key="c.id" :value="c.id">{{ c.name }}<template v-if="c.env === 'prod'"> · PROD</template></option>
        </select>
        <div v-if="!choices.length" class="hint">{{ $t('backup.restore.noTarget') }}</div>
      </div>
      <div v-if="okFiles.length > 1" class="lm-field">
        <span class="lm-label">{{ $t('backup.restore.source') }}</span>
        <select v-model="source" class="lm-select">
          <option value="">{{ $t('backup.restore.auto') }}</option>
          <option v-for="f in okFiles" :key="f.target_id" :value="f.target_id">{{ targets.find((x) => x.id === f.target_id)?.name || f.target_id }}</option>
        </select>
      </div>
      <div class="lm-field">
        <span class="lm-label">{{ $t('backup.restore.method') }}</span>
        <select v-model="method" class="lm-select" :disabled="methodOptions.length < 2">
          <option v-for="m in methodOptions" :key="m" :value="m">{{ methodLabel(m) }}</option>
        </select>
      </div>
      <div v-if="isMySQL" class="lm-field full">
        <span class="lm-label">{{ $t('backup.restore.mapping') }}<InfoTip :width="300" :text="$t('backup.restore.mappingHelp')" /></span>
        <div class="map">
          <div v-for="d in run.databases" :key="d" class="mr">
            <span class="lm-mono lm-ellipsis src">{{ d }}</span>
            <Icon name="arrow-right" :size="14" class="ar" />
            <input v-model="mapping[d]" class="lm-select lm-mono" spellcheck="false" />
          </div>
        </div>
      </div>
      <p v-else class="full hint">{{ $t('backup.restore.sqliteReplace') }}</p>
    </div>

    <div v-else-if="step === 2" class="pc">
      <div v-for="it in items" :key="it.target" class="item" :class="{ warn: it.need_confirm }">
        <div class="ih">
          <Icon :name="it.need_confirm ? 'warning' : 'success'" :size="15" />
          <span class="lm-mono">{{ isMySQL ? it.target : conns.byId[connId]?.name }}</span>
          <span class="lm-faint">{{ !it.exists ? $t('backup.restore.notExists') : it.tables ? $t('backup.restore.tables', { n: it.tables }) : $t('backup.restore.empty') }}</span>
        </div>
        <template v-if="it.need_confirm">
          <p class="cm">{{ isMySQL ? $t('backup.restore.needConfirm', { name: it.confirm_text }) : $t('backup.restore.sqliteConfirm', { name: it.confirm_text }) }}</p>
          <input v-model="confirm[it.target]" class="lm-select lm-mono" :placeholder="$t('backup.restore.confirmPh', { name: it.confirm_text })" spellcheck="false" autocomplete="off" />
        </template>
      </div>
    </div>

    <div v-else class="run">
      <ProgressBar v-if="prog && !finished" :p="prog" />
      <div v-else-if="!finished" class="lm-muted">{{ $t('backup.restore.running') }}</div>
      <div v-if="finished" class="res" :class="rec.status === 'success' ? 'ok' : 'bad'">
        <Icon :name="rec.status === 'success' ? 'success' : 'error'" :size="18" />
        <div>
          <div class="rt">{{ rec.status === 'success' ? $t('backup.restore.done') : $t('backup.restore.failed') }}</div>
          <div v-if="rec.error" class="re">{{ rec.error }}</div>
          <div v-if="importRes" class="lm-faint">{{ $t('backup.restore.statements', { n: importRes.Statements ?? importRes.statements ?? 0 }) }}<template v-if="(importRes.Errors ?? importRes.errors)"> · {{ $t('backup.restore.errors', { n: importRes.Errors ?? importRes.errors }) }}</template></div>
          <div v-if="rec.result?.previous_file" class="lm-faint">{{ $t('backup.restore.previous', { f: rec.result.previous_file }) }}</div>
        </div>
      </div>
    </div>

    <div v-if="err" class="err"><Icon name="error" :size="14" />{{ err }}</div>

    <template #footer>
      <Btn v-if="step === 2" variant="ghost" @click="step = 1">{{ $t('backup.restore.back') }}</Btn>
      <span class="lm-spacer" />
      <template v-if="step === 1">
        <Btn variant="ghost" @click="open = false">{{ $t('common.cancel') }}</Btn>
        <Btn variant="primary" :loading="checking" :disabled="!connId" @click="precheck">{{ $t('backup.restore.precheck') }}</Btn>
      </template>
      <template v-else-if="step === 2">
        <Btn variant="primary" :loading="starting" :disabled="!confirmed" @click="start">{{ $t('backup.restore.start') }}</Btn>
      </template>
      <template v-else>
        <Btn v-if="!finished" icon="stop" @click="cancelRestore">{{ $t('backup.cancel') }}</Btn>
        <Btn v-else variant="primary" @click="open = false">{{ $t('common.done') }}</Btn>
      </template>
    </template>
  </Modal>
</template>

<style scoped>
.steps { display: flex; gap: 16px; margin-bottom: 18px; font-size: 12.5px; color: var(--text-3); }
.st { display: inline-flex; align-items: center; gap: 6px; }
.st b { width: 20px; height: 20px; border-radius: 50%; display: inline-flex; align-items: center; justify-content: center; font-size: 11px; background: var(--bg-active); color: var(--text-2); font-weight: 600; }
.st.on { color: var(--text); font-weight: 500; }
.st.on b { background: var(--accent); color: #fff; }
.st.done b { background: var(--accent-soft); color: var(--accent); }
.hint { font-size: 12.5px; color: var(--text-3); margin: 6px 0 0; }
.map { display: flex; flex-direction: column; gap: 6px; }
.mr { display: grid; grid-template-columns: minmax(0, 1fr) 20px minmax(0, 1.2fr); align-items: center; gap: 8px; }
.src { font-size: 12.5px; }
.ar { color: var(--text-3); }
.pc { display: flex; flex-direction: column; gap: 8px; }
.item { border: 1px solid var(--border); border-radius: var(--radius-m); padding: 10px 12px; }
.item.warn { border-color: var(--warning); background: rgba(194, 124, 14, .06); }
.ih { display: flex; align-items: center; gap: 8px; font-size: 13px; flex-wrap: wrap; }
.item:not(.warn) .ih :deep(svg) { color: var(--success); }
.item.warn .ih :deep(svg) { color: var(--warning); }
.cm { margin: 8px 0; font-size: 12.5px; color: var(--text-2); }
.run { min-height: 60px; }
.res { display: flex; gap: 10px; align-items: flex-start; padding: 12px; border-radius: var(--radius-m); font-size: 13px; }
.res.ok { background: rgba(47, 138, 91, .08); }
.res.ok > :first-child { color: var(--success); }
.res.bad { background: var(--danger-bg); }
.res.bad > :first-child { color: var(--danger); }
.rt { font-weight: 600; margin-bottom: 2px; }
.re { color: var(--danger); word-break: break-word; }
.err { margin-top: 12px; display: flex; gap: 6px; align-items: flex-start; font-size: 12.5px; color: var(--danger); background: var(--danger-bg); padding: 8px 10px; border-radius: var(--radius-m); word-break: break-word; }
@media (max-width: 760px) { .steps { gap: 10px; } }
</style>
