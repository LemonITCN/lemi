<script setup lang="ts">
// SQL 控制台：CodeMirror 编辑器 + 服务端会话（手动事务、取消、续取）+ 多结果标签，结果用 DataGrid 显示（单表带主键时可直接改）。
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { EditorView, keymap } from '@codemirror/view'
import { EditorState, Compartment, Prec } from '@codemirror/state'
import { format as formatSQL } from 'sql-formatter'
import DataGrid from '@/modules/grid/DataGrid.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Tab } from '@/modules/registry'
import type { QueryResult } from '@/lib/meta'
import { fetchCompletion } from '@/lib/meta'
import { editorBasics, sqlLang } from '@/lib/cm'
import { splitSQL, statementAt } from '@/lib/sqlsplit'
import { dialectOf } from '@/lib/dialect'
import { api, ApiError } from '@/api/client'
import { useConns } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { confirmDialog } from '@/lib/confirm'
import { registerTabState, unregisterTabState } from '@/stores/tabstate'
import { errorText, i18n } from '@/i18n'
import { plainText } from '@/modules/grid/model'
import ExplainTree from '@/components/code/ExplainTree.vue'
import { openExport } from '@/modules/dataio/open'

const props = defineProps<{ tab: Tab; active: boolean }>()
const conns = useConns()
const tabs = useTabs()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t

const conn = computed(() => conns.byId[props.tab.params.connectionId])
const dialect = computed(() => dialectOf(conn.value))
const database = ref<string>(props.tab.params.database || '')
const dbList = ref<string[]>([])
const sid = ref('')
const readOnly = ref(false)
const inTx = ref(false)
const running = ref(false)
const results = shallowRef<QueryResult[]>([])
const activeResult = ref(0)
const explain = ref<{ format: string; result: QueryResult } | null>(null)
const editorHost = ref<HTMLDivElement>()
const splitH = ref<number>(props.tab.params.splitH || 0.5)
const gridRef = ref<InstanceType<typeof DataGrid>>()
const changes = ref(0)
let view: EditorView | null = null
const langConf = new Compartment()

// ---------- 会话 ----------
async function openSession() {
  if (!conn.value) return
  const r = await api.post(`/connections/${conn.value.id}/sessions`, { db: database.value })
  sid.value = r.id
  readOnly.value = r.read_only
  inTx.value = false
  if (r.database && !database.value) database.value = r.database
}

async function withSession<T>(fn: () => Promise<T>): Promise<T> {
  if (!sid.value) await openSession()
  try {
    return await fn()
  } catch (e) {
    if (e instanceof ApiError && e.code === 'session_gone') {
      if (inTx.value) toast.error(t('console.txLost'))
      await openSession()
      return await fn()
    }
    throw e
  }
}

// ---------- 执行 ----------
function currentSQL(all: boolean): string {
  if (!view) return ''
  const st = view.state
  const sel = st.selection.main
  if (!all && !sel.empty) return st.sliceDoc(sel.from, sel.to)
  const doc = st.doc.toString()
  if (all) return doc
  return statementAt(doc, dialect.value, sel.head)?.text || ''
}

async function run(all = false, sqlOverride?: string) {
  const sql = sqlOverride ?? currentSQL(all)
  if (!sql.trim() || running.value) return
  const stmts = splitSQL(sql, dialect.value)
  const dangerous = stmts.some((s) => /^\s*(drop|truncate)\b/i.test(s.text) || /^\s*(update|delete)\b(?![\s\S]*\bwhere\b)/i.test(s.text))
  if (dangerous && conn.value?.env === 'prod') {
    if (!(await confirmDialog({ title: t('console.prodDanger'), message: t('console.prodDangerHint', { name: conn.value.name }), danger: true, okText: t('console.runAnyway') }))) return
  }
  running.value = true
  explain.value = null
  try {
    const r = await withSession(() => api.post(`/sessions/${sid.value}/execute`, { sql, max_rows: 500 }))
    results.value = r.results
    inTx.value = r.in_tx
    if (r.database) database.value = r.database
    activeResult.value = Math.max(0, r.results.findIndex((x: QueryResult) => x.kind === 'rows' || x.kind === 'error'))
    if (activeResult.value < 0) activeResult.value = 0
  } catch (e) {
    results.value = [{ statement: sql, kind: 'error', error: errorText(e), affected: 0, duration_ms: 0 }]
    activeResult.value = 0
  } finally {
    running.value = false
  }
  return results.value
}

async function cancel() {
  if (!sid.value) return
  try { await api.post(`/sessions/${sid.value}/cancel`) } catch (e) { toast.error(errorText(e)) }
}

