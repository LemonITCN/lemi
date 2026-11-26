<script setup lang="ts">
// 数据表格：行列双向虚拟滚动，像 Excel 一样框选、复制粘贴、直接编辑；改动在本地累积，由父组件提交。
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, shallowRef, watch } from 'vue'
import CtxMenu from '@/components/ui/ContextMenu.vue'
import type { MenuItem } from '@/components/ui/Menu.vue'
import Icon from '@/components/icons/Icon.vue'
import ValueEditor from './ValueEditor.vue'
import type { Cell, ResultColumn } from '@/lib/meta'
import type { Dialect } from '@/lib/sql'
import { copyText } from '@/lib/cm'
import { useUI } from '@/stores/ui'
import { useToast } from '@/stores/toast'
import { i18n } from '@/i18n'
import { NUMERIC_KINDS, buildChanges, copyAs, displayText, filterFor, isDefault, parseTSV, plainText, sameCell, textWidth, toTSV, type CopyFormat } from './model'

const props = withDefaults(defineProps<{
  columns: ResultColumn[]
  rows: Cell[][]
  editable?: boolean
  dialect?: Dialect
  table?: string
  keyCols?: string[]
  /** 不能编辑的列（生成列等） */
  readonlyCols?: string[]
  /** 可为空的列（置空时用 NULL，否则用空串） */
  nullable?: Record<string, boolean>
  /** 枚举列的可选值 */
  enums?: Record<string, string[]>
  sort?: { col: string; dir: 'asc' | 'desc' } | null
  /** 取完整值（被截断的长文本 / 二进制） */
  loadFull?: (row: number, col: number) => Promise<Cell>
  /** 行号从几开始（翻页时） */
  rowOffset?: number
}>(), { editable: false, dialect: 'mysql', keyCols: () => [], readonlyCols: () => [], nullable: () => ({}), enums: () => ({}), sort: null, rowOffset: 0 })

const emit = defineEmits<{
  sort: [col: string, dir: 'asc' | 'desc' | null]
  filter: [where: string]
  submit: []
  changed: [count: number]
  selection: []
}>()

const ui = useUI()
const toast = useToast()
const t = i18n.global.t

const rowH = computed(() => (ui.isMobile ? 32 : ui.density === 'comfortable' ? 28 : 25))
const headH = computed(() => rowH.value + 4)

// ---------- 数据与改动 ----------
const edits = shallowRef(new Map<number, Map<number, Cell>>())
const added = ref<Cell[][]>([])
const deleted = ref(new Set<number>())
const undoStack: { r: number; c: number; prev: Cell | undefined; addedRow?: boolean }[] = []

const n0 = computed(() => props.rows.length)
const total = computed(() => n0.value + added.value.length)

watch(() => props.rows, () => {
  edits.value = new Map()
  added.value = []
  deleted.value = new Set()
  undoStack.length = 0
  emitChanged()
  initWidths()
})

function value(r: number, c: number): Cell | undefined {
  if (r < n0.value) {
    const m = edits.value.get(r)
    if (m && m.has(c)) return m.get(c)
    return props.rows[r]?.[c]
  }
  return added.value[r - n0.value]?.[c]
}

function isEdited(r: number, c: number) { return r < n0.value && !!edits.value.get(r)?.has(c) }
function rowState(r: number): '' | 'new' | 'del' | 'mod' {
  if (r >= n0.value) return 'new'
  if (deleted.value.has(r)) return 'del'
  if (edits.value.get(r)?.size) return 'mod'
  return ''
}

const changeCount = computed(() => {
  let n = added.value.length + deleted.value.size
  for (const [r, m] of edits.value) if (m.size && !deleted.value.has(r)) n++
  return n
})
function emitChanged() { emit('changed', changeCount.value) }

function colEditable(c: number) {
  const col = props.columns[c]
  if (!props.editable || !col) return false
  if (!col.origin && !col.table) return false
  return !props.readonlyCols.includes(col.origin || col.name)
}

