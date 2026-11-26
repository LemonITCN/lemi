<script setup lang="ts">
// ER 图：勾选库里的表，按外键生成 Mermaid erDiagram 渲染；支持缩放、拖动平移、适应窗口，导出 SVG / PNG / 源码。
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import type { Tab } from '@/modules/registry'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import { useConns } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { fetchTable, mapLimit, type TableInfo } from '@/lib/meta'
import { copyText } from '@/lib/cm'
import { errorText, i18n } from '@/i18n'
import { buildErd } from './erd'

const DEFAULT_LIMIT = 40

const props = defineProps<{ tab: Tab; active: boolean }>()
const conns = useConns()
const tabs = useTabs()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t

const conn = computed(() => conns.byId[props.tab.params.connectionId])
const connMissing = computed(() => !conn.value && !conns.loading && conns.list.length > 0)
const db = computed(() => String(props.tab.params.database || ''))

// ---------- 表清单与勾选 ----------
const allTables = ref<string[]>([])
const selected = ref<string[]>([])
const limited = ref(false)
const listLoading = ref(false)
const listError = ref('')
const listLoaded = ref(false)
const filter = ref('')
const panelOpen = ref(!ui.isMobile)
const showComments = ref(props.tab.params.comments !== false)
const keysOnly = ref(!!props.tab.params.keysOnly)

const selSet = computed(() => new Set(selected.value))
const visibleTables = computed(() => {
  const q = filter.value.trim().toLowerCase()
  return q ? allTables.value.filter((n) => n.toLowerCase().includes(q)) : allTables.value
})

async function loadList(force = false) {
  const c = conn.value
  if (!c) return
  listLoading.value = true
  listError.value = ''
  try {
    const o = await conns.loadObjects(c.id, db.value, force)
    allTables.value = (o.tables || []).map((x) => x.name)
    const saved: string[] | undefined = Array.isArray(props.tab.params.tables) ? props.tab.params.tables : undefined
    if (saved) {
      const have = new Set(allTables.value)
      selected.value = saved.filter((n) => have.has(n))
    } else {
      limited.value = allTables.value.length > DEFAULT_LIMIT
      selected.value = allTables.value.slice(0, DEFAULT_LIMIT)
    }
    listLoaded.value = true
  } catch (e) {
    listError.value = errorText(e)
  } finally {
    listLoading.value = false
  }
}

// 刷新页面后恢复的标签在第一次切到前台时才开始读结构与渲染
watch([conn, () => props.active], ([c, a]) => { if (c && a && !listLoaded.value && !listLoading.value) loadList() }, { immediate: true })

function toggle(name: string) {
  selected.value = selSet.value.has(name) ? selected.value.filter((n) => n !== name) : [...selected.value, name]
}
function selectAll() {
  const add = visibleTables.value.filter((n) => !selSet.value.has(n))
  selected.value = [...selected.value, ...add]
}
function selectNone() {
  if (filter.value.trim()) {
    const drop = new Set(visibleTables.value)
    selected.value = selected.value.filter((n) => !drop.has(n))
  } else selected.value = []
}

// 按库里的顺序画，勾选顺序不影响布局
const ordered = computed(() => allTables.value.filter((n) => selSet.value.has(n)))

// ---------- 取结构与生成源码 ----------
const cache = new Map<string, TableInfo>()
const failed = ref<Record<string, string>>({})
const progress = reactive({ done: 0, total: 0 })
const fetching = ref(false)
const source = ref('')
let buildSeq = 0

async function build() {
  const c = conn.value
  if (!c || !listLoaded.value) return
  const seq = ++buildSeq
  const names = ordered.value
  const need = names.filter((n) => !cache.has(n))
  fetching.value = need.length > 0
  if (need.length) {
    progress.done = 0
    progress.total = need.length
    const errs: Record<string, string> = { ...failed.value }
    await mapLimit(need, 6, async (n) => {
      try {
        cache.set(n, await fetchTable(c.id, db.value, n))
        delete errs[n]
      } catch (e) {
        errs[n] = errorText(e)
      } finally {
        if (seq === buildSeq) progress.done++
      }
    })
    if (seq !== buildSeq) return
    failed.value = errs
    fetching.value = false
  }
  const list = names.map((n) => cache.get(n)).filter((x): x is TableInfo => !!x)
  source.value = list.length ? buildErd(list, { comments: showComments.value, keysOnly: keysOnly.value }) : ''
  await render()
}