async function fetchMore() {
  const r = results.value[activeResult.value]
  if (!r?.has_more) return
  try {
    const m = await withSession(() => api.post(`/sessions/${sid.value}/fetch`, { n: 1000 }))
    const next = { ...r, rows: [...(r.rows || []), ...m.rows], has_more: m.has_more }
    const list = [...results.value]
    list[activeResult.value] = next
    results.value = list
  } catch (e) { toast.error(errorText(e)) }
}

async function doExplain() {
  const sql = currentSQL(false)
  if (!sql.trim()) return
  try {
    explain.value = await withSession(() => api.post(`/sessions/${sid.value}/explain`, { sql }))
  } catch (e) { toast.error(errorText(e)) }
}

async function tx(stmt: 'BEGIN' | 'COMMIT' | 'ROLLBACK') { await run(false, stmt) }

async function useDB(db: string) {
  database.value = db
  tabs.setParams(props.tab.id, { database: db })
  tabs.update(props.tab.id, { title: `${conn.value?.name}${db ? ' · ' + db : ''}` })
  if (sid.value) {
    try { await api.post(`/sessions/${sid.value}/use`, { db }) } catch (e) { toast.error(errorText(e)) }
  }
  loadCompletion()
}

function doFormat() {
  if (!view) return
  const sel = view.state.selection.main
  const from = sel.empty ? 0 : sel.from
  const to = sel.empty ? view.state.doc.length : sel.to
  try {
    const out = formatSQL(view.state.sliceDoc(from, to), { language: dialect.value === 'sqlite' ? 'sqlite' : 'mysql', keywordCase: 'upper' })
    view.dispatch({ changes: { from, to, insert: out } })
  } catch (e: any) { toast.error(t('console.formatFailed') + e.message) }
}

async function submitResult() {
  const r = results.value[activeResult.value]
  const list = gridRef.value?.buildChanges() || []
  if (!r?.editable?.ok || !list.length) return
  try {
    const a = await api.post(`/connections/${conn.value!.id}/data/apply`, { db: r.editable.db, table: r.editable.table, changes: list, session: sid.value })
    if (a.failed_at >= 0) { toast.error(t('table.applyFailed', { n: a.failed_at + 1, err: a.error })); return }
    toast.success(t('table.applied', { n: a.applied }))
    await run(false, r.statement)
  } catch (e) { toast.error(errorText(e)) }
}

// ---------- 编辑器 ----------
let saveTimer: any
async function loadCompletion() {
  if (!conn.value || !view) return
  try {
    const db = database.value || (dialect.value === 'sqlite' ? 'main' : '')
    const schema = db ? await fetchCompletion(conn.value.id, db) : {}
    view.dispatch({ effects: langConf.reconfigure(sqlLang(dialect.value, schema)) })
  } catch {}
}

onMounted(async () => {
  view = new EditorView({
    parent: editorHost.value!,
    state: EditorState.create({
      doc: props.tab.params.sql || '',
      extensions: [
        Prec.highest(keymap.of([
          { key: 'Mod-Enter', run: () => { run(false); return true } },
          { key: 'Shift-Mod-Enter', run: () => { run(true); return true } },
          { key: 'Mod-Shift-f', run: () => { doFormat(); return true } },
        ])),
        editorBasics({ placeholder: t('console.placeholder') }),
        langConf.of(sqlLang(dialect.value)),
        EditorView.updateListener.of((u) => {
          if (!u.docChanged) return
          clearTimeout(saveTimer)
          saveTimer = setTimeout(() => tabs.setParams(props.tab.id, { sql: u.state.doc.toString() }), 400)
        }),
      ],
    }),
  })
  if (!conns.list.length && !conns.loading) await conns.load()
  if (conn.value) {
    try {
      const list = await conns.loadDatabases(conn.value.id)
      dbList.value = list.map((d) => d.name)
      if (!database.value && dialect.value === 'sqlite') database.value = 'main'
    } catch {}
    loadCompletion()
    try { await openSession() } catch (e) { toast.error(errorText(e)) }
  }
  // 由对象树「在控制台打开」或智能体带来的 SQL：自动执行
  if (props.tab.params.autorun) {
    tabs.setParams(props.tab.id, { autorun: false })
    run(true)
  }
  registerTabState(props.tab.id, () => ({
    kind: 'console', tab_id: props.tab.id, connection_id: conn.value?.id, db: database.value, sql: view?.state.doc.toString() || '', in_tx: inTx.value,
    results: results.value.map((r) => ({ kind: r.kind, statement: r.statement, columns: r.columns?.map((c) => c.name), rows_loaded: r.rows?.length || 0, affected: r.affected, error: r.error, duration_ms: r.duration_ms,
      sample: (r.rows || []).slice(0, 20).map((row) => row.map((v) => plainText(v, 'NULL'))) })),
    selection: gridRef.value?.selection(),
  }), {
    run: async (sql: string) => {
      if (view) view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: sql } })
      return run(true, sql)
    },
  })
})