function setValue(r: number, c: number, v: Cell, record = true) {
  if (!colEditable(c) || deleted.value.has(r)) return
  if (r < n0.value) {
    const orig = props.rows[r][c]
    const m = new Map(edits.value)
    const rowMap = new Map(m.get(r) || [])
    if (record) undoStack.push({ r, c, prev: rowMap.has(c) ? rowMap.get(c) : undefined })
    if (sameCell(orig, v)) rowMap.delete(c)
    else rowMap.set(c, v)
    if (rowMap.size) m.set(r, rowMap)
    else m.delete(r)
    edits.value = m
  } else {
    const row = added.value[r - n0.value]
    if (!row) return
    if (record) undoStack.push({ r, c, prev: row[c], addedRow: true })
    row[c] = v
  }
  emitChanged()
}

function undo() {
  const u = undoStack.pop()
  if (!u) return
  if (u.addedRow) {
    const row = added.value[u.r - n0.value]
    if (row) row[u.c] = u.prev === undefined ? { $default: true } : u.prev
  } else {
    setValue(u.r, u.c, u.prev === undefined ? props.rows[u.r][u.c] : (u.prev as Cell), false)
  }
  focus.r = u.r
  focus.c = u.c
  anchor.r = u.r
  anchor.c = u.c
  scrollIntoView(u.r, u.c)
  emitChanged()
}

function addRow(copyFrom?: number): number {
  if (!props.editable) return -1
  const row: Cell[] = props.columns.map((_, c) => (copyFrom !== undefined && !props.keyCols.includes(props.columns[c].origin || props.columns[c].name) ? value(copyFrom, c) ?? null : { $default: true }))
  added.value.push(row)
  const r = total.value - 1
  focus.r = anchor.r = r
  focus.c = anchor.c = 0
  nextTick(() => scrollIntoView(r, 0))
  emitChanged()
  return r
}

function deleteRows(rows: number[]) {
  if (!props.editable) return
  const d = new Set(deleted.value)
  const removeAdded: number[] = []
  for (const r of rows) {
    if (r >= n0.value) removeAdded.push(r - n0.value)
    else if (d.has(r)) d.delete(r)
    else d.add(r)
  }
  deleted.value = d
  if (removeAdded.length) added.value = added.value.filter((_, i) => !removeAdded.includes(i))
  emitChanged()
}

function revertAll() {
  edits.value = new Map()
  added.value = []
  deleted.value = new Set()
  undoStack.length = 0
  emitChanged()
}

function revertCell(r: number, c: number) {
  if (r < n0.value) setValue(r, c, props.rows[r][c])
}

// ---------- 列宽 ----------
const widths = ref<number[]>([])
function initWidths() {
  const sample = props.rows.slice(0, 60)
  widths.value = props.columns.map((col, c) => {
    let w = textWidth(col.name) + 3
    for (const row of sample) w = Math.max(w, Math.min(48, textWidth(displayText(row[c]))))
    return Math.max(64, Math.min(380, Math.round(w * 7.4 + 22)))
  })
}
watch(() => props.columns, initWidths, { immediate: true })
const gutterW = computed(() => Math.max(44, String(total.value + props.rowOffset).length * 8 + 22))
const lefts = computed(() => {
  const out: number[] = []
  let x = gutterW.value
  for (const w of widths.value) { out.push(x); x += w }
  out.push(x)
  return out
})
const totalW = computed(() => lefts.value[lefts.value.length - 1] || gutterW.value)

function startColResize(e: PointerEvent, c: number) {
  e.preventDefault()
  e.stopPropagation()
  const x0 = e.clientX
  const w0 = widths.value[c]
  const move = (ev: PointerEvent) => { widths.value[c] = Math.max(40, Math.min(1200, w0 + ev.clientX - x0)) }
  const up = () => { window.removeEventListener('pointermove', move); window.removeEventListener('pointerup', up) }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}

