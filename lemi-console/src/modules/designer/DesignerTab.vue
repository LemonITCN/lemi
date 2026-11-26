<script setup lang="ts">
// 表结构设计器：读表结构成草稿，分区编辑列 / 索引 / 外键 / 表选项；后端按差异生成 DDL，执行前预览。
import { computed, reactive, ref, watch } from 'vue'
import type { Tab } from '@/modules/registry'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import Modal from '@/components/ui/Modal.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import Segmented from '@/components/ui/Segmented.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import TextField from '@/components/ui/TextField.vue'
import SqlView from '@/components/code/SqlView.vue'
import ColumnsPane from './ColumnsPane.vue'
import IndexesPane from './IndexesPane.vue'
import FksPane from './FksPane.vue'
import { api } from '@/api/client'
import { useConns, openers } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { dialectOf } from '@/lib/dialect'
import { fetchTable, type QueryResult, type TableInfo } from '@/lib/meta'
import { confirmDialog } from '@/lib/confirm'
import { copyText } from '@/lib/cm'
import { errorText, i18n } from '@/i18n'
import {
  MYSQL_CHARSETS, MYSQL_COLLATIONS, MYSQL_ENGINES, MYSQL_TYPES, SQLITE_TYPES,
  designFromDraft, draftFromTable, newDraft, validate, type Draft, type Problem,
} from './design'

type Section = 'columns' | 'indexes' | 'fks' | 'options'

const props = defineProps<{ tab: Tab; active: boolean }>()
const conns = useConns()
const tabs = useTabs()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t

const conn = computed(() => conns.byId[props.tab.params.connectionId])
const connMissing = computed(() => !conn.value && !conns.loading && conns.list.length > 0)
const db = computed(() => String(props.tab.params.database || ''))
const tableName = computed(() => String(props.tab.params.table || ''))
const isNew = computed(() => !tableName.value)
const dialect = computed(() => dialectOf(conn.value))
const isMy = computed(() => dialect.value === 'mysql')
const canExec = computed(() => !!conn.value && conn.value.access !== 'read' && !conn.value.read_only)
const isProd = computed(() => conn.value?.env === 'prod')
const uid = 'dz-' + props.tab.id

const draft = ref<Draft | null>(null)
const info = ref<TableInfo | null>(null)
const loading = ref(false)
const error = ref('')
const problems = ref<Problem[]>([])
let snapshot = ''
let origAutoInc = ''
let loaded = false

const section = ref<Section>((props.tab.params.section as Section) || 'columns')
watch(section, (s) => tabs.setParams(props.tab.id, { section: s }))

const sections = computed(() => [
  { value: 'columns', label: t('designer.sec.columns') },
  { value: 'indexes', label: t('designer.sec.indexes') },
  { value: 'fks', label: t('designer.sec.fks') },
  { value: 'options', label: t('designer.sec.options') },
])

const tables = computed(() => (conns.objects[props.tab.params.connectionId + '\u0000' + db.value]?.tables || []).map((o) => o.name))
const types = computed(() => (isMy.value ? MYSQL_TYPES : SQLITE_TYPES))
const collFor = (cs: string) => MYSQL_COLLATIONS.filter((c) => c === cs || c.startsWith(cs + '_'))

const dirty = ref(false)
watch(draft, (d) => {
  const v = !!d && JSON.stringify(d) !== snapshot
  dirty.value = v
  if (!!props.tab.dirty !== v) tabs.update(props.tab.id, { dirty: v })
  if (d && problems.value.length) problems.value = validate(d, dialect.value)
}, { deep: true })