onBeforeUnmount(() => {
  clearTimeout(saveTimer)
  if (view) tabs.setParams(props.tab.id, { sql: view.state.doc.toString() })
  view?.destroy()
  unregisterTabState(props.tab.id)
  if (sid.value) api.del(`/sessions/${sid.value}`).catch(() => {})
})

watch(() => ui.theme, () => view?.requestMeasure())
watch(changes, (n) => tabs.update(props.tab.id, { dirty: n > 0 }))

const cur = computed(() => results.value[activeResult.value])
const tabLabel = (r: QueryResult, i: number) => r.kind === 'rows' ? `${t('console.result')} ${i + 1}` : r.kind === 'error' ? t('console.error') : `${t('console.exec')} ${i + 1}`

// 结果区高度拖动
function startSplit(e: PointerEvent) {
  const host = (e.currentTarget as HTMLElement).parentElement!
  const rect = host.getBoundingClientRect()
  const move = (ev: PointerEvent) => { splitH.value = Math.min(0.85, Math.max(0.15, (ev.clientY - rect.top) / rect.height)) }
  const up = () => { window.removeEventListener('pointermove', move); window.removeEventListener('pointerup', up); tabs.setParams(props.tab.id, { splitH: splitH.value }) }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}
</script>

<template>
  <div class="console" :class="{ prod: conn?.env === 'prod' }">
    <div class="lm-toolbar bar">
      <Btn size="sm" variant="primary" icon="play" :disabled="running" :title="$t('console.runHint')" @click="run(false)">{{ $t('console.run') }}</Btn>
      <IconBtn icon="play-all" :label="$t('console.runAll')" :size="16" :disabled="running" @click="run(true)" />
      <IconBtn icon="stop" :label="$t('console.cancel')" :size="15" :disabled="!running" @click="cancel" />
      <IconBtn icon="explain" :label="$t('console.explain')" :size="16" @click="doExplain" />
      <IconBtn icon="format" :label="$t('console.format')" :size="16" @click="doFormat" />
      <span class="sep" />
      <select v-if="dbList.length > 1" class="dbsel" :value="database" @change="useDB(($event.target as HTMLSelectElement).value)">
        <option value="">{{ $t('console.noDb') }}</option>
        <option v-for="d in dbList" :key="d" :value="d">{{ d }}</option>
      </select>
      <template v-if="!readOnly">
        <span class="sep" />
        <Btn v-if="!inTx" size="sm" variant="ghost" icon="commit" @click="tx('BEGIN')">{{ $t('console.begin') }}</Btn>
        <template v-else>
          <span class="lm-badge warning">{{ $t('console.inTx') }}</span>
          <Btn size="sm" variant="ghost" icon="check" @click="tx('COMMIT')">{{ $t('console.commit') }}</Btn>
          <Btn size="sm" variant="ghost" icon="rollback" @click="tx('ROLLBACK')">{{ $t('console.rollback') }}</Btn>
        </template>
      </template>
      <span class="lm-spacer" />
      <span v-if="readOnly" class="lm-badge" :title="$t('console.readOnlyHint')">{{ $t('table.readOnly') }}</span>
      <span v-if="conn?.env === 'prod'" class="lm-badge danger">{{ $t('explorer.env.prod') }}</span>
    </div>
    <div class="split">
      <div ref="editorHost" class="editor" :style="{ height: splitH * 100 + '%' }" />
      <div class="splitter" @pointerdown="startSplit" />
      <div class="results">
        <div v-if="explain" class="rtabs"><button class="rt on">{{ $t('console.explain') }}</button><span class="lm-spacer" /><IconBtn icon="close" :size="14" @click="explain = null" /></div>
        <div v-else-if="results.length" class="rtabs">
          <button v-for="(r, i) in results" :key="i" class="rt" :class="{ on: i === activeResult, err: r.kind === 'error' }" @click="activeResult = i">
            <Icon :name="r.kind === 'rows' ? 'table' : r.kind === 'error' ? 'error' : 'check'" :size="13" />{{ tabLabel(r, i) }}
          </button>
        </div>
        <div class="rbody">
          <ExplainTree v-if="explain" :format="explain.format" :result="explain.result" />
          <template v-else-if="cur">
            <DataGrid v-if="cur.kind === 'rows'" ref="gridRef" :key="activeResult + ':' + cur.statement" :columns="cur.columns || []" :rows="cur.rows || []"
              :editable="!!cur.editable?.ok" :dialect="dialect" :table="cur.editable?.table" :key-cols="cur.editable?.key || []"
              @changed="(n: number) => (changes = n)" @submit="submitResult" />
            <div v-else class="msg" :class="{ bad: cur.kind === 'error' }">
              <code class="stmt">{{ cur.statement.length > 400 ? cur.statement.slice(0, 400) + '…' : cur.statement }}</code>
              <p v-if="cur.kind === 'error'">{{ cur.error }}</p>
              <p v-else>{{ $t('console.affected', { n: cur.affected }) }}<span v-if="cur.last_insert_id"> · LAST_INSERT_ID = {{ cur.last_insert_id }}</span></p>
            </div>
          </template>
          <div v-else class="hint">
            <p>{{ $t('console.emptyHint') }}</p>
            <p class="keys"><span class="lm-kbd">⌘/Ctrl + Enter</span> {{ $t('console.run') }} · <span class="lm-kbd">⌘/Ctrl + Shift + Enter</span> {{ $t('console.runAll') }}</p>
          </div>
        </div>
        <div v-if="cur && !explain" class="rstatus">
          <span v-if="cur.kind === 'rows'">{{ cur.rows?.length || 0 }}{{ cur.has_more ? '+' : '' }} {{ $t('table.rows') }}</span>
          <button v-if="cur.kind === 'rows' && cur.has_more" class="link" @click="fetchMore">{{ $t('console.fetchMore') }}</button>
          <span>· {{ cur.duration_ms }} ms</span>
          <span v-if="cur.kind === 'rows' && !cur.editable?.ok && cur.editable?.reason" class="lm-faint lm-ellipsis">· {{ cur.editable.reason }}</span>
          <span class="lm-spacer" />
          <IconBtn v-if="cur.kind === 'rows' && conn" icon="download" :label="$t('table.export')" :size="15" @click="openExport({ conn: conn!, database, sql: cur.statement })" />
          <Btn v-if="changes" size="sm" variant="primary" icon="check" @click="submitResult">{{ $t('table.submit') }} {{ changes }}</Btn>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.console { flex: 1; display: flex; flex-direction: column; min-height: 0; }