// ---------- 虚拟滚动 ----------
const vp = ref<HTMLDivElement>()
const scrollTop = ref(0)
const scrollLeft = ref(0)
const vpH = ref(400)
const vpW = ref(800)
let ro: ResizeObserver | null = null
onMounted(() => {
  if (!vp.value) return
  ro = new ResizeObserver(() => { vpH.value = vp.value!.clientHeight; vpW.value = vp.value!.clientWidth })
  ro.observe(vp.value)
})
onBeforeUnmount(() => ro?.disconnect())
function onScroll() {
  scrollTop.value = vp.value!.scrollTop
  scrollLeft.value = vp.value!.scrollLeft
}
const visRows = computed(() => {
  const a = Math.max(0, Math.floor(scrollTop.value / rowH.value) - 4)
  const b = Math.min(total.value, Math.ceil((scrollTop.value + vpH.value) / rowH.value) + 4)
  const out: number[] = []
  for (let r = a; r < b; r++) out.push(r)
  return out
})
const visCols = computed(() => {
  const L = lefts.value
  const x0 = scrollLeft.value
  const x1 = x0 + vpW.value
  const out: number[] = []
  for (let c = 0; c < props.columns.length; c++) if (L[c + 1] > x0 - 200 && L[c] < x1 + 200) out.push(c)
  return out
})

function scrollIntoView(r: number, c: number) {
  const el = vp.value
  if (!el) return
  const top = headH.value + r * rowH.value
  if (top - headH.value < el.scrollTop) el.scrollTop = top - headH.value
  else if (top + rowH.value > el.scrollTop + el.clientHeight) el.scrollTop = top + rowH.value - el.clientHeight
  if (c >= 0) {
    const L = lefts.value
    if (L[c] - gutterW.value < el.scrollLeft) el.scrollLeft = L[c] - gutterW.value
    else if (L[c + 1] > el.scrollLeft + el.clientWidth) el.scrollLeft = L[c + 1] - el.clientWidth
  }
}

// ---------- 选择 ----------
const anchor = reactive({ r: 0, c: 0 })
const focus = reactive({ r: 0, c: 0 })
const sel = computed(() => ({ r0: Math.min(anchor.r, focus.r), r1: Math.max(anchor.r, focus.r), c0: Math.min(anchor.c, focus.c), c1: Math.max(anchor.c, focus.c) }))
function inSel(r: number, c: number) { const s = sel.value; return r >= s.r0 && r <= s.r1 && c >= s.c0 && c <= s.c1 }
function rowSelected(r: number) { const s = sel.value; return r >= s.r0 && r <= s.r1 && s.c0 === 0 && s.c1 === props.columns.length - 1 }
const hasData = computed(() => total.value > 0 && props.columns.length > 0)

function clampFocus() {
  focus.r = Math.max(0, Math.min(total.value - 1, focus.r))
  focus.c = Math.max(0, Math.min(props.columns.length - 1, focus.c))
}

let dragging = false
function onCellDown(e: MouseEvent, r: number, c: number) {
  if (e.button !== 0) {
    if (!inSel(r, c)) { anchor.r = focus.r = r; anchor.c = focus.c = c }
    return
  }
  commitEdit()
  focusSink()
  if (e.shiftKey) { focus.r = r; focus.c = c }
  else { anchor.r = focus.r = r; anchor.c = focus.c = c }
  dragging = true
  emit('selection')
}
function onCellEnter(r: number, c: number) {
  if (dragging) { focus.r = r; focus.c = c }
}
function onRowHeadDown(e: MouseEvent, r: number) {
  commitEdit()
  focusSink()
  if (e.shiftKey) focus.r = r
  else { anchor.r = focus.r = r }
  anchor.c = 0
  focus.c = props.columns.length - 1
  dragging = true
  emit('selection')
}
function onRowHeadEnter(r: number) { if (dragging) focus.r = r }
function selectColumn(c: number, extend = false) {
  commitEdit()
  focusSink()
  anchor.r = 0
  focus.r = Math.max(0, total.value - 1)
  if (!extend) anchor.c = c
  focus.c = c
  emit('selection')
}
function selectAll() {
  anchor.r = 0; anchor.c = 0
  focus.r = Math.max(0, total.value - 1); focus.c = props.columns.length - 1
}
const stopDrag = () => { dragging = false }
onMounted(() => window.addEventListener('mouseup', stopDrag))
onBeforeUnmount(() => window.removeEventListener('mouseup', stopDrag))

// ---------- 编辑 ----------
const editing = ref<{ r: number; c: number; text: string; enumVals?: string[] } | null>(null)
const editorEl = ref<HTMLInputElement | HTMLSelectElement>()
const valuePanel = ref<{ r: number; c: number } | null>(null)

