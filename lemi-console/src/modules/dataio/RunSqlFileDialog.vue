<script setup lang="ts">
// 运行 SQL 文件（.sql / .sql.gz）：边上传边执行，进度经实时通道推送（dump_import_progress）。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import Segmented from '@/components/ui/Segmented.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import { api, ApiError } from '@/api/client'
import { useConns, type Conn } from '@/stores/conns'
import { useToast } from '@/stores/toast'
import { on } from '@/lib/realtime'
import { confirmDialog } from '@/lib/confirm'
import { dialectOf } from '@/lib/dialect'
import { formatBytes } from '@/lib/meta'
import { errorText, i18n } from '@/i18n'

const props = defineProps<{ conn: Conn; database: string; onDone: (v?: boolean) => void }>()
const conns = useConns()
const toast = useToast()
const t = i18n.global.t
const open = ref(true)
const mysql = computed(() => dialectOf(props.conn) === 'mysql')

const file = ref<File | null>(null)
const input = ref<HTMLInputElement>()
const dbs = ref<string[]>([])
const target = ref(props.database)
const policy = ref<'stop' | 'continue'>('stop')
const phase = ref<'form' | 'running' | 'done'>('form')
const prog = ref({ bytes: 0, total: 0, statements: 0, errors: 0, table: '' })
const result = ref<any>(null)
const failMsg = ref('')
let ctrl: AbortController | null = null
let off: (() => void) | null = null

onMounted(async () => {
  if (!mysql.value) return
  try { dbs.value = (await conns.loadDatabases(props.conn.id)).filter((d) => !d.system).map((d) => d.name) } catch {}
})
onBeforeUnmount(() => { off?.(); ctrl?.abort() })

function pick(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (f) file.value = f
}

/** 服务端结构体没有 json 标签时字段是大写开头，这里两种都认。 */
function norm(r: any) {
  if (!r) return null
  const g = (a: string, b: string) => (r[a] !== undefined ? r[a] : r[b])
  const list = (g('error_list', 'ErrorList') || []).map((x: any) => ({ index: x.index ?? x.Index, line: x.line ?? x.Line, snippet: x.snippet ?? x.Snippet, error: x.error ?? x.Error }))
  return { statements: g('statements', 'Statements') || 0, errors: g('errors', 'Errors') || 0, errorList: list, bytes: g('bytes', 'Bytes') || 0,
    complete: !!g('complete', 'Complete'), format: g('format', 'Format') || 'unknown', skipped: g('skipped', 'Skipped') || 0 }
}

const pct = computed(() => (prog.value.total > 0 ? Math.min(100, (prog.value.bytes / prog.value.total) * 100) : 0))
const res = computed(() => norm(result.value))

function close(v?: boolean) {
  if (phase.value === 'running') return
  open.value = false
  props.onDone(v)
}

async function run() {
  if (!file.value) return
  if (props.conn.env === 'prod' && !(await confirmDialog({ title: t('dataio.prodRunTitle'), message: t('dataio.prodRunHint', { name: props.conn.name }), danger: true, okText: t('dataio.run') }))) return
  phase.value = 'running'
  failMsg.value = ''
  result.value = null
  const gz = /\.gz$/i.test(file.value.name)
  prog.value = { bytes: 0, total: gz ? 0 : file.value.size, statements: 0, errors: 0, table: '' }
  off = on('dump_import_progress', (d: any) => {
    if (d?.connection_id !== props.conn.id) return
    prog.value = { bytes: d.bytes || 0, total: d.total || prog.value.total, statements: d.statements || 0, errors: d.errors || 0, table: d.table || '' }
  })
  ctrl = new AbortController()
  // 表单字段必须放在文件前面：服务端边读边执行，读到文件部分就开始了
  const fd = new FormData()
  fd.append('db', mysql.value ? target.value : '')
  fd.append('continue_on_error', policy.value === 'continue' ? 'true' : 'false')
  fd.append('size', String(file.value.size))
  fd.append('file', file.value, file.value.name)
  try {
    result.value = await api.post(`/connections/${props.conn.id}/dump/import`, fd, { signal: ctrl.signal })
    const r = norm(result.value)!
    if (r.errors) toast.info(t('dataio.doneWithErrors', { n: r.statements, e: r.errors }))
    else toast.success(t('dataio.doneOk', { n: r.statements }))
  } catch (e: any) {
    if (e?.name === 'AbortError') failMsg.value = t('dataio.aborted')
    else {
      failMsg.value = errorText(e)
      if (e instanceof ApiError && e.extra) result.value = e.extra
    }
  } finally {
    off?.()
    off = null
    ctrl = null
    phase.value = 'done'
    // 文件可能建了库、表：刷新对象树（不清空缓存，免得已展开的节点闪掉）
    conns.loadDatabases(props.conn.id, true).catch(() => {})
    const db = mysql.value ? target.value : props.database
    if (db) conns.loadObjects(props.conn.id, db, true).catch(() => {})
  }
}

function abort() { ctrl?.abort() }
</script>