.console.prod { box-shadow: inset 0 2px 0 var(--danger); }
.bar { gap: 4px; }
.dbsel { height: 26px; border: 1px solid var(--border); border-radius: 5px; background: var(--bg); font-size: 12.5px; max-width: 200px; }
.split { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.editor { min-height: 60px; overflow: hidden; }
.editor :deep(.cm-editor) { height: 100%; }
.splitter { height: 5px; flex: none; cursor: row-resize; background: var(--bg-soft); border-top: 1px solid var(--border); border-bottom: 1px solid var(--border); }
.results { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.rtabs { height: 30px; flex: none; display: flex; align-items: stretch; border-bottom: 1px solid var(--border); background: var(--bg-soft); overflow-x: auto; }
.rt { display: flex; align-items: center; gap: 5px; padding: 0 12px; border: 0; border-right: 1px solid var(--border); background: none; color: var(--text-2); font-size: 12px; white-space: nowrap; }
.rt.on { background: var(--bg); color: var(--text); }
.rt.err { color: var(--danger); }
.rbody { flex: 1; min-height: 0; display: flex; flex-direction: column; position: relative; }
.msg { margin: 12px; padding: 12px 14px; border-radius: var(--radius-m); background: var(--bg-soft); font-size: 13px; overflow: auto; }
.msg.bad { background: var(--danger-bg); color: var(--danger); }
.msg p { margin: 8px 0 0; white-space: pre-wrap; font-family: var(--font-mono); font-size: 12.5px; }
.msg.mono { font-family: var(--font-mono); font-size: 12px; white-space: pre; flex: 1; }
.stmt { display: block; font-family: var(--font-mono); font-size: 12px; color: var(--text-2); white-space: pre-wrap; }
.hint { margin: auto; text-align: center; color: var(--text-3); font-size: 12.5px; }
.keys { margin-top: 6px; }
.rstatus { height: 30px; flex: none; display: flex; align-items: center; gap: 6px; padding: 0 8px 0 12px; border-top: 1px solid var(--border); font-size: 12px; color: var(--text-2); background: var(--bg-soft); }
.link { border: 0; background: none; color: var(--accent); padding: 0; font-size: 12px; }
</style>