function startEdit(r: number, c: number, initial?: string) {
  if (!colEditable(c) || deleted.value.has(r)) return
  const v = value(r, c)
  const col = props.columns[c]
  if (ui.isMobile || (typeof v === 'string' && v.includes('\n')) || (v && typeof v === 'object' && ('$trunc' in v || '$bin' in v)) || col.kind === 'json' || col.kind === 'text' && typeof v === 'string' && v.length > 200) {
    valuePanel.value = { r, c }
    return
  }
  const name = col.origin || col.name
  editing.value = { r, c, text: initial !== undefined ? initial : isDefault(v) || v === null || v === undefined ? '' : plainText(v), enumVals: props.enums[name] }
  nextTick(() => {
    const el = editorEl.value
    if (!el) return
    el.focus()
    if (el instanceof HTMLInputElement && initial === undefined) el.select()
  })
}

function commitEdit(move?: [number, number]) {
  const e = editing.value
  if (!e) return
  editing.value = null
  const cur = value(e.r, e.c)
  const was = cur === null || cur === undefined || isDefault(cur) ? null : plainText(cur)
  if (e.text !== was && !(was === null && e.text === '' && !e.enumVals)) setValue(e.r, e.c, e.text)
  if (move) {
    focus.r = anchor.r = Math.max(0, Math.min(total.value - 1, e.r + move[0]))
    focus.c = anchor.c = Math.max(0, Math.min(props.columns.length - 1, e.c + move[1]))
    scrollIntoView(focus.r, focus.c)
  }
  focusSink()
}
function cancelEdit() {
  editing.value = null
  focusSink()
}
function onEditorKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.isComposing) { e.preventDefault(); commitEdit([e.shiftKey ? -1 : 1, 0]) }
  else if (e.key === 'Tab') { e.preventDefault(); commitEdit([0, e.shiftKey ? -1 : 1]) }
  else if (e.key === 'Escape') { e.preventDefault(); cancelEdit() }
  e.stopPropagation()
}

function setNull(cells = selectedCells()) {
  for (const [r, c] of cells) {
    const name = props.columns[c].origin || props.columns[c].name
    if (props.nullable[name] === false) setValue(r, c, '')
    else setValue(r, c, null)
  }
}
function setDefault(cells = selectedCells()) { for (const [r, c] of cells) setValue(r, c, { $default: true }) }

function selectedCells(): [number, number][] {
  const s = sel.value
  const out: [number, number][] = []
  for (let r = s.r0; r <= s.r1; r++) for (let c = s.c0; c <= s.c1; c++) out.push([r, c])
  return out
}

// ---------- 键盘 ----------
const root = ref<HTMLDivElement>()
void root
// 隐藏的输入接收器：键盘焦点放在它上面，直接打字、粘贴、中文输入法组字都从这里进来
const sink = ref<HTMLTextAreaElement>()
let composing = false
function focusSink() { sink.value?.focus({ preventScroll: true }) }
function onSinkInput() {
  const el = sink.value
  if (!el || composing || editing.value) return
  const text = el.value
  el.value = ''
  if (text && hasData.value) startEdit(focus.r, focus.c, text)
}
function onCompStart() { composing = true }
function onCompEnd(e: CompositionEvent) {
  composing = false
  const el = sink.value
  if (el) el.value = ''
  if (e.data && hasData.value && !editing.value) startEdit(focus.r, focus.c, e.data)
}
const sinkPos = computed(() => ({ top: headH.value + focus.r * rowH.value + 'px', left: (lefts.value[focus.c] ?? 0) + 'px', height: rowH.value + 'px' }))
function onKey(e: KeyboardEvent) {
  if (editing.value || !hasData.value || composing || e.isComposing || e.key === 'Process') return
  const mod = e.metaKey || e.ctrlKey
  const page = Math.max(1, Math.floor(vpH.value / rowH.value) - 1)
  const move = (dr: number, dc: number) => {
    focus.r += dr
    focus.c += dc
    clampFocus()
    if (!e.shiftKey) { anchor.r = focus.r; anchor.c = focus.c }
    scrollIntoView(focus.r, focus.c)
    e.preventDefault()
  }
  switch (e.key) {
    case 'ArrowUp': return move(mod ? -focus.r : -1, 0)
    case 'ArrowDown': return move(mod ? total.value : 1, 0)
    case 'ArrowLeft': return move(0, mod ? -focus.c : -1)
    case 'ArrowRight': return move(0, mod ? props.columns.length : 1)
    case 'PageUp': return move(-page, 0)
    case 'PageDown': return move(page, 0)
    case 'Home': return move(mod ? -focus.r : 0, -focus.c)
    case 'End': return move(mod ? total.value : 0, props.columns.length)
    case 'Tab': return move(0, e.shiftKey ? -1 : 1)
    case 'Enter':
      e.preventDefault()
      if (mod) { emit('submit'); return }
      startEdit(focus.r, focus.c)
      return
    case 'F2': e.preventDefault(); startEdit(focus.r, focus.c); return
    case 'Escape': anchor.r = focus.r; anchor.c = focus.c; return
    case 'Delete': case 'Backspace': e.preventDefault(); setNull(); return
  }
  if (mod && e.key.toLowerCase() === 'a') { e.preventDefault(); selectAll(); return }
  if (mod && e.key.toLowerCase() === 'z') { e.preventDefault(); undo(); return }
  // 可打印字符交给接收器的 input 事件（兼容输入法与程序化输入）
}

