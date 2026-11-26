<script setup lang="ts">
// 把 CSV / TSV / Excel 导入到表：上传预览 → 列映射与选项 → 执行（分批事务，可整体回滚或跳过出错的行）。
import { computed, onMounted, ref } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import Switch from '@/components/ui/Switch.vue'
import Segmented from '@/components/ui/Segmented.vue'
import TextField from '@/components/ui/TextField.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import { api, ApiError } from '@/api/client'
import type { Conn } from '@/stores/conns'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { fetchTable, formatBytes, type ColumnInfo } from '@/lib/meta'
import { autoMapping } from './mapping'
import { errorText, i18n } from '@/i18n'

const props = defineProps<{ conn: Conn; database: string; table: string; onDone: (v?: { inserted: number }) => void }>()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t
const open = ref(true)

const step = ref<1 | 2 | 3>(1)
const columns = ref<ColumnInfo[]>([])
const file = ref<File | null>(null)
const input = ref<HTMLInputElement>()
const uploading = ref(false)
const pv = ref<{ import_id: string; format: string; sheets?: string[]; sample: string[][] } | null>(null)
const sheet = ref('')
const header = ref(true)
const mapping = ref<Record<string, number>>({})
const emptyAsNull = ref(true)
const nullText = ref('')
const skipErrors = ref(false)
const busy = ref(false)
const result = ref<{ inserted: number; failed: number; errors: string[]; duration_ms: number } | null>(null)
const err = ref('')

onMounted(async () => {
  try { columns.value = (await fetchTable(props.conn.id, props.database, props.table)).columns } catch (e) { err.value = errorText(e) }
})

const fileCols = computed(() => {
  const s = pv.value?.sample || []
  const n = s.reduce((m, r) => Math.max(m, r.length), 0)
  return Array.from({ length: n }, (_, i) => ({ i, label: header.value && s[0]?.[i] ? s[0][i] : t('dataio.colN', { n: i + 1 }) }))
})
const sampleRows = computed(() => (pv.value?.sample || []).slice(header.value ? 1 : 0, (header.value ? 1 : 0) + 8))
const mappedCount = computed(() => Object.values(mapping.value).filter((i) => i >= 0).length)

function pick(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (f) { file.value = f; upload() }
  if (input.value) input.value.value = ''
}

async function upload() {
  if (!file.value) return
  uploading.value = true
  err.value = ''
  try {
    const fd = new FormData()
    if (sheet.value) fd.append('sheet', sheet.value)
    fd.append('file', file.value, file.value.name)
    pv.value = await api.post(`/connections/${props.conn.id}/import/preview`, fd)
    if (!sheet.value && pv.value?.sheets?.length) sheet.value = pv.value.sheets[0]
    remap()
  } catch (e) {
    pv.value = null
    err.value = errorText(e)
  } finally {
    uploading.value = false
  }
}

function remap() {
  const s = pv.value?.sample || []
  mapping.value = autoMapping(columns.value.filter((c) => !c.generated).map((c) => c.name), header.value ? s[0] || [] : [], fileCols.value.length, header.value)
}

async function changeSheet(v: string) {
  sheet.value = v
  await upload()
}

function close(v?: { inserted: number }) {
  if (busy.value) return
  open.value = false
  props.onDone(v)
}