let buildTimer: ReturnType<typeof setTimeout> | undefined
function scheduleBuild(delay = 350) {
  clearTimeout(buildTimer)
  buildTimer = setTimeout(build, delay)
}

watch(selected, (v) => {
  if (!listLoaded.value) return
  tabs.setParams(props.tab.id, { tables: v })
  needFit = true
  scheduleBuild()
})
watch([showComments, keysOnly], ([c, k]) => {
  tabs.setParams(props.tab.id, { comments: c, keysOnly: k })
  scheduleBuild(0)
})
watch(() => listLoaded.value && allTables.value.length, () => scheduleBuild(0))

async function refresh() {
  cache.clear()
  failed.value = {}
  listLoaded.value = false
  await loadList(true)
  scheduleBuild(0)
}

// ---------- 渲染 ----------
const host = ref<HTMLDivElement>()
const viewport = ref<HTMLDivElement>()
const rendering = ref(false)
const renderError = ref('')
const hasSvg = ref(false)
const natural = reactive({ w: 0, h: 0 })
let renderSeq = 0
let needFit = true

function isDark() { return document.documentElement.dataset.theme === 'dark' }

async function render() {
  const el = host.value
  if (!el) return
  const seq = ++renderSeq
  renderError.value = ''
  if (!source.value) {
    el.innerHTML = ''
    hasSvg.value = false
    return
  }
  rendering.value = true
  const id = `erd-${props.tab.id}-${seq}`
  try {
    const { default: mermaid } = await import('mermaid')
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: 'strict',
      theme: isDark() ? 'dark' : 'default',
      fontFamily: getComputedStyle(document.body).fontFamily,
      maxTextSize: 2_000_000,
      maxEdges: 5000,
      htmlLabels: false,
      er: { useMaxWidth: false },
      flowchart: { htmlLabels: false },
    })
    const { svg } = await mermaid.render(id, source.value)
    if (seq !== renderSeq) return
    el.innerHTML = svg
    const s = el.querySelector('svg')
    if (s) {
      const vb = s.viewBox?.baseVal
      const w = vb && vb.width ? vb.width : parseFloat(s.getAttribute('width') || '0') || s.getBoundingClientRect().width
      const h = vb && vb.height ? vb.height : parseFloat(s.getAttribute('height') || '0') || s.getBoundingClientRect().height
      natural.w = w
      natural.h = h
      s.setAttribute('width', String(w))
      s.setAttribute('height', String(h))
      s.style.maxWidth = 'none'
    }
    hasSvg.value = true
    if (needFit) await nextTick(fit)
  } catch (e) {
    if (seq !== renderSeq) return
    document.getElementById('d' + id)?.remove()
    document.getElementById(id)?.remove()
    renderError.value = errorText(e)
  } finally {
    if (seq === renderSeq) rendering.value = false
  }
}

// 主题切换后按新主题重新渲染
let themeObs: MutationObserver | null = null
onMounted(() => {
  themeObs = new MutationObserver(() => { if (source.value) render() })
  themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
})
onBeforeUnmount(() => {
  themeObs?.disconnect()
  clearTimeout(buildTimer)
})

// ---------- 缩放与平移 ----------
const view = reactive({ x: 0, y: 0, k: 1 })
const zoomPct = computed(() => Math.round(view.k * 100) + '%')
const MIN_K = 0.05
const MAX_K = 4

function zoomAt(factor: number, cx?: number, cy?: number) {
  const vp = viewport.value
  if (!vp) return
  const px = cx ?? vp.clientWidth / 2
  const py = cy ?? vp.clientHeight / 2
  const k = Math.min(MAX_K, Math.max(MIN_K, view.k * factor))
  view.x = px - (px - view.x) * (k / view.k)
  view.y = py - (py - view.y) * (k / view.k)
  view.k = k
}