function selectedMatrix(): { cols: ResultColumn[]; rows: (Cell | undefined)[][]; keyRows: (Cell | undefined)[][] } {
  const s = sel.value
  const cols = props.columns.slice(s.c0, s.c1 + 1)
  const rows: (Cell | undefined)[][] = []
  const keyIdx = props.keyCols.map((k) => props.columns.findIndex((c) => (c.origin || c.name) === k))
  const keyRows: (Cell | undefined)[][] = []
  for (let r = s.r0; r <= s.r1; r++) {
    const row: (Cell | undefined)[] = []
    for (let c = s.c0; c <= s.c1; c++) row.push(value(r, c))
    rows.push(row)
    keyRows.push(keyIdx.map((i) => (i >= 0 ? (r < n0.value ? props.rows[r][i] : value(r, i)) : undefined)))
  }
  return { cols, rows, keyRows }
}

async function copy(fmt: CopyFormat = 'tsv') {
  const m = selectedMatrix()
  const text = copyAs(fmt, m.cols, m.rows, { dialect: props.dialect, table: props.table, keyCols: props.keyCols, keyRows: m.keyRows })
  if (await copyText(text)) toast.success(t('grid.copied', { n: m.rows.length }))
}

function onCopy(e: ClipboardEvent) {
  if (editing.value || !hasData.value) return
  const m = selectedMatrix()
  e.clipboardData?.setData('text/plain', toTSV(m.rows.map((r) => r.map((v) => plainText(v)))))
  e.preventDefault()
}

function onPaste(e: ClipboardEvent) {
  if (editing.value) return
  const text = e.clipboardData?.getData('text/plain')
  if (!text || !props.editable) return
  e.preventDefault()
  pasteText(text)
}

function pasteText(text: string) {
  const data = parseTSV(text)
  if (!data.length) return
  const s = sel.value
  // 单个值且选了一块区域：填满整个选区
  if (data.length === 1 && data[0].length === 1 && (s.r1 > s.r0 || s.c1 > s.c0)) {
    for (const [r, c] of selectedCells()) setValue(r, c, data[0][0])
    return
  }
  let r0 = s.r0
  const c0 = s.c0
  for (let i = 0; i < data.length; i++) {
    let r = r0 + i
    if (r >= total.value) r = addRow()
    if (r < 0) break
    data[i].forEach((v, j) => {
      const c = c0 + j
      if (c < props.columns.length) setValue(r, c, v)
    })
  }
  anchor.r = r0
  anchor.c = c0
  focus.r = Math.min(total.value - 1, r0 + data.length - 1)
  focus.c = Math.min(props.columns.length - 1, c0 + Math.max(...data.map((d) => d.length)) - 1)
  toast.info(t('grid.pasted', { n: data.length }))
}

