<script setup lang="ts">
// 表格页：打开一张表的数据，WHERE / ORDER BY 筛选、翻页、像 Excel 一样编辑后一次提交。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import DataGrid from '@/modules/grid/DataGrid.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import Modal from '@/components/ui/Modal.vue'
import Icon from '@/components/icons/Icon.vue'
import SqlView from '@/components/code/SqlView.vue'
import type { Tab } from '@/modules/registry'
import type { Cell, QueryResult, TableInfo } from '@/lib/meta'
import { api } from '@/api/client'
import { useConns, openers } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { dialectOf } from '@/lib/dialect'
import { quoteIdent } from '@/lib/sql'
import { confirmDialog } from '@/lib/confirm'
import { registerTabState, unregisterTabState } from '@/stores/tabstate'
import { errorText, i18n } from '@/i18n'
import { plainText } from '@/modules/grid/model'

const props = defineProps<{ tab: Tab; active: boolean }>()
const conns = useConns()
const tabs = useTabs()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t

const conn = computed(() => conns.byId[props.tab.params.connectionId])
const dialect = computed(() => dialectOf(conn.value))
const db = computed<string>(() => props.tab.params.database)
const table = computed<string>(() => props.tab.params.table)

const where = ref<string>(props.tab.params.where || '')
const orderBy = ref<string>(props.tab.params.orderBy || '')
const sort = ref<{ col: string; dir: 'asc' | 'desc' } | null>(null)
const limit = ref<number>(props.tab.params.limit || 500)
const offset = ref(0)
const result = ref<QueryResult | null>(null)
const info = ref<TableInfo | null>(null)
const loading = ref(false)
const error = ref('')
const changes = ref(0)
const total = ref<number | null>(null)
const counting = ref(false)
const submitting = ref(false)
const preview = ref<string[] | null>(null)
const grid = ref<InstanceType<typeof DataGrid>>()
const showFilters = ref(!ui.isMobile)
let loadSeq = 0
let loadedOnce: Promise<void> | null = null

const editable = computed(() => !!result.value?.editable?.ok)
const keyCols = computed(() => result.value?.editable?.key || info.value?.key || [])
const readonlyCols = computed(() => (info.value?.columns || []).filter((c) => c.generated).map((c) => c.name))
const nullable = computed(() => Object.fromEntries((info.value?.columns || []).map((c) => [c.name, c.nullable])))
const enums = computed(() => Object.fromEntries((info.value?.columns || []).filter((c) => c.kind === 'enum' && c.enum_values).map((c) => [c.name, c.enum_values!])))
const effectiveOrder = computed(() => orderBy.value.trim() || (sort.value ? `${quoteIdent(sort.value.col, dialect.value)} ${sort.value.dir.toUpperCase()}` : ''))