<template>
  <Modal :open="open" :title="$t('dataio.runTitle')" :width="600" :hide-close="phase === 'running'" @update:open="(v: boolean) => !v && close()">
    <div v-if="phase === 'form'" class="form">
      <div class="lm-field">
        <span class="lm-label">{{ $t('dataio.file') }}<InfoTip :text="$t('dataio.fileTypes')" /></span>
        <div class="picker" @click="input?.click()">
          <Icon name="file" :size="18" />
          <span v-if="file" class="lm-ellipsis fname">{{ file.name }}</span>
          <span v-else class="lm-faint">{{ $t('dataio.chooseFile') }}</span>
          <span v-if="file" class="lm-faint size">{{ formatBytes(file.size) }}</span>
          <input ref="input" type="file" accept=".sql,.gz,.sql.gz,application/sql,application/gzip" hidden @change="pick" />
        </div>
      </div>
      <div v-if="mysql" class="lm-field">
        <span class="lm-label">{{ $t('dataio.targetDb') }}<InfoTip :text="$t('dataio.targetDbHelp')" /></span>
        <select v-model="target" class="lm-select">
          <option v-if="!dbs.includes(target)" :value="target">{{ target || '—' }}</option>
          <option v-for="d in dbs" :key="d" :value="d">{{ d }}</option>
        </select>
      </div>
      <div class="lm-field">
        <span class="lm-label">{{ $t('dataio.onError') }}</span>
        <Segmented v-model="policy" :options="[{ value: 'stop', label: $t('dataio.stopOnError') }, { value: 'continue', label: $t('dataio.continueOnError') }]" class="seg" />
      </div>
    </div>

    <div v-else class="run">
      <div class="bar"><div class="fill" :class="{ indet: phase === 'running' && !prog.total }" :style="{ width: (phase === 'done' && !failMsg ? 100 : pct) + '%' }" /></div>
      <div class="stats">
        <span>{{ $t('dataio.statements', { n: (res?.statements ?? prog.statements).toLocaleString() }) }}</span>
        <span v-if="(res?.errors ?? prog.errors) > 0" class="bad">· {{ $t('dataio.errorsN', { n: res?.errors ?? prog.errors }) }}</span>
        <span v-if="res?.skipped" class="lm-faint">· {{ $t('dataio.skipped', { n: res.skipped }) }}</span>
        <span class="lm-spacer" />
        <span v-if="prog.total" class="lm-faint">{{ $t('dataio.bytesOf', { a: formatBytes(prog.bytes), b: formatBytes(prog.total) }) }}</span>
      </div>
      <div v-if="phase === 'running' && prog.table" class="lm-faint cur lm-ellipsis">{{ $t('dataio.currentTable', { t: prog.table }) }}</div>
      <div v-if="phase === 'done' && res && !failMsg" class="meta">
        <span v-if="['lemi', 'navicat', 'mysqldump'].includes(res.format)" class="lm-badge">{{ $t('dataio.sourceFormat') }} · {{ $t('dataio.formatNames.' + res.format) }}</span>
        <span v-if="!res.complete" class="lm-badge warning">{{ $t('dataio.incomplete') }}</span>
      </div>
      <div v-if="failMsg" class="fail"><Icon name="error" :size="16" /><span>{{ failMsg }}</span></div>
      <div v-if="res?.errorList?.length" class="errs">
        <table class="lm-table">
          <thead><tr><th>{{ $t('dataio.errLine') }}</th><th>{{ $t('dataio.errMsg') }}</th></tr></thead>
          <tbody>
            <tr v-for="(x, i) in res.errorList" :key="i">
              <td class="num">{{ x.line }}</td>
              <td><div class="emsg">{{ x.error }}</div><code class="snip">{{ x.snippet }}</code></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <template #footer>
      <template v-if="phase === 'form'">
        <Btn @click="close()">{{ $t('common.cancel') }}</Btn>
        <Btn variant="primary" icon="play" :disabled="!file" @click="run">{{ $t('dataio.run') }}</Btn>
      </template>
      <Btn v-else-if="phase === 'running'" variant="danger" icon="stop" @click="abort">{{ $t('dataio.cancelRun') }}</Btn>
      <Btn v-else variant="primary" @click="close(true)">{{ $t('common.done') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 16px; }
.picker { display: flex; align-items: center; gap: 10px; height: 44px; padding: 0 12px; border: 1px dashed var(--border-strong); border-radius: var(--radius-m); cursor: pointer; color: var(--text-2); min-width: 0; }
.picker:hover { border-color: var(--accent); background: var(--bg-hover); }
.fname { color: var(--text); font-weight: 500; flex: 1; min-width: 0; }
.size { font-size: 12px; flex: none; }
.seg { flex-wrap: wrap; }
.run { display: flex; flex-direction: column; gap: 10px; }
.bar { height: 6px; border-radius: 3px; background: var(--bg-active); overflow: hidden; position: relative; }
.fill { height: 100%; background: var(--accent); transition: width .2s; }
.fill.indet { width: 30% !important; animation: indet 1.2s ease-in-out infinite; }
@keyframes indet { from { transform: translateX(-100%); } to { transform: translateX(340%); } }
.stats { display: flex; align-items: center; gap: 6px; font-size: 13px; flex-wrap: wrap; }
.bad { color: var(--danger); }
.cur { font-size: 12px; }
.meta { display: flex; gap: 6px; flex-wrap: wrap; }
.fail { display: flex; gap: 8px; align-items: flex-start; padding: 10px 12px; border-radius: var(--radius-m); background: var(--danger-bg); color: var(--danger); font-size: 13px; white-space: pre-wrap; word-break: break-word; }
.errs { max-height: 280px; overflow: auto; border: 1px solid var(--border); border-radius: var(--radius-m); }
.num { font-family: var(--font-mono); font-size: 12px; color: var(--text-2); vertical-align: top; width: 1%; white-space: nowrap; }
.emsg { color: var(--danger); font-size: 12.5px; word-break: break-word; }
.snip { display: block; margin-top: 3px; font-family: var(--font-mono); font-size: 11.5px; color: var(--text-2); white-space: pre-wrap; word-break: break-all; max-height: 4.5em; overflow: hidden; }
</style>