// ---------- 右键菜单 ----------
const menuItems = computed<MenuItem[]>(() => {
  const r = focus.r
  const c = focus.c
  const col = props.columns[c]
  const items: MenuItem[] = [
    { label: t('grid.copy'), icon: 'copy', hint: '⌘C', run: () => copy('tsv') },
    { heading: t('grid.copyAs') },
    { label: 'CSV', run: () => copy('csv') },
    { label: 'JSON', run: () => copy('json') },
    { label: 'INSERT', run: () => copy('insert') },
    { label: 'UPDATE', run: () => copy('update') },
    { label: 'Markdown', run: () => copy('markdown') },
    { label: t('grid.copyWhere'), run: () => copy('where') },
  ]
  if (col && hasData.value) {
    items.push({ separator: true }, { label: t('grid.filterBy'), icon: 'filter', run: () => emit('filter', filterFor(col, value(r, c), props.dialect)) })
    items.push({ label: t('grid.viewValue'), icon: 'eye', run: () => (valuePanel.value = { r, c }) })
  }
  if (props.editable) {
    items.push({ separator: true },
      { label: t('grid.edit'), icon: 'edit', disabled: !colEditable(c), run: () => startEdit(r, c) },
      { label: t('grid.paste'), icon: 'paste', run: async () => { try { pasteText(await navigator.clipboard.readText()) } catch { toast.error(t('grid.pasteDenied')) } } },
      { label: t('grid.setNull'), icon: 'null', run: () => setNull() },
      { label: t('grid.setDefault'), run: () => setDefault() },
      { label: t('grid.revertCell'), icon: 'undo', disabled: !isEdited(r, c), run: () => { for (const [rr, cc] of selectedCells()) revertCell(rr, cc) } },
      { separator: true },
      { label: t('grid.addRow'), icon: 'row-add', run: () => addRow() },
      { label: t('grid.cloneRow'), icon: 'duplicate', run: () => addRow(r) },
      { label: deleted.value.has(r) ? t('grid.undelete') : t('grid.deleteRows'), icon: 'row-remove', danger: !deleted.value.has(r), run: () => deleteRows(selectedRowList()) })
  }
  return items
})

function selectedRowList() {
  const out: number[] = []
  for (let r = sel.value.r0; r <= sel.value.r1; r++) out.push(r)
  return out
}

function onValueSaved(v: Cell) {
  const p = valuePanel.value
  valuePanel.value = null
  if (p) setValue(p.r, p.c, v)
}

function headerSort(c: number) {
  const name = props.columns[c].origin || props.columns[c].name
  const cur = props.sort?.col === name ? props.sort.dir : null
  emit('sort', name, cur === null ? 'asc' : cur === 'asc' ? 'desc' : null)
}

defineExpose({
  changeCount,
  buildChanges: () => buildChanges(props.rows, edits.value, added.value, deleted.value, props.columns, props.keyCols),
  revertAll,
  addRow: () => addRow(),
  deleteSelected: () => deleteRows(selectedRowList()),
  undo,
  focus: () => focusSink(),
  selection: () => {
    const m = selectedMatrix()
    return { columns: m.cols.map((c) => c.name), rows: m.rows.map((r) => r.map((v) => plainText(v, 'NULL'))), range: { ...sel.value } }
  },
  scrollToRow: (r: number) => { anchor.r = focus.r = r; scrollIntoView(r, focus.c) },
  value,
})
</script>

