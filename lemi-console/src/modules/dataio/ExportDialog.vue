<script setup lang="ts">
// 导出表数据或查询结果：CSV / TSV / Excel / JSON / SQL INSERT / Markdown，由服务端流式生成一次性下载链接。
import { computed, ref } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import Switch from '@/components/ui/Switch.vue'
import TextField from '@/components/ui/TextField.vue'
import Segmented from '@/components/ui/Segmented.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import { api, downloadURL } from '@/api/client'
import type { Conn } from '@/stores/conns'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'

const props = defineProps<{ conn: Conn; database: string; table?: string; where?: string; orderBy?: string; sql?: string; onDone: (v?: boolean) => void }>()
const toast = useToast()
const t = i18n.global.t
const open = ref(true)

const formats = ['csv', 'tsv', 'xlsx', 'json', 'sql', 'markdown'] as const
type Fmt = typeof formats[number]
const format = ref<Fmt>(remembered())
const scope = ref<'filtered' | 'all'>('filtered')
const header = ref(true)
const nullText = ref('')
const sqlTable = ref(props.table || '')
const maxRows = ref('')
const filename = ref('')
const busy = ref(false)

const hasFilter = computed(() => !props.sql && !!props.table && !!(props.where?.trim() || props.orderBy?.trim()))
const title = computed(() => (props.table && !props.sql ? t('dataio.exportTitle', { name: props.table }) : t('dataio.exportResultTitle')))
const fmtOptions = computed(() => formats.map((f) => ({ value: f, label: t('dataio.formats.' + f) })))

function remembered(): Fmt {
  try {
    const v = localStorage.getItem('lemi.export.format') as Fmt
    if (formats.includes(v)) return v
  } catch {}
  return 'csv'
}

function close(v?: boolean) {
  open.value = false
  props.onDone(v)
}

async function run() {
  busy.value = true
  try { localStorage.setItem('lemi.export.format', format.value) } catch {}
  try {
    const body: Record<string, any> = { db: props.database, format: format.value, header: header.value, null_text: nullText.value }
    if (props.sql) body.sql = props.sql
    else {
      body.table = props.table
      if (scope.value === 'filtered') {
        body.where = props.where || ''
        body.order_by = props.orderBy || ''
      }
    }
    if (format.value === 'sql') body.sql_table = sqlTable.value.trim() || props.table || 'result'
    const n = parseInt(maxRows.value, 10)
    if (n > 0) body.max_rows = n
    if (filename.value.trim()) body.filename = filename.value.trim()
    const r = await api.post(`/connections/${props.conn.id}/export`, body)
    downloadURL(r.url)
    toast.success(t('dataio.exportStarted', { file: r.filename }))
    close(true)
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :open="open" :title="title" :width="520" @update:open="(v: boolean) => !v && close()">
    <div class="form">
      <div class="lm-field">
        <span class="lm-label">{{ $t('dataio.format') }}<InfoTip :text="$t('dataio.exportHint')" /></span>
        <div class="fmts">
          <button v-for="o in fmtOptions" :key="o.value" type="button" class="fmt" :class="{ on: format === o.value }" @click="format = o.value">{{ o.label }}</button>
        </div>
      </div>
      <div v-if="hasFilter" class="lm-field">
        <span class="lm-label">{{ $t('dataio.scope') }}</span>
        <Segmented v-model="scope" :options="[{ value: 'filtered', label: $t('dataio.scopeFiltered') }, { value: 'all', label: $t('dataio.scopeAll') }]" />
        <code v-if="scope === 'filtered' && where" class="cond lm-ellipsis">WHERE {{ where }}</code>
      </div>
      <label v-if="format === 'csv' || format === 'tsv' || format === 'xlsx'" class="row">
        <span>{{ $t('dataio.header') }}</span><Switch v-model="header" />
      </label>
      <div class="lm-form">
        <TextField v-if="format !== 'json' && format !== 'sql'" v-model="nullText" :label="$t('dataio.nullText')" :placeholder="$t('dataio.nullTextPh')" size="sm" mono />
        <TextField v-if="format === 'sql'" v-model="sqlTable" :label="$t('dataio.sqlTable')" size="sm" mono />
        <TextField v-model="maxRows" :label="$t('dataio.maxRows')" :placeholder="$t('dataio.maxRowsPh')" type="number" size="sm" />
        <TextField v-model="filename" :label="$t('dataio.filename')" :placeholder="$t('dataio.filenamePh')" size="sm" class="full" />
      </div>
    </div>
    <template #footer>
      <Btn @click="close()">{{ $t('common.cancel') }}</Btn>
      <Btn variant="primary" icon="download" :loading="busy" @click="run">{{ $t('common.download') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 16px; }
.fmts { display: grid; grid-template-columns: repeat(3, 1fr); gap: 6px; }
.fmt { height: 32px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); background: var(--bg); color: var(--text-2); font-size: 13px; }
.fmt:hover { background: var(--bg-hover); color: var(--text); }
.fmt.on { border-color: var(--accent); color: var(--accent); background: var(--accent-soft); font-weight: 500; }
.row { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 13px; }
.cond { display: block; margin-top: 6px; font-family: var(--font-mono); font-size: 12px; color: var(--text-2); max-width: 100%; }
</style>