async function load() {
  const c = conn.value
  if (!c) return
  loading.value = true
  error.value = ''
  try {
    if (tableName.value) {
      const ti = await fetchTable(c.id, db.value, tableName.value)
      info.value = ti
      draft.value = draftFromTable(ti, dialect.value)
    } else {
      info.value = null
      draft.value = newDraft(dialect.value)
    }
    origAutoInc = draft.value.opts.auto_increment
    snapshot = JSON.stringify(draft.value)
    problems.value = []
    loaded = true
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
  conns.loadObjects(c.id, db.value).catch(() => {})
}

watch(conn, (c) => { if (c && !loaded && !loading.value) load() }, { immediate: true })

async function reload() {
  if (dirty.value && !(await confirmDialog({ title: t('designer.discardTitle'), message: t('designer.discardHint'), danger: true, okText: t('designer.discard') }))) return
  load()
}

async function reset() {
  if (!(await confirmDialog({ title: t('designer.discardTitle'), message: t('designer.discardHint'), danger: true, okText: t('designer.discard') }))) return
  draft.value = JSON.parse(snapshot)
  problems.value = []
}

// ---------- 预览与执行 ----------
const preview = reactive({ open: false, title: '', statements: [] as string[], execError: '', executing: false })
const previewing = ref(false)
const sql = computed(() => preview.statements.map((s) => s.trim().replace(/;\s*$/, '') + ';').join('\n\n'))

function payload(execute: boolean) {
  return { db: db.value, table: tableName.value, design: designFromDraft(draft.value!, dialect.value, origAutoInc), execute }
}

async function openPreview(forExec: boolean) {
  const c = conn.value
  if (!c || !draft.value) return
  problems.value = validate(draft.value, dialect.value)
  if (problems.value.length) {
    section.value = problems.value[0].section
    return
  }
  previewing.value = true
  try {
    const r = await api.post(`/connections/${c.id}/designer`, payload(false))
    const st: string[] = r.statements || []
    if (forExec && !st.length) {
      toast.info(t('designer.noChanges'))
      return
    }
    Object.assign(preview, { open: true, title: forExec ? t('designer.confirmExec') : t('designer.previewSql'), statements: st, execError: '' })
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    previewing.value = false
  }
}

async function execute() {
  const c = conn.value
  if (!c || !draft.value || !canExec.value) return
  if (isProd.value && !(await confirmDialog({ title: t('designer.prodTitle'), message: t('designer.prodHint', { name: c.name }), danger: true, okText: t('designer.execute') }))) return
  preview.executing = true
  preview.execError = ''
  const body = payload(true)
  try {
    const r = await api.post(`/connections/${c.id}/designer`, body)
    const results: QueryResult[] = r.results || []
    const bad = results.findIndex((x) => x.kind === 'error')
    conns.loadObjects(c.id, db.value, true).catch(() => {})
    if (bad >= 0) {
      preview.execError = t('designer.execFailed', { n: bad + 1, error: results[bad].error || '' })
      return
    }
    toast.success(t('designer.execOk'))
    preview.open = false
    const newName = body.design.name
    if (newName && newName !== tableName.value) {
      tabs.setParams(props.tab.id, { table: newName })
      tabs.update(props.tab.id, { title: newName, key: `designer:${c.id}:${db.value}:${newName}` })
    }
    await load()
  } catch (e) {
    preview.execError = errorText(e)
  } finally {
    preview.executing = false
  }
}

async function copySql() { if (await copyText(sql.value)) toast.success(t('common.copied')) }
function toConsole() {
  if (conn.value) openers.console(tabs, conn.value, db.value, { sql: sql.value })
  preview.open = false
}

// 当前分区的「新增」按钮
const colsPane = ref<InstanceType<typeof ColumnsPane>>()
const idxPane = ref<InstanceType<typeof IndexesPane>>()
const fkPane = ref<InstanceType<typeof FksPane>>()
const addLabel = computed(() => ({ columns: t('designer.addColumn'), indexes: t('designer.addIndex'), fks: t('designer.addFk'), options: '' })[section.value])
function addCurrent() {
  if (section.value === 'columns') colsPane.value?.add()
  else if (section.value === 'indexes') idxPane.value?.add()
  else if (section.value === 'fks') fkPane.value?.add()
}

const mobileMenu = computed<MenuItem[]>(() => [
  { label: t('designer.previewSql'), icon: 'code', disabled: !draft.value, run: () => openPreview(false) },
  { label: t('common.refresh'), icon: 'refresh', disabled: isNew.value, run: reload },
  { label: t('designer.resetChanges'), icon: 'undo', disabled: !dirty.value, run: reset },
])

const title = computed(() => tableName.value || draft.value?.opts.name || t('designer.newTable'))

function openDdl() {
  const c = conn.value
  if (!c) return
  tabs.open('ddl', { connectionId: c.id, database: db.value, kind: 'view', name: tableName.value }, { title: tableName.value, key: `ddl:${c.id}:${db.value}:view:${tableName.value}`, connectionId: c.id })
}

function problemText(p: Problem) { return t(p.key, p.params || {}) }
</script>

<template>
  <div class="designer">
    <div class="lm-toolbar">
      <Icon name="table" :size="16" class="ticon" />
      <span class="title lm-ellipsis" :title="title">{{ title }}</span>
      <span v-if="isNew" class="lm-badge accent">{{ $t('designer.newBadge') }}</span>
      <span v-if="dirty" class="lm-badge warning">{{ $t('designer.unsaved') }}</span>
      <span v-if="conn && !canExec" class="lm-badge">{{ $t('designer.readOnly') }}</span>
      <span class="lm-spacer" />
      <template v-if="!ui.isMobile">
        <IconBtn icon="refresh" :size="16" :label="$t('common.refresh')" :disabled="loading || isNew || !conn" @click="reload" />
        <Btn v-if="dirty" size="sm" variant="ghost" icon="undo" @click="reset">{{ $t('designer.resetChanges') }}</Btn>
        <Btn size="sm" icon="code" :disabled="!draft" :loading="previewing" @click="openPreview(false)">{{ $t('designer.previewSql') }}</Btn>
        <Btn v-if="canExec" size="sm" variant="primary" icon="play" :disabled="!draft || previewing" @click="openPreview(true)">{{ $t('designer.execute') }}</Btn>
      </template>
      <template v-else>
        <Btn v-if="canExec" size="sm" variant="primary" :disabled="!draft" :loading="previewing" @click="openPreview(true)">{{ $t('designer.execute') }}</Btn>
        <Menu :items="mobileMenu" align="end"><IconBtn icon="more" :size="16" :label="$t('common.more')" /></Menu>
      </template>
    </div>

    <div v-if="draft && info?.kind !== 'view'" class="subbar">
      <div class="seg-wrap"><Segmented v-model="section" :options="sections" /></div>
      <span class="lm-spacer" />
      <IconBtn v-if="addLabel && ui.isMobile" icon="plus" :size="17" :label="addLabel" @click="addCurrent" />
      <Btn v-else-if="addLabel" size="sm" variant="ghost" icon="plus" @click="addCurrent">{{ addLabel }}</Btn>
    </div>

    <div class="body">
      <div v-if="connMissing" class="state"><Icon name="plug" :size="28" :stroke="1.3" /><p>{{ $t('designer.connMissing') }}</p></div>
      <div v-else-if="error" class="state">
        <Icon name="error" :size="28" :stroke="1.3" class="err-icon" /><p class="err">{{ error }}</p>
        <Btn size="sm" icon="refresh" @click="load">{{ $t('common.retry') }}</Btn>
      </div>
      <div v-else-if="!draft" class="state"><span class="spin" /><p>{{ $t('common.loading') }}</p></div>
      <div v-else-if="info?.kind === 'view'" class="state">
        <Icon name="view" :size="28" :stroke="1.3" /><p>{{ $t('designer.isView') }}</p>
        <Btn size="sm" icon="code" @click="openDdl">{{ $t('designer.viewDdl') }}</Btn>
      </div>
      <div v-else class="content" :class="{ dim: loading }">
        <div v-if="problems.length" class="problems" role="alert">
          <Icon name="warning" :size="16" />
          <ul>
            <li v-for="(p, i) in problems.slice(0, 6)" :key="i"><button type="button" class="link" @click="section = p.section">{{ problemText(p) }}</button></li>
            <li v-if="problems.length > 6" class="lm-faint">{{ $t('designer.moreProblems', { n: problems.length - 6 }) }}</li>
          </ul>
        </div>

        <ColumnsPane v-if="section === 'columns'" ref="colsPane" :draft="draft" :dialect="dialect" :mobile="ui.isMobile" :uid="uid" />
        <IndexesPane v-else-if="section === 'indexes'" ref="idxPane" :draft="draft" :dialect="dialect" :mobile="ui.isMobile" />
        <FksPane v-else-if="section === 'fks'" ref="fkPane" :draft="draft" :dialect="dialect" :mobile="ui.isMobile" :conn-id="tab.params.connectionId"
          :database="db" :orig-table="tableName" :tables="tables" />
        <div v-else class="opts">
          <div class="lm-form">
            <TextField v-model="draft.opts.name" :label="$t('designer.opt.name')" mono />
            <template v-if="isMy">
              <label class="lm-field"><span class="lm-label">{{ $t('designer.opt.engine') }}</span>
                <input v-model="draft.opts.engine" class="inp" :list="uid + '-eng'" spellcheck="false" /></label>
              <TextField v-model="draft.opts.comment" :label="$t('designer.opt.comment')" class="full" />
              <label class="lm-field"><span class="lm-label">{{ $t('designer.opt.charset') }}</span>
                <input v-model="draft.opts.charset" class="inp mono" :list="uid + '-cs'" spellcheck="false" /></label>
              <label class="lm-field"><span class="lm-label">{{ $t('designer.opt.collation') }}<InfoTip :text="$t('designer.collationTip')" /></span>
                <input v-model="draft.opts.collation" class="inp mono" :list="MYSQL_CHARSETS.includes(draft.opts.charset) ? `${uid}-coll-${draft.opts.charset}` : uid + '-coll'" spellcheck="false" /></label>
              <label class="lm-field"><span class="lm-label">{{ $t('designer.opt.autoInc') }}<InfoTip :text="$t('designer.autoIncTip')" /></span>
                <input v-model="draft.opts.auto_increment" class="inp" inputmode="numeric" /></label>
            </template>
            <p v-else class="full lm-faint note">{{ $t('designer.sqliteOptions') }}</p>
          </div>
        </div>
      </div>
    </div>

    <datalist :id="uid + '-types'"><option v-for="x in types" :key="x" :value="x" /></datalist>
    <template v-if="isMy">
      <datalist :id="uid + '-eng'"><option v-for="x in MYSQL_ENGINES" :key="x" :value="x" /></datalist>
      <datalist :id="uid + '-cs'"><option v-for="x in MYSQL_CHARSETS" :key="x" :value="x" /></datalist>
      <datalist :id="uid + '-onupd'"><option v-for="x in ['CURRENT_TIMESTAMP', 'CURRENT_TIMESTAMP(3)', 'CURRENT_TIMESTAMP(6)']" :key="x" :value="x" /></datalist>
      <datalist :id="uid + '-coll'"><option v-for="x in MYSQL_COLLATIONS" :key="x" :value="x" /></datalist>
      <datalist v-for="cs in MYSQL_CHARSETS" :id="`${uid}-coll-${cs}`" :key="cs"><option v-for="x in collFor(cs)" :key="x" :value="x" /></datalist>
    </template>

    <Modal v-model:open="preview.open" :title="preview.title" :width="760">
      <div v-if="!preview.statements.length" class="lm-empty">{{ $t('designer.noChanges') }}</div>
      <template v-else>
        <p v-if="!isMy && preview.statements.some((s) => /^BEGIN\b/i.test(s.trim()))" class="hint"><Icon name="info" :size="14" />{{ $t('designer.rebuildHint') }}</p>
        <SqlView :code="sql" :dialect="dialect" max-height="50vh" />
      </template>
      <p v-if="preview.execError" class="exec-err" role="alert">{{ preview.execError }}</p>
      <template #footer>
        <Btn v-if="!ui.isMobile && preview.statements.length" variant="ghost" icon="terminal" @click="toConsole">{{ $t('designer.toConsole') }}</Btn>
        <Btn v-if="preview.statements.length" variant="ghost" icon="copy" @click="copySql">{{ $t('common.copy') }}</Btn>
        <span class="lm-spacer" />
        <Btn @click="preview.open = false">{{ $t('common.cancel') }}</Btn>
        <Btn v-if="canExec && preview.statements.length" :variant="isProd ? 'danger' : 'primary'" icon="play" :loading="preview.executing" @click="execute">{{ $t('designer.execute') }}</Btn>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
.designer { display: flex; flex-direction: column; flex: 1; min-height: 0; height: 100%; min-width: 0; }
.ticon { color: var(--text-2); margin: 0 2px 0 4px; }
.title { font-weight: 500; font-size: 13px; min-width: 0; }
.subbar { flex: none; display: flex; align-items: center; gap: 8px; padding: 6px 12px; border-bottom: 1px solid var(--border); min-width: 0; }
.seg-wrap { min-width: 0; overflow-x: auto; scrollbar-width: none; }
.seg-wrap::-webkit-scrollbar { display: none; }
.body { flex: 1; min-height: 0; position: relative; overflow: auto; }
.content { padding: 12px 16px 40px; min-width: 0; transition: opacity .15s; }
.content.dim { opacity: .55; pointer-events: none; }
.problems { display: flex; gap: 8px; align-items: flex-start; background: var(--danger-bg); color: var(--danger); border-radius: var(--radius-m); padding: 8px 12px; margin-bottom: 12px; font-size: 13px; }
.problems ul { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.problems .link { border: 0; background: none; padding: 0; color: inherit; text-align: left; word-break: break-word; }
.problems .link:hover { text-decoration: underline; }
.opts { max-width: 720px; }
.inp { height: 34px; padding: 0 10px; border-radius: var(--radius-m); border: 1px solid var(--border-strong); background: var(--bg); outline: none; width: 100%; min-width: 0; }
.inp:focus { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
.inp.mono { font-family: var(--font-mono); font-size: 13px; }
.note { margin: 0; font-size: 13px; }
.state { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px; color: var(--text-3); padding: 24px; text-align: center; }
.state p { margin: 0; font-size: 13px; max-width: 520px; word-break: break-word; }
.state .err { color: var(--danger); }
.err-icon { color: var(--danger); }
.hint { display: flex; align-items: center; gap: 6px; margin: 0 0 8px; font-size: 12.5px; color: var(--text-2); }
.exec-err { margin: 10px 0 0; padding: 8px 10px; border-radius: var(--radius-m); background: var(--danger-bg); color: var(--danger); font-size: 13px; white-space: pre-wrap; word-break: break-word; }
.spin { width: 18px; height: 18px; border-radius: 50%; border: 2px solid var(--border-strong); border-right-color: transparent; animation: spin .7s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (max-width: 760px) { .content { padding: 10px 10px 40px; } .subbar { padding: 6px 10px; } }
</style>