<template>
  <div ref="root" class="grid" @copy="onCopy" @paste="onPaste" @mousedown="!editing && $nextTick(focusSink)">
    <CtxMenu :items="menuItems" :width="210">
      <div ref="vp" class="vp" @scroll="onScroll">
        <div class="spacer" :style="{ height: headH + total * rowH + 'px', width: totalW + 'px' }">
          <!-- 表头 -->
          <div class="head" :style="{ height: headH + 'px', width: totalW + 'px' }">
            <div class="corner" :style="{ width: gutterW + 'px', height: headH + 'px' }" @mousedown.prevent="selectAll" />
            <div v-for="c in visCols" :key="c" class="hcell" :class="{ sel: sel.c0 <= c && c <= sel.c1 && sel.r0 === 0 && sel.r1 === total - 1 && total > 0, num: NUMERIC_KINDS.has(columns[c].kind) }"
              :style="{ left: lefts[c] + 'px', width: widths[c] + 'px', height: headH + 'px' }" :title="`${columns[c].name}  ${columns[c].type}`"
              @mousedown.left="selectColumn(c, $event.shiftKey)">
              <Icon v-if="columns[c].pk" name="key" :size="11" class="pk" />
              <span class="hname">{{ columns[c].name }}</span>
              <span class="htype">{{ columns[c].type.toLowerCase() }}</span>
              <button class="sortbtn" :class="{ on: sort?.col === (columns[c].origin || columns[c].name) }" @mousedown.stop @click.stop="headerSort(c)">
                <Icon :name="sort?.col === (columns[c].origin || columns[c].name) && sort?.dir === 'desc' ? 'arrow-down' : 'arrow-up'" :size="11" />
              </button>
              <span class="rz" @pointerdown="startColResize($event, c)" @mousedown.stop />
            </div>
          </div>
          <!-- 行 -->
          <div v-for="r in visRows" :key="r" class="row" :class="[rowState(r), { odd: r % 2 === 1 }]" :style="{ top: headH + r * rowH + 'px', height: rowH + 'px', width: totalW + 'px' }">
            <div class="gut" :class="{ sel: rowSelected(r), focus: r === focus.r }" :style="{ width: gutterW + 'px', height: rowH + 'px' }"
              @mousedown.left="onRowHeadDown($event, r)" @mouseenter="onRowHeadEnter(r)">
              <span v-if="rowState(r) === 'new'" class="mark new">+</span>
              <span v-else-if="rowState(r) === 'del'" class="mark del">−</span>
              <span v-else-if="rowState(r) === 'mod'" class="mark mod">*</span>
              {{ r < n0 ? r + 1 + rowOffset : '' }}
            </div>
            <div v-for="c in visCols" :key="c" class="cell"
              :class="{ sel: inSel(r, c), focus: r === focus.r && c === focus.c, num: NUMERIC_KINDS.has(columns[c].kind), null: value(r, c) === null || value(r, c) === undefined,
                special: !!value(r, c) && typeof value(r, c) === 'object', edited: isEdited(r, c) }"
              :style="{ left: lefts[c] + 'px', width: widths[c] + 'px', height: rowH + 'px', lineHeight: rowH - 1 + 'px' }"
              @mousedown="onCellDown($event, r, c)" @mouseenter="onCellEnter(r, c)" @dblclick="startEdit(r, c)">
              {{ displayText(value(r, c)) }}
            </div>
          </div>
          <textarea ref="sink" class="sink" :style="sinkPos" tabindex="0" autocomplete="off" autocorrect="off" autocapitalize="off" spellcheck="false" aria-label="grid"
            @keydown="onKey" @input="onSinkInput" @compositionstart="onCompStart" @compositionend="onCompEnd" />
          <!-- 编辑框 -->
          <template v-if="editing">
            <select v-if="editing.enumVals" ref="editorEl" v-model="editing.text" class="editor"
              :style="{ top: headH + editing.r * rowH + 'px', left: lefts[editing.c] + 'px', width: Math.max(widths[editing.c], 120) + 'px', height: rowH + 'px' }"
              @keydown="onEditorKey" @blur="commitEdit()" @change="commitEdit()">
              <option v-for="o in editing.enumVals" :key="o" :value="o">{{ o }}</option>
            </select>
            <input v-else ref="editorEl" v-model="editing.text" class="editor" spellcheck="false"
              :style="{ top: headH + editing.r * rowH + 'px', left: lefts[editing.c] + 'px', width: Math.max(widths[editing.c], 140) + 'px', height: rowH + 'px' }"
              @keydown="onEditorKey" @blur="commitEdit()" />
          </template>
        </div>
        <div v-if="!total && columns.length" class="empty">{{ $t('grid.noRows') }}</div>
      </div>
    </CtxMenu>
    <ValueEditor v-if="valuePanel" :column="columns[valuePanel.c]" :value="value(valuePanel.r, valuePanel.c)" :editable="colEditable(valuePanel.c) && !deleted.has(valuePanel.r)"
      :load-full="loadFull && valuePanel.r < n0 ? () => loadFull!(valuePanel!.r, valuePanel!.c) : undefined" @close="valuePanel = null" @save="onValueSaved" />
  </div>
