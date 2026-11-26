<script setup lang="ts">
// 一次备份的详情：概况、实时进度（可取消）、各存储目标上的文件、还原校验结果、警告与错误、备份清单；
// 操作：下载原文件 / 下载为 .sql（一次性链接）、立即校验、还原、删除。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import Icon from '@/components/icons/Icon.vue'
import ProgressBar from './ProgressBar.vue'
import RestoreWizard from './RestoreWizard.vue'
import { api, downloadURL } from '@/api/client'
import { useSession } from '@/stores/session'
import { useConns } from '@/stores/conns'
import { useToast } from '@/stores/toast'
import { confirmDialog } from '@/lib/confirm'
import { formatDate } from '@/lib/time'
import { errorText, i18n } from '@/i18n'
import { formatBytes, formatDuration, live, onProgress, statusClass, type Run, type Target } from './api'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ runId: string; targets: Target[] }>()
const emit = defineEmits<{ changed: [] }>()
const session = useSession()
const conns = useConns()
const toast = useToast()
const t = i18n.global.t

const run = ref<Run | null>(null)
const err = ref('')
const verifyTask = ref('')
const restoreOpen = ref(false)
const dlTarget = ref('')
const showTables = ref(false)
const ws = computed(() => session.current?.id || '')

async function load() {
  if (!props.runId) return
  err.value = ''
  try {
    run.value = await api.get(`/workspaces/${ws.value}/backups/runs/${props.runId}`)
    if (!dlTarget.value) dlTarget.value = okFiles.value[0]?.target_id || ''
  } catch (e) { err.value = errorText(e) }
}
watch(() => [open.value, props.runId], () => { if (open.value) { run.value = null; dlTarget.value = ''; verifyTask.value = ''; load() } }, { immediate: true })

const off = onProgress((p) => {
  if (!open.value || !run.value) return
  if ((p.id === run.value.id || p.id === verifyTask.value) && p.done) {
    if (p.id === verifyTask.value) verifyTask.value = ''
    load()
    emit('changed')
  }
})
onBeforeUnmount(off)

const prog = computed(() => (run.value ? live[run.value.id] : undefined))
const verifyProg = computed(() => (verifyTask.value ? live[verifyTask.value] : undefined))
const okFiles = computed(() => (run.value?.files || []).filter((f) => f.status === 'ok'))
const targetName = (id: string) => props.targets.find((x) => x.id === id)?.name || id
const connOK = computed(() => !!run.value && !!conns.byId[run.value.connection_id])
const canRun = computed(() => session.atLeast('member'))
const tables = computed(() => {
  const out: { db: string; name: string; rows: number; hash: string }[] = []
  for (const d of run.value?.manifest?.databases || []) for (const tb of d.Tables || []) out.push({ db: d.Name, name: tb.Name, rows: tb.Rows, hash: tb.Hash })
  return out
})
const v = computed(() => run.value?.verify || null)