async function load(opts: { keepOffset?: boolean; force?: boolean } = {}) {
  if (!conn.value) return
  if (changes.value > 0 && !opts.force) {
    if (!(await confirmDialog({ title: t('table.discardTitle'), message: t('table.discardHint', { n: changes.value }), danger: true, okText: t('table.discard') }))) return
  }
  if (!opts.keepOffset) offset.value = 0
  const seq = ++loadSeq
  loading.value = true
  error.value = ''
  tabs.setParams(props.tab.id, { where: where.value, orderBy: orderBy.value, limit: limit.value })
  const p = (async () => {
    try {
      const r = await api.post(`/connections/${conn.value!.id}/data/query`, { db: db.value, table: table.value, where: where.value, order_by: effectiveOrder.value, limit: limit.value, offset: offset.value })
      if (seq !== loadSeq) return
      result.value = r.result
      info.value = r.table
    } catch (e) {
      if (seq !== loadSeq) return
      error.value = errorText(e)
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  })()
  loadedOnce = p
  await p
}

async function count() {
  counting.value = true
  try {
    total.value = (await api.post(`/connections/${conn.value!.id}/data/count`, { db: db.value, table: table.value, where: where.value })).count
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    counting.value = false
  }
}

async function apply(dry: boolean) {
  const list = grid.value?.buildChanges() || []
  if (!list.length) return
  if (!dry && conn.value?.env === 'prod') {
    if (!(await confirmDialog({ title: t('table.prodTitle'), message: t('table.prodHint', { n: list.length, name: conn.value.name }), danger: true, okText: t('table.submit') }))) return
  }
  submitting.value = true
  try {
    const r = await api.post(`/connections/${conn.value!.id}/data/apply`, { db: db.value, table: table.value, changes: list, dry_run: dry })
    if (dry) {
      preview.value = r.statements
      return
    }
    if (r.failed_at >= 0) {
      toast.error(t('table.applyFailed', { n: r.failed_at + 1, err: r.error }))
      return
    }
    preview.value = null
    toast.success(t('table.applied', { n: r.applied }))
    await load({ keepOffset: true, force: true })
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    submitting.value = false
  }
}

function revert() {
  grid.value?.revertAll()
}

function onSort(col: string, dir: 'asc' | 'desc' | null) {
  orderBy.value = ''
  sort.value = dir ? { col, dir } : null
  load()
}

function onFilter(cond: string) {
  where.value = where.value.trim() ? `(${where.value.trim()}) AND ${cond}` : cond
  load()
}

function page(dir: number) {
  offset.value = Math.max(0, offset.value + dir * limit.value)
  load({ keepOffset: true })
}

async function loadFull(r: number, c: number): Promise<Cell> {
  const col = result.value!.columns![c]
  const key: Record<string, any> = {}
  for (const k of keyCols.value) {
    const i = result.value!.columns!.findIndex((x) => (x.origin || x.name) === k)
    key[k] = grid.value?.value(r, i) ?? null
  }
  const v = await api.post(`/connections/${conn.value!.id}/data/cell`, { db: db.value, table: table.value, column: col.origin || col.name, key })
  return v.value
}

const range = computed(() => {
  const n = result.value?.rows?.length || 0
  if (!n) return '0'
  return `${offset.value + 1}–${offset.value + n}`
})

onMounted(() => {
  // 刷新后恢复的标签：连接列表可能还没取回来，取回后再加载
  if (conn.value) load({ force: true })
  else if (!conns.list.length && !conns.loading) conns.load()
  const stop = watch(conn, (c) => { if (c && !result.value && !loading.value) { load({ force: true }); stop() } })
  registerTabState(props.tab.id, () => ({
    kind: 'table', tab_id: props.tab.id, connection_id: conn.value?.id, db: db.value, table: table.value, where: where.value, order_by: effectiveOrder.value,
    offset: offset.value, total_loaded: result.value?.rows?.length || 0, has_more: !!result.value?.has_more, pending_changes: changes.value,
    columns: (result.value?.columns || []).map((c) => c.name),
    selection: grid.value?.selection(),
    visible_rows_sample: (result.value?.rows || []).slice(0, 30).map((r) => r.map((v) => plainText(v, 'NULL'))),
    error: error.value || undefined,
  }), {
    // 智能体修改当前视图：更新条件后重新加载，等加载完成
    update: async (p: { where?: string; order_by?: string; scroll_to_row?: number }) => {
      if (p.where !== undefined) where.value = p.where
      if (p.order_by !== undefined) { orderBy.value = p.order_by; sort.value = null }
      await load({ force: true })
      if (p.scroll_to_row !== undefined) grid.value?.scrollToRow(p.scroll_to_row)
    },
    loaded: () => loadedOnce,
  })
})
onBeforeUnmount(() => unregisterTabState(props.tab.id))

// 外部（智能体、对象树）改了标签参数时重新加载
watch(() => [props.tab.params.where, props.tab.params.orderBy], ([w, o]) => {
  if ((w ?? '') !== where.value || (o ?? '') !== orderBy.value) {
    where.value = w || ''
    orderBy.value = o || ''
    load({ force: true })
  }
})
watch(changes, (n) => tabs.update(props.tab.id, { dirty: n > 0 }))

function onFilterKey(e: KeyboardEvent) {
  if (e.key === 'Enter') { e.preventDefault(); load() }
}
</script>

<template>
  <div class="tv" :class="{ prod: conn?.env === 'prod' }">
    <div class="lm-toolbar bar">
      <IconBtn icon="refresh" :label="$t('common.refresh')" :size="16" @click="load({ keepOffset: true })" />
      <template v-if="editable">
        <span class="sep" />
        <IconBtn icon="row-add" :label="$t('grid.addRow')" :size="16" @click="grid?.addRow()" />
        <IconBtn icon="row-remove" :label="$t('grid.deleteRows')" :size="16" @click="grid?.deleteSelected()" />
        <IconBtn icon="undo" :label="$t('table.revertAll')" :size="16" :disabled="!changes" @click="revert" />
        <IconBtn icon="code" :label="$t('table.previewSQL')" :size="16" :disabled="!changes" @click="apply(true)" />
        <Btn size="sm" :variant="changes ? 'primary' : 'secondary'" icon="check" :disabled="!changes" :loading="submitting" @click="apply(false)">
          {{ $t('table.submit') }}<span v-if="changes" class="cnt">{{ changes }}</span>
        </Btn>
      </template>
      <span class="sep" />
      <IconBtn icon="filter" :label="$t('table.filters')" :size="16" :active="showFilters" class="m-only" @click="showFilters = !showFilters" />
      <span class="lm-spacer" />
      <span v-if="conn?.env === 'prod'" class="lm-badge danger">{{ $t('explorer.env.prod') }}</span>
      <span v-if="result && !editable" class="lm-badge" :title="result.editable?.reason">{{ $t('table.readOnly') }}</span>
      <IconBtn icon="terminal" :label="$t('table.openConsole')" :size="16" @click="conn && openers.console(tabs, conn, db, { sql: `SELECT * FROM ${quoteIdent(table, dialect)}${where ? ' WHERE ' + where : ''};` })" />
    </div>
    <div v-show="showFilters" class="filters">
      <label class="f"><span class="kw">WHERE</span><input v-model="where" class="fi" spellcheck="false" :placeholder="$t('table.wherePh')" @keydown="onFilterKey" /></label>
      <label class="f"><span class="kw">ORDER BY</span><input v-model="orderBy" class="fi" spellcheck="false" :placeholder="sort ? `${sort.col} ${sort.dir.toUpperCase()}` : ''" @keydown="onFilterKey" /></label>
    </div>
    <div class="body">
      <div v-if="error" class="err"><Icon name="error" :size="16" /><span>{{ error }}</span></div>
      <DataGrid v-else-if="result" ref="grid" :columns="result.columns || []" :rows="result.rows || []" :editable="editable" :dialect="dialect" :table="table"
        :key-cols="keyCols" :readonly-cols="readonlyCols" :nullable="nullable" :enums="enums" :sort="sort" :row-offset="offset" :load-full="loadFull"
        @sort="onSort" @filter="onFilter" @submit="apply(false)" @changed="(n: number) => (changes = n)" />
      <div v-if="loading" class="loading"><span class="spin" /></div>
    </div>
    <div class="status">
      <span>{{ range }}{{ result?.has_more ? '+' : '' }} {{ $t('table.rows') }}</span>
      <span v-if="total !== null">· {{ $t('table.total', { n: total }) }}</span>
      <button v-else class="link" :disabled="counting" @click="count">{{ counting ? $t('common.loading') : $t('table.count') }}</button>
      <span v-if="result">· {{ result.duration_ms }} ms</span>
      <span v-if="changes" class="pending">· {{ $t('table.pending', { n: changes }) }}</span>
      <span v-if="result && !editable && result.editable?.reason" class="lm-faint lm-ellipsis reason">· {{ result.editable.reason }}</span>
      <span class="lm-spacer" />
      <select v-model.number="limit" class="ps" @change="load()">
        <option :value="100">100</option><option :value="500">500</option><option :value="1000">1000</option><option :value="5000">5000</option>
      </select>
      <IconBtn icon="chevron-left" :label="$t('table.prev')" :size="15" :disabled="offset === 0" @click="page(-1)" />
      <IconBtn icon="chevron-right" :label="$t('table.next')" :size="15" :disabled="!result?.has_more" @click="page(1)" />
    </div>
    <Modal :open="!!preview" :title="$t('table.previewSQL')" :width="720" @update:open="(v: boolean) => !v && (preview = null)">
      <div class="pv"><SqlView :code="(preview || []).join('\n')" :dialect="dialect" fill /></div>
      <template #footer>
        <Btn @click="preview = null">{{ $t('common.close') }}</Btn>
        <Btn variant="primary" :loading="submitting" @click="apply(false)">{{ $t('table.submit') }}</Btn>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
.tv { flex: 1; display: flex; flex-direction: column; min-height: 0; }
.tv.prod { box-shadow: inset 0 2px 0 var(--danger); }
.bar { gap: 2px; }
.cnt { margin-left: 6px; background: rgba(255, 255, 255, .25); border-radius: 8px; padding: 0 6px; font-size: 11px; }
.filters { display: flex; gap: 8px; padding: 6px 8px; border-bottom: 1px solid var(--border); background: var(--bg-soft); }
.f { flex: 1; display: flex; align-items: center; height: 28px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); background: var(--bg); overflow: hidden; min-width: 0; }
.f:focus-within { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
.kw { font-family: var(--font-mono); font-size: 11px; color: var(--cm-kw); padding: 0 8px; font-weight: 600; white-space: nowrap; border-right: 1px solid var(--border); height: 100%; display: flex; align-items: center; background: var(--bg-soft); }
.fi { flex: 1; border: 0; outline: none; background: none; height: 100%; padding: 0 8px; font-family: var(--font-mono); font-size: 12.5px; min-width: 0; }
.body { flex: 1; min-height: 0; display: flex; flex-direction: column; position: relative; }
.err { margin: 16px; padding: 12px 14px; border-radius: var(--radius-m); background: var(--danger-bg); color: var(--danger); display: flex; gap: 8px; align-items: flex-start; font-size: 13px; white-space: pre-wrap; font-family: var(--font-mono); }
.loading { position: absolute; top: 8px; right: 12px; }
.spin { display: block; width: 16px; height: 16px; border-radius: 50%; border: 2px solid var(--accent); border-right-color: transparent; animation: spin .7s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.status { height: 28px; flex: none; display: flex; align-items: center; gap: 6px; padding: 0 6px 0 12px; border-top: 1px solid var(--border); font-size: 12px; color: var(--text-2); background: var(--bg-soft); }
.pending { color: var(--warning); }
.reason { max-width: 40%; }
.link { border: 0; background: none; color: var(--accent); padding: 0; font-size: 12px; }
.ps { height: 22px; border: 1px solid var(--border); border-radius: 4px; background: var(--bg); font-size: 12px; }
.pv { height: 360px; border: 1px solid var(--border); border-radius: var(--radius-m); overflow: hidden; }
.m-only { display: none; }
@media (max-width: 760px) {
  .m-only { display: flex; }
  .filters { flex-direction: column; }
  .reason { display: none; }
}
</style>