function fit() {
  const vp = viewport.value
  if (!vp || !natural.w || !natural.h) return
  const vw = vp.clientWidth
  const vh = vp.clientHeight
  if (!vw || !vh) { needFit = true; return }
  needFit = false
  const pad = 24
  const k = Math.max(MIN_K, Math.min((vw - pad * 2) / natural.w, (vh - pad * 2) / natural.h, 1.5))
  view.k = k
  view.x = (vw - natural.w * k) / 2
  view.y = Math.max(pad, (vh - natural.h * k) / 2)
}

function resetZoom() { zoomAt(1 / view.k) }

// 标签被隐藏时视口尺寸为 0，重新显示后再适应窗口
watch(() => props.active, (a) => { if (a && needFit && hasSvg.value) nextTick(fit) })

function onWheel(e: WheelEvent) {
  const vp = viewport.value
  if (!vp || !hasSvg.value) return
  e.preventDefault()
  if (e.ctrlKey || e.metaKey) {
    const r = vp.getBoundingClientRect()
    zoomAt(Math.exp(-e.deltaY * 0.0025), e.clientX - r.left, e.clientY - r.top)
  } else {
    view.x -= e.deltaX
    view.y -= e.deltaY
  }
}

// 鼠标拖动平移；触屏单指平移、双指缩放
const pointers = new Map<number, { x: number; y: number }>()
let pinchDist = 0
const dragging = ref(false)

function onPointerDown(e: PointerEvent) {
  if (!hasSvg.value || (e.pointerType === 'mouse' && e.button !== 0)) return
  if ((e.target as HTMLElement).closest('button, a, input')) return
  viewport.value?.setPointerCapture(e.pointerId)
  pointers.set(e.pointerId, { x: e.clientX, y: e.clientY })
  dragging.value = true
  if (pointers.size === 2) {
    const [a, b] = [...pointers.values()]
    pinchDist = Math.hypot(a.x - b.x, a.y - b.y)
  }
}

function onPointerMove(e: PointerEvent) {
  const p = pointers.get(e.pointerId)
  if (!p) return
  if (pointers.size === 1) {
    view.x += e.clientX - p.x
    view.y += e.clientY - p.y
  }
  p.x = e.clientX
  p.y = e.clientY
  if (pointers.size === 2 && viewport.value) {
    const [a, b] = [...pointers.values()]
    const d = Math.hypot(a.x - b.x, a.y - b.y)
    if (pinchDist > 0 && d > 0) {
      const r = viewport.value.getBoundingClientRect()
      zoomAt(d / pinchDist, (a.x + b.x) / 2 - r.left, (a.y + b.y) / 2 - r.top)
    }
    pinchDist = d
  }
}

function onPointerUp(e: PointerEvent) {
  pointers.delete(e.pointerId)
  if (pointers.size < 2) pinchDist = 0
  if (!pointers.size) dragging.value = false
}

// ---------- 导出 ----------
function fileBase() { return `${db.value || 'erd'}-er` }

function download(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 2000)
}

function bgColor() { return getComputedStyle(document.body).backgroundColor || (isDark() ? '#191919' : '#ffffff') }

/** 导出用的 SVG 文本：带命名空间与当前主题背景。 */
function svgText(): string | null {
  const s = host.value?.querySelector('svg')
  if (!s) return null
  const clone = s.cloneNode(true) as SVGSVGElement
  clone.setAttribute('xmlns', 'http://www.w3.org/2000/svg')
  clone.setAttribute('width', String(natural.w))
  clone.setAttribute('height', String(natural.h))
  const vb = s.viewBox?.baseVal
  const rect = document.createElementNS('http://www.w3.org/2000/svg', 'rect')
  rect.setAttribute('x', String(vb?.x || 0))
  rect.setAttribute('y', String(vb?.y || 0))
  rect.setAttribute('width', String(vb?.width || natural.w))
  rect.setAttribute('height', String(vb?.height || natural.h))
  rect.setAttribute('fill', bgColor())
  clone.insertBefore(rect, clone.firstChild)
  return new XMLSerializer().serializeToString(clone)
}