async function run() {
  if (!pv.value) return
  if (!mappedCount.value) { toast.error(t('dataio.noMapping')); return }
  busy.value = true
  err.value = ''
  try {
    const m: Record<string, number> = {}
    for (const [k, v] of Object.entries(mapping.value)) if (v >= 0) m[k] = v
    result.value = await api.post(`/connections/${props.conn.id}/import/run`, {
      import_id: pv.value.import_id, db: props.database, table: props.table, sheet: sheet.value, header: header.value, mapping: m,
      empty_as_null: emptyAsNull.value, null_text: nullText.value, skip_errors: skipErrors.value,
    })
    step.value = 3
  } catch (e) {
    err.value = errorText(e)
    if (e instanceof ApiError && e.code === 'import_expired') { pv.value = null; step.value = 1 }
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :open="open" :title="$t('dataio.importTitle', { name: table })" :width="ui.isMobile ? '100%' : 760" @update:open="(v: boolean) => !v && close(result ? { inserted: result.inserted } : undefined)">
    <div class="steps">
      <span v-for="(s, i) in ['stepFile', 'stepMap', 'stepDone']" :key="s" class="st" :class="{ on: step === i + 1, done: step > i + 1 }">
        <span class="n">{{ step > i + 1 ? '' : i + 1 }}<Icon v-if="step > i + 1" name="check" :size="11" /></span>{{ $t('dataio.' + s) }}
      </span>
    </div>

    <div v-if="step === 1" class="pane">
      <div class="lm-field">
        <span class="lm-label">{{ $t('dataio.file') }}<InfoTip :text="$t('dataio.importFileTypes')" /></span>
        <div class="picker" @click="input?.click()">
          <Icon name="upload" :size="18" />
          <span v-if="file" class="lm-ellipsis fname">{{ file.name }}</span>
          <span v-else class="lm-faint">{{ $t('dataio.chooseFile') }}</span>
          <span v-if="file" class="lm-faint size">{{ formatBytes(file.size) }}</span>
          <span v-if="uploading" class="spin" />
          <input ref="input" type="file" accept=".csv,.tsv,.txt,.xlsx" hidden @change="pick" />
        </div>
      </div>
      <template v-if="pv">
        <div class="opts">
          <label class="row"><span>{{ $t('dataio.hasHeader') }}</span><Switch v-model="header" @update:model-value="remap" /></label>
          <label v-if="pv.sheets?.length" class="row"><span>{{ $t('dataio.sheet') }}</span>
            <select class="lm-select sheet" :value="sheet" @change="changeSheet(($event.target as HTMLSelectElement).value)"><option v-for="s in pv.sheets" :key="s" :value="s">{{ s }}</option></select>
          </label>
        </div>
        <div class="lm-label">{{ $t('dataio.preview', { n: sampleRows.length }) }}</div>
        <div class="pvwrap">
          <table class="pvt">
            <thead><tr><th v-for="c in fileCols" :key="c.i">{{ c.label }}</th></tr></thead>
            <tbody><tr v-for="(r, ri) in sampleRows" :key="ri"><td v-for="c in fileCols" :key="c.i">{{ r[c.i] ?? '' }}</td></tr></tbody>
          </table>
        </div>
      </template>
    </div>

    <div v-else-if="step === 2" class="pane">
      <div class="map-head">
        <span class="lm-label">{{ $t('dataio.mapping') }}</span>
        <span class="lm-spacer" />
        <button class="link" type="button" @click="remap">{{ $t('dataio.autoMap') }}</button>
      </div>
      <div class="map">
        <div v-for="c in columns" :key="c.name" class="mrow">
          <div class="mcol">
            <span class="lm-ellipsis cname">{{ c.name }}</span>
            <span class="lm-faint ctype lm-ellipsis">{{ c.type }}<template v-if="c.generated"> · {{ $t('dataio.generated') }}</template></span>
          </div>
          <Icon name="arrow-left" :size="14" class="arr" />
          <select v-model.number="mapping[c.name]" class="lm-select msel" :disabled="!!c.generated">
            <option :value="-1">{{ $t('dataio.skipColumn') }}</option>
            <option v-for="f in fileCols" :key="f.i" :value="f.i">{{ f.label }}</option>
          </select>
        </div>
      </div>
      <div class="opts">
        <label class="row"><span>{{ $t('dataio.emptyAsNull') }}</span><Switch v-model="emptyAsNull" /></label>
        <TextField v-model="nullText" :label="$t('dataio.nullMarker')" :placeholder="$t('dataio.nullMarkerPh')" size="sm" mono />
        <div class="lm-field">
          <span class="lm-label">{{ $t('dataio.errorPolicy') }}</span>
          <Segmented :model-value="skipErrors ? 'skip' : 'rollback'" :options="[{ value: 'rollback', label: $t('dataio.rollbackAll') }, { value: 'skip', label: $t('dataio.skipBadRows') }]" @update:model-value="(v: string) => (skipErrors = v === 'skip')" />
        </div>
      </div>
    </div>

    <div v-else class="pane">
      <div v-if="result" class="done">
        <Icon :name="result.failed ? 'warning' : 'success'" :size="22" :class="result.failed ? 'warn' : 'ok'" />
        <div>
          <div class="big">{{ result.failed ? $t('dataio.importedWithFailed', { n: result.inserted, f: result.failed }) : $t('dataio.imported', { n: result.inserted }) }}</div>
          <div class="lm-faint">{{ $t('dataio.duration', { ms: result.duration_ms }) }}</div>
        </div>
      </div>
      <template v-if="result?.errors?.length">
        <div class="lm-label">{{ $t('dataio.importErrors') }}</div>
        <div class="elist"><div v-for="(e, i) in result.errors" :key="i" class="eline">{{ e }}</div></div>
      </template>
    </div>

    <div v-if="err" class="fail"><Icon name="error" :size="16" /><span>{{ err }}</span></div>

    <template #footer>
      <template v-if="step === 1">
        <Btn @click="close()">{{ $t('common.cancel') }}</Btn>
        <Btn variant="primary" :disabled="!pv || uploading" @click="step = 2">{{ $t('common.next') }}</Btn>
      </template>
      <template v-else-if="step === 2">
        <Btn :disabled="busy" @click="step = 1">{{ $t('common.back') }}</Btn>
        <Btn variant="primary" icon="upload" :loading="busy" :disabled="!mappedCount" @click="run">{{ busy ? $t('dataio.importing') : $t('dataio.doImport') }}</Btn>
      </template>
      <Btn v-else variant="primary" @click="close({ inserted: result?.inserted || 0 })">{{ $t('common.done') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.steps { display: flex; gap: 18px; margin-bottom: 16px; font-size: 12.5px; color: var(--text-3); flex-wrap: wrap; }
.st { display: inline-flex; align-items: center; gap: 6px; }
.st.on { color: var(--text); font-weight: 500; }
.st.done { color: var(--text-2); }
.n { width: 18px; height: 18px; border-radius: 50%; display: inline-flex; align-items: center; justify-content: center; font-size: 11px; border: 1px solid var(--border-strong); }
.st.on .n { background: var(--accent); border-color: var(--accent); color: #fff; }
.st.done .n { background: var(--accent-soft); border-color: transparent; color: var(--accent); }
.pane { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
.picker { display: flex; align-items: center; gap: 10px; height: 44px; padding: 0 12px; border: 1px dashed var(--border-strong); border-radius: var(--radius-m); cursor: pointer; color: var(--text-2); min-width: 0; }
.picker:hover { border-color: var(--accent); background: var(--bg-hover); }
.fname { color: var(--text); font-weight: 500; flex: 1; min-width: 0; }
.size { font-size: 12px; flex: none; }
.opts { display: flex; flex-direction: column; gap: 12px; }
.row { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 13px; }
.sheet { width: auto; max-width: 60%; height: 30px; }
.pvwrap { overflow: auto; max-height: 240px; border: 1px solid var(--border); border-radius: var(--radius-m); }
.pvt { border-collapse: collapse; font-size: 12px; font-family: var(--font-mono); }
.pvt th, .pvt td { padding: 4px 8px; border-bottom: 1px solid var(--border); border-right: 1px solid var(--border); white-space: nowrap; max-width: 220px; overflow: hidden; text-overflow: ellipsis; text-align: left; }
.pvt th { background: var(--bg-soft); color: var(--text-2); font-weight: 500; position: sticky; top: 0; }
.map-head { display: flex; align-items: center; }
.map-head .lm-label { margin: 0; }
.link { border: 0; background: none; color: var(--accent); padding: 0; font-size: 12.5px; }
.map { border: 1px solid var(--border); border-radius: var(--radius-m); max-height: 300px; overflow: auto; }
.mrow { display: grid; grid-template-columns: minmax(0, 1fr) 16px minmax(0, 1fr); align-items: center; gap: 10px; padding: 6px 10px; border-bottom: 1px solid var(--border); }
.mrow:last-child { border-bottom: 0; }
.mcol { display: flex; flex-direction: column; min-width: 0; }
.cname { font-family: var(--font-mono); font-size: 12.5px; }
.ctype { font-size: 11px; }
.arr { color: var(--text-3); }
.msel { height: 30px; font-size: 12.5px; }
.done { display: flex; align-items: center; gap: 12px; padding: 8px 0; }
.big { font-size: 15px; font-weight: 600; }
.ok { color: var(--success); }
.warn { color: var(--warning); }
.elist { max-height: 200px; overflow: auto; border: 1px solid var(--border); border-radius: var(--radius-m); padding: 6px 10px; font-family: var(--font-mono); font-size: 12px; color: var(--danger); }
.eline { padding: 2px 0; word-break: break-word; }
.fail { margin-top: 12px; display: flex; gap: 8px; align-items: flex-start; padding: 10px 12px; border-radius: var(--radius-m); background: var(--danger-bg); color: var(--danger); font-size: 13px; white-space: pre-wrap; word-break: break-word; }
.spin { width: 14px; height: 14px; border-radius: 50%; border: 2px solid var(--accent); border-right-color: transparent; animation: spin .7s linear infinite; flex: none; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