async function download(format: 'raw' | 'sql') {
  try {
    const r = await api.post(`/workspaces/${ws.value}/backups/runs/${props.runId}/download`, { format, target_id: dlTarget.value })
    downloadURL(r.url)
  } catch (e) { toast.error(errorText(e)) }
}
async function verify() {
  try {
    const r = await api.post(`/workspaces/${ws.value}/backups/runs/${props.runId}/verify`)
    verifyTask.value = r.task_id
    if (run.value) run.value.verify_status = 'running'
    toast.info(t('backup.run.verifyStarted'))
  } catch (e) { toast.error(errorText(e)) }
}
async function cancel() {
  try { await api.post(`/workspaces/${ws.value}/backups/runs/${props.runId}/cancel`); toast.info(t('backup.canceling')) } catch (e) { toast.error(errorText(e)) }
}
async function remove() {
  if (!(await confirmDialog({ title: t('backup.run.deleteRun'), message: t('backup.run.deleteConfirm'), danger: true, okText: t('common.delete') }))) return
  try {
    await api.del(`/workspaces/${ws.value}/backups/runs/${props.runId}`)
    toast.success(t('backup.run.deleted'))
    emit('changed')
    open.value = false
  } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <Modal v-model:open="open" :title="$t('backup.run.title')" :width="780">
    <div v-if="err" class="lm-empty">{{ err }}</div>
    <div v-else-if="!run" class="lm-empty">{{ $t('common.loading') }}</div>
    <div v-else class="rd">
      <div class="top">
        <span class="lm-badge" :class="statusClass[run.status]">{{ $t('backup.status.' + run.status) }}</span>
        <span v-if="run.verify_status" class="lm-badge" :class="statusClass[run.verify_status]">{{ $t('backup.verify.' + run.verify_status) }}</span>
        <span class="name lm-ellipsis">{{ run.connection_name }}<template v-if="run.plan_name"> · {{ run.plan_name }}</template></span>
      </div>

      <div v-if="prog && run.status === 'running'" class="live">
        <ProgressBar :p="prog" />
        <Btn v-if="canRun" size="sm" icon="stop" @click="cancel">{{ $t('backup.cancel') }}</Btn>
      </div>
      <div v-if="verifyProg" class="live"><ProgressBar :p="verifyProg" /></div>

      <dl class="facts">
        <div class="wide"><dt>{{ $t('backup.run.file') }}</dt><dd class="lm-mono break">{{ run.file_name || '—' }}</dd></div>
        <div><dt>{{ $t('backup.run.method') }}</dt><dd>{{ $t('backup.method.' + run.method) }}<span v-if="run.tool" class="lm-faint"> · {{ run.tool }}</span></dd></div>
        <div><dt>{{ $t('backup.run.server') }}</dt><dd>{{ run.server_version || '—' }}</dd></div>
        <div><dt>{{ $t('backup.run.databases') }}</dt><dd>{{ run.databases.join(', ') || '—' }}</dd></div>
        <div><dt>{{ $t('backup.run.size') }}</dt><dd>{{ formatBytes(run.size) }}<span class="lm-faint"> · {{ $t('backup.run.rawSize') }} {{ formatBytes(run.raw_size) }}</span></dd></div>
        <div><dt>{{ $t('backup.run.compression') }}</dt><dd>{{ run.compression }}<span v-if="run.encrypted" class="enc"><Icon name="lock" :size="12" />{{ $t('backup.passphraseSet') }}</span></dd></div>
        <div><dt>{{ $t('backup.run.started') }}</dt><dd>{{ formatDate(run.started_at, true) }}</dd></div>
        <div><dt>{{ $t('backup.run.duration') }}</dt><dd>{{ run.finished_at ? formatDuration(run.finished_at - run.started_at) : '—' }}</dd></div>
        <div><dt>{{ $t('backup.run.trigger') }}</dt><dd>{{ $t('backup.trigger.' + run.trigger) }}<template v-if="run.attempt"> #{{ run.attempt }}</template></dd></div>
        <div v-if="run.sha256" class="wide"><dt>{{ $t('backup.run.sha256') }}</dt><dd class="lm-mono break">{{ run.sha256 }}</dd></div>
      </dl>

      <div v-if="run.error" class="box danger"><Icon name="error" :size="14" /><span>{{ run.error }}</span></div>
      <div v-if="run.warnings?.length" class="box warn">
        <Icon name="warning" :size="14" />
        <ul><li v-for="w in run.warnings" :key="w">{{ w }}</li></ul>
      </div>

      <template v-if="run.files?.length">
        <h4>{{ $t('backup.run.files') }}</h4>
        <div class="files">
          <div v-for="f in run.files" :key="f.target_id" class="file">
            <span class="lm-badge" :class="statusClass[f.status] || ''">{{ $t('backup.run.fileStatus.' + f.status) }}</span>
            <span class="tname">{{ targetName(f.target_id) }}</span>
            <span class="lm-mono lm-faint lm-ellipsis path">{{ f.path }}</span>
            <span v-if="f.error" class="ferr">{{ f.error }}</span>
          </div>
        </div>
      </template>

      <template v-if="v">
        <h4>{{ $t('backup.run.verifyResult') }}</h4>
        <div class="box" :class="v.ok ? 'ok' : 'danger'">
          <Icon :name="v.ok ? 'success' : 'error'" :size="14" />
          <div class="vb">
            <div>{{ $t('backup.run.tablesChecked', { n: v.tables_checked || 0, r: (v.rows_checked || 0).toLocaleString() }) }}
              · {{ v.hash_checked ? $t('backup.run.hashChecked') : $t('backup.run.countOnly') }}</div>
            <div v-if="v.integrity" class="lm-faint">{{ $t('backup.run.integrity') }}：{{ v.integrity }}</div>
            <div v-if="v.temp_databases?.length" class="lm-faint">{{ $t('backup.run.tempDb') }}：{{ v.temp_databases.join(', ') }} ·
              {{ v.temp_db_dropped ? $t('backup.run.tempDropped') : $t('backup.run.tempLeft', { e: (v.temp_drop_errors || []).join('；') }) }}</div>
            <div v-if="v.error" class="verr">{{ v.error }}</div>
            <table v-if="v.mismatches?.length" class="lm-table mm">
              <thead><tr><th>{{ $t('backup.run.tables') }}</th><th>{{ $t('backup.run.expected') }}</th><th>{{ $t('backup.run.got') }}</th><th /></tr></thead>
              <tbody><tr v-for="m in v.mismatches" :key="m.database + m.table"><td class="lm-mono">{{ m.database }}.{{ m.table }}</td><td>{{ m.expected_rows }}</td><td>{{ m.got_rows }}</td><td class="verr">{{ m.error || (m.expected_hash !== m.got_hash ? $t('backup.run.hash') : '') }}</td></tr></tbody>
            </table>
          </div>
        </div>
      </template>

      <template v-if="tables.length">
        <button type="button" class="toggle" @click="showTables = !showTables"><Icon :name="showTables ? 'chevron-down' : 'chevron-right'" :size="13" />{{ $t('backup.run.manifest') }} · {{ tables.length }}</button>
        <div v-if="showTables" class="tbl-wrap">
          <table class="lm-table">
            <thead><tr><th>{{ $t('backup.run.tables') }}</th><th class="num">{{ $t('backup.run.rows') }}</th><th>{{ $t('backup.run.hash') }}</th></tr></thead>
            <tbody><tr v-for="tb in tables" :key="tb.db + '.' + tb.name"><td class="lm-mono">{{ tb.db }}.{{ tb.name }}</td><td class="num">{{ tb.rows.toLocaleString() }}</td><td class="lm-mono lm-faint">{{ tb.hash || '—' }}</td></tr></tbody>
          </table>
        </div>
      </template>
    </div>
    <template v-if="run && run.status === 'success'" #footer>
      <div class="acts">
        <select v-if="okFiles.length > 1" v-model="dlTarget" class="lm-select src" :aria-label="$t('backup.run.downloadFrom')">
          <option v-for="f in okFiles" :key="f.target_id" :value="f.target_id">{{ targetName(f.target_id) }}</option>
        </select>
        <Btn size="sm" icon="download" @click="download('raw')">{{ $t('backup.run.downloadRaw') }}</Btn>
        <Btn size="sm" icon="download" @click="download('sql')">{{ run.method === 'sqlite_file' ? $t('backup.run.downloadDb') : $t('backup.run.downloadSql') }}</Btn>
        <span class="lm-spacer" />
        <Btn v-if="canRun" size="sm" icon="check" :loading="run.verify_status === 'running'" @click="verify">{{ $t('backup.run.verifyNow') }}</Btn>
        <Btn v-if="canRun" size="sm" variant="primary" icon="history" @click="restoreOpen = true">{{ $t('backup.run.restore') }}</Btn>
        <Btn v-if="session.atLeast('admin')" size="sm" variant="ghost" icon="trash" @click="remove">{{ $t('common.delete') }}</Btn>
      </div>
    </template>
    <template v-else-if="run && run.status !== 'running' && session.atLeast('admin')" #footer>
      <span class="lm-spacer" />
      <Btn size="sm" variant="ghost" icon="trash" @click="remove">{{ $t('common.delete') }}</Btn>
    </template>
    <RestoreWizard v-if="run" v-model:open="restoreOpen" :run="run" :targets="targets" :default-conn="connOK ? run.connection_id : ''" @done="emit('changed')" />
  </Modal>
</template>

<style scoped>
.rd { display: flex; flex-direction: column; gap: 14px; }
.top { display: flex; align-items: center; gap: 8px; min-width: 0; }
.top .name { font-weight: 600; font-size: 14px; }
.live { display: flex; align-items: center; gap: 12px; padding: 10px 12px; background: var(--bg-soft); border-radius: var(--radius-m); }
.live > :first-child { flex: 1; min-width: 0; }
.facts { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 20px; margin: 0; }
.facts > div { min-width: 0; }
.facts .wide { grid-column: 1 / -1; }
dt { font-size: 11.5px; color: var(--text-3); margin-bottom: 2px; }
dd { margin: 0; font-size: 13px; display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.break { word-break: break-all; }
.enc { display: inline-flex; align-items: center; gap: 3px; margin-left: 8px; color: var(--text-2); }
h4 { font-size: 13px; font-weight: 600; margin: 4px 0 -4px; }
.box { display: flex; gap: 8px; align-items: flex-start; padding: 10px 12px; border-radius: var(--radius-m); font-size: 12.5px; line-height: 1.5; word-break: break-word; }
.box > :first-child { margin-top: 2px; flex: none; }
.box.danger { background: var(--danger-bg); color: var(--danger); }
.box.warn { background: rgba(194, 124, 14, .1); color: var(--warning); }
.box.ok { background: rgba(47, 138, 91, .08); color: var(--text); }
.box.ok > :first-child { color: var(--success); }
.box ul { margin: 0; padding-left: 16px; }
.vb { display: flex; flex-direction: column; gap: 4px; min-width: 0; flex: 1; color: var(--text); }
.verr { color: var(--danger); }
.mm { margin-top: 6px; background: var(--bg); border-radius: var(--radius-s); }
.files { display: flex; flex-direction: column; border: 1px solid var(--border); border-radius: var(--radius-m); }
.file { display: flex; align-items: center; gap: 8px; padding: 8px 10px; border-bottom: 1px solid var(--border); flex-wrap: wrap; min-width: 0; }
.file:last-child { border-bottom: 0; }
.tname { font-size: 13px; font-weight: 500; }
.path { flex: 1; min-width: 0; font-size: 11.5px; }
.ferr { flex-basis: 100%; font-size: 12px; color: var(--danger); }
.toggle { display: inline-flex; align-items: center; gap: 4px; border: 0; background: none; font-size: 13px; font-weight: 600; padding: 0; color: var(--text); align-self: flex-start; }
.tbl-wrap { max-height: 280px; overflow: auto; border: 1px solid var(--border); border-radius: var(--radius-m); }
.num { text-align: right; }
.acts { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; width: 100%; }
.src { width: auto; max-width: 180px; height: 30px; }
@media (max-width: 760px) { .facts { gap: 8px 14px; } }
</style>