function exportSvg() {
  const text = svgText()
  if (text) download(new Blob([text], { type: 'image/svg+xml;charset=utf-8' }), fileBase() + '.svg')
}

async function exportPng() {
  const text = svgText()
  if (!text) return
  const url = URL.createObjectURL(new Blob([text], { type: 'image/svg+xml;charset=utf-8' }))
  try {
    const img = new Image()
    await new Promise<void>((res, rej) => { img.onload = () => res(); img.onerror = () => rej(new Error('image')); img.src = url })
    const w = natural.w
    const h = natural.h
    // 2 倍清晰度；超大图按浏览器画布上限缩小
    const scale = Math.max(0.1, Math.min(2, 16000 / w, 16000 / h, Math.sqrt(120_000_000 / (w * h))))
    const canvas = document.createElement('canvas')
    canvas.width = Math.round(w * scale)
    canvas.height = Math.round(h * scale)
    const ctx = canvas.getContext('2d')
    if (!ctx) throw new Error('canvas')
    ctx.fillStyle = bgColor()
    ctx.fillRect(0, 0, canvas.width, canvas.height)
    ctx.drawImage(img, 0, 0, canvas.width, canvas.height)
    const blob = await new Promise<Blob | null>((res) => canvas.toBlob(res, 'image/png'))
    if (!blob) throw new Error('png')
    download(blob, fileBase() + '.png')
  } catch {
    toast.error(t('erd.pngFailed'))
  } finally {
    URL.revokeObjectURL(url)
  }
}

async function copySource() {
  if (source.value && (await copyText(source.value))) toast.success(t('common.copied'))
}

const displayItems = computed<MenuItem[]>(() => [
  { label: t('erd.showComments'), checked: showComments.value, run: () => { showComments.value = !showComments.value } },
  { label: t('erd.keysOnly'), checked: keysOnly.value, run: () => { keysOnly.value = !keysOnly.value } },
])
const exportItems = computed<MenuItem[]>(() => [
  { label: t('erd.exportSvg'), icon: 'download', disabled: !hasSvg.value, run: exportSvg },
  { label: t('erd.exportPng'), icon: 'image', disabled: !hasSvg.value, run: exportPng },
  { label: t('erd.copySource'), icon: 'copy', disabled: !source.value, run: copySource },
])
const mobileItems = computed<MenuItem[]>(() => [
  ...displayItems.value,
  { separator: true },
  { label: t('common.refresh'), icon: 'refresh', run: refresh },
  { separator: true },
  ...exportItems.value,
])

const failedCount = computed(() => Object.keys(failed.value).filter((n) => selSet.value.has(n)).length)
const failedText = computed(() => Object.entries(failed.value).filter(([n]) => selSet.value.has(n)).map(([n, e]) => `${n}: ${e}`).join('\n'))
const busy = computed(() => fetching.value || rendering.value)
</script>