</template>

<style scoped>
.grid { flex: 1; min-height: 0; position: relative; outline: none; display: flex; flex-direction: column; font-size: 12.5px; }
.vp { flex: 1; min-height: 0; overflow: auto; position: relative; background: var(--bg); }
.spacer { position: relative; }
.head { position: sticky; top: 0; z-index: 3; background: var(--bg-soft); border-bottom: 1px solid var(--border-strong); }
.corner { position: sticky; left: 0; top: 0; z-index: 4; background: var(--bg-soft); border-right: 1px solid var(--border-strong); cursor: default; }
.hcell { position: absolute; top: 0; display: flex; align-items: center; gap: 5px; padding: 0 6px 0 8px; border-right: 1px solid var(--border); cursor: default; user-select: none; overflow: hidden; }
.hcell:hover { background: var(--bg-hover); }
.hcell.sel { background: var(--accent-soft); }
.hname { font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; color: var(--text); flex: none; max-width: 70%; }
.htype { color: var(--text-3); font-size: 10.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 1; min-width: 0; }
.pk { color: var(--c-yellow); flex: none; }
.sortbtn { border: 0; background: none; padding: 2px; border-radius: 3px; color: var(--text-3); display: flex; opacity: 0; flex: none; }
.hcell:hover .sortbtn, .sortbtn.on { opacity: 1; }
.sortbtn.on { color: var(--accent); }
.rz { position: absolute; right: -3px; top: 0; bottom: 0; width: 7px; cursor: col-resize; z-index: 2; }
.row { position: absolute; left: 0; }
.row.odd .cell { background: var(--grid-odd, transparent); }
.gut { position: sticky; left: 0; z-index: 2; display: flex; align-items: center; justify-content: flex-end; gap: 3px; padding-right: 8px;
  background: var(--bg-soft); color: var(--text-3); font-size: 11px; border-right: 1px solid var(--border-strong); border-bottom: 1px solid var(--border); font-variant-numeric: tabular-nums; cursor: default; user-select: none; box-sizing: border-box; }
.gut.focus { color: var(--text); }
.gut.sel { background: var(--accent-soft); color: var(--accent); }
.mark { font-weight: 700; margin-right: auto; padding-left: 5px; }
.mark.new { color: var(--success); }
.mark.del { color: var(--danger); }
.mark.mod { color: var(--warning); }
.cell { position: absolute; top: 0; padding: 0 8px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; border-right: 1px solid var(--border); border-bottom: 1px solid var(--border);
  font-family: var(--font-mono); font-size: 12px; color: var(--text); cursor: cell; user-select: none; }
.cell.num { text-align: right; font-variant-numeric: tabular-nums; }
.cell.null { color: var(--text-3); font-style: italic; }
.cell.special { color: var(--c-purple); }
.cell.edited { background: rgba(203, 145, 47, .18) !important; }
.row.new .cell { background: rgba(68, 131, 97, .12); }
.row.del .cell { background: rgba(212, 76, 71, .1); text-decoration: line-through; color: var(--text-3); }
.cell.sel { background: var(--accent-soft) !important; }
.cell.focus { box-shadow: inset 0 0 0 2px var(--accent); }
.sink { position: absolute; width: 2px; opacity: 0; border: 0; padding: 0; margin: 0; resize: none; overflow: hidden; z-index: 0; pointer-events: none; font-size: 12px; }
.editor { position: absolute; z-index: 5; border: 0; outline: 2px solid var(--accent); padding: 0 7px; font-family: var(--font-mono); font-size: 12px; background: var(--bg-elev); color: var(--text); box-shadow: var(--shadow-pop); }
.empty { position: absolute; top: 60px; left: 0; right: 0; text-align: center; color: var(--text-3); font-size: 13px; pointer-events: none; }
html[data-theme="dark"] .grid { --grid-odd: rgba(255, 255, 255, .015); }
html[data-theme="light"] .grid, html:not([data-theme]) .grid { --grid-odd: rgba(55, 53, 47, .018); }
</style>