<template>
  <div class="erd" :class="{ mobile: ui.isMobile }">
    <div class="lm-toolbar">
      <IconBtn :icon="ui.isMobile ? 'list' : 'sidebar'" :size="16" :active="panelOpen" :label="$t('erd.tables')" @click="panelOpen = !panelOpen" />
      <Icon name="erd" :size="16" class="ticon" />
      <span class="title lm-ellipsis">{{ db }}</span>
      <span class="lm-faint count">{{ selected.length }}/{{ allTables.length }}</span>
      <span class="lm-spacer" />
      <span v-if="fetching && !ui.isMobile" class="lm-faint prog">{{ $t('erd.fetching', { done: progress.done, total: progress.total }) }}</span>
      <template v-if="!ui.isMobile">
        <Menu :items="displayItems" align="end"><Btn size="sm" variant="ghost" icon="view">{{ $t('erd.display') }}</Btn></Menu>
        <span class="sep" />
      </template>
      <IconBtn icon="zoom-out" :size="16" :label="$t('erd.zoomOut')" :disabled="!hasSvg" @click="zoomAt(1 / 1.25)" />
      <button v-if="!ui.isMobile" type="button" class="pct" :disabled="!hasSvg" :title="$t('erd.resetZoom')" @click="resetZoom">{{ zoomPct }}</button>
      <IconBtn icon="zoom-in" :size="16" :label="$t('erd.zoomIn')" :disabled="!hasSvg" @click="zoomAt(1.25)" />
      <IconBtn icon="fit" :size="16" :label="$t('erd.fit')" :disabled="!hasSvg" @click="fit" />
      <template v-if="!ui.isMobile">
        <span class="sep" />
        <IconBtn icon="refresh" :size="16" :label="$t('common.refresh')" :disabled="busy || !conn" @click="refresh" />
        <Menu :items="exportItems" align="end"><Btn size="sm" variant="ghost" icon="download">{{ $t('erd.export') }}</Btn></Menu>
      </template>
      <Menu v-else :items="mobileItems" align="end"><IconBtn icon="more" :size="16" :label="$t('common.more')" /></Menu>
    </div>

    <div class="main">
      <aside v-show="panelOpen" class="panel">
        <div class="panel-head">
          <div class="search"><Icon name="search" :size="14" /><input v-model="filter" :placeholder="$t('erd.filter')" spellcheck="false" /></div>
          <div class="panel-acts">
            <button type="button" class="link" @click="selectAll">{{ $t('erd.selectAll') }}</button>
            <button type="button" class="link" @click="selectNone">{{ $t('erd.selectNone') }}</button>
          </div>
          <p v-if="limited" class="notice">{{ $t('erd.limited', { total: allTables.length, n: DEFAULT_LIMIT }) }}</p>
        </div>
        <div class="list">
          <p v-if="listError" class="list-err">{{ listError }}</p>
          <p v-else-if="listLoading && !allTables.length" class="lm-empty">{{ $t('common.loading') }}</p>
          <p v-else-if="!visibleTables.length" class="lm-empty">{{ allTables.length ? $t('erd.noMatch') : $t('erd.noTables') }}</p>
          <label v-for="n in visibleTables" :key="n" class="row" :class="{ bad: failed[n] }" :title="failed[n] || n">
            <input type="checkbox" class="ck" :checked="selSet.has(n)" @change="toggle(n)" />
            <span class="lm-ellipsis">{{ n }}</span>
            <Icon v-if="failed[n] && selSet.has(n)" name="warning" :size="14" class="warn" />
          </label>
        </div>
        <div v-if="ui.isMobile" class="panel-foot"><Btn size="sm" block @click="panelOpen = false">{{ $t('common.done') }}</Btn></div>
      </aside>

      <div ref="viewport" class="viewport" :class="{ grabbing: dragging, ready: hasSvg }" @wheel="onWheel" @pointerdown="onPointerDown" @pointermove="onPointerMove"
        @pointerup="onPointerUp" @pointercancel="onPointerUp">
        <div ref="host" class="canvas" :class="{ dim: busy }" :style="{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.k})` }" />
        <div v-if="connMissing" class="state"><Icon name="plug" :size="28" :stroke="1.3" /><p>{{ $t('erd.connMissing') }}</p></div>
        <div v-else-if="renderError" class="state">
          <Icon name="error" :size="28" :stroke="1.3" class="err-icon" /><p class="err">{{ renderError }}</p>
          <Btn size="sm" icon="copy" @click="copySource">{{ $t('erd.copySource') }}</Btn>
        </div>
        <div v-else-if="listLoaded && !allTables.length && !listLoading" class="state"><Icon name="erd" :size="28" :stroke="1.3" /><p>{{ $t('erd.noTables') }}</p></div>
        <div v-else-if="listLoaded && !selected.length" class="state"><Icon name="erd" :size="28" :stroke="1.3" /><p>{{ $t('erd.noneSelected') }}</p></div>
        <div v-else-if="!hasSvg && (busy || listLoading || !listLoaded)" class="state"><span class="spin" /><p>{{ fetching ? $t('erd.fetching', { done: progress.done, total: progress.total }) : $t('erd.rendering') }}</p></div>
        <div v-if="failedCount && !renderError" class="failed" :title="failedText"><Icon name="warning" :size="14" />{{ $t('erd.failed', { n: failedCount }) }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.erd { display: flex; flex-direction: column; flex: 1; min-height: 0; height: 100%; min-width: 0; }
.ticon { color: var(--text-2); margin: 0 2px; }
.title { font-weight: 500; font-size: 13px; min-width: 0; }
.count { font-size: 12px; font-variant-numeric: tabular-nums; flex: none; }
.prog { font-size: 12px; white-space: nowrap; }
.pct { border: 0; background: none; color: var(--text-2); font-size: 12px; min-width: 44px; height: 28px; border-radius: var(--radius-m); font-variant-numeric: tabular-nums; }
.pct:hover:not(:disabled) { background: var(--bg-hover); color: var(--text); }
.main { flex: 1; min-height: 0; display: flex; position: relative; }
.panel { width: 240px; flex: none; border-right: 1px solid var(--border); display: flex; flex-direction: column; min-height: 0; background: var(--bg); }
.panel-head { padding: 8px; border-bottom: 1px solid var(--border); display: flex; flex-direction: column; gap: 6px; }
.search { display: flex; align-items: center; gap: 6px; height: 30px; padding: 0 8px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); color: var(--text-3); }
.search:focus-within { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.search input { flex: 1; min-width: 0; border: 0; outline: none; background: transparent; font-size: 13px; }
.panel-acts { display: flex; gap: 12px; }
.link { border: 0; background: none; padding: 0; color: var(--accent); font-size: 12.5px; }
.link:hover { text-decoration: underline; }
.notice { margin: 0; font-size: 12px; color: var(--warning); line-height: 1.45; }
.list { flex: 1; min-height: 0; overflow: auto; padding: 4px; }
.list-err { margin: 8px; font-size: 12.5px; color: var(--danger); word-break: break-word; }
.row { display: flex; align-items: center; gap: 8px; height: 28px; padding: 0 6px; border-radius: var(--radius-s); font-size: 13px; cursor: pointer; min-width: 0; }
.row:hover { background: var(--bg-hover); }
.row .lm-ellipsis { flex: 1; min-width: 0; }
.row.bad { color: var(--text-2); }
.warn { color: var(--warning); }
.ck { width: 15px; height: 15px; accent-color: var(--accent); margin: 0; flex: none; }
.panel-foot { padding: 8px; border-top: 1px solid var(--border); }
.viewport { flex: 1; min-width: 0; position: relative; overflow: hidden; background: var(--bg); touch-action: none; user-select: none; }
.viewport.ready { cursor: grab; }
.viewport.grabbing { cursor: grabbing; }
.canvas { position: absolute; left: 0; top: 0; transform-origin: 0 0; transition: opacity .15s; }
.canvas.dim { opacity: .5; }
.canvas :deep(svg) { display: block; }
.state { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px; color: var(--text-3); padding: 24px; text-align: center; pointer-events: none; }
.state > * { pointer-events: auto; }
.state p { margin: 0; font-size: 13px; max-width: 520px; word-break: break-word; }
.state .err { color: var(--danger); white-space: pre-wrap; }
.err-icon { color: var(--danger); }
.failed { position: absolute; left: 12px; bottom: 12px; display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--warning); background: var(--bg-elev); box-shadow: var(--shadow-soft); border-radius: var(--radius-m); padding: 4px 8px; max-width: calc(100% - 24px); }
.spin { width: 18px; height: 18px; border-radius: 50%; border: 2px solid var(--border-strong); border-right-color: transparent; animation: spin .7s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.erd.mobile .panel { position: absolute; z-index: 5; left: 0; right: 0; top: 0; width: auto; max-height: 70%; border-right: 0; border-bottom: 1px solid var(--border); box-shadow: var(--shadow-pop); }
</style>
