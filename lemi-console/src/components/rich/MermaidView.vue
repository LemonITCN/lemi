<script setup lang="ts">
// Mermaid 图：securityLevel=strict、不用 HTML 标签；跟随深浅主题。可放大查看与下载 SVG。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = withDefaults(defineProps<{ code: string; maxHeight?: string }>(), { maxHeight: '420px' })
const host = ref<HTMLDivElement>()
const error = ref('')
let seq = 0
let mo: MutationObserver | null = null

function dark() { return document.documentElement.dataset.theme === 'dark' }

async function render() {
  const el = host.value
  if (!el) return
  const my = ++seq
  error.value = ''
  try {
    const { default: mermaid } = await import('mermaid')
    mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: dark() ? 'dark' : 'default', htmlLabels: false,
      fontFamily: getComputedStyle(document.body).fontFamily, flowchart: { htmlLabels: false }, er: { useMaxWidth: true } })
    const id = 'mm-' + Math.random().toString(36).slice(2)
    const { svg } = await mermaid.render(id, props.code)
    if (my !== seq) return
    el.innerHTML = svg
  } catch (e: any) {
    if (my !== seq) return
    el.innerHTML = ''
    error.value = e?.message || String(e)
  }
}

/** 当前渲染出的 SVG 文本（导出用）。 */
function svgText(): string { return host.value?.querySelector('svg')?.outerHTML || '' }
defineExpose({ svgText })

onMounted(() => {
  render()
  mo = new MutationObserver(() => render())
  mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
})
onBeforeUnmount(() => mo?.disconnect())
watch(() => props.code, render)
</script>

<template>
  <div class="mv">
    <div ref="host" class="svg" :style="{ maxHeight }" />
    <pre v-if="error" class="err">{{ error }}</pre>
  </div>
</template>

<style scoped>
.mv { width: 100%; }
.svg { overflow: auto; display: flex; justify-content: center; }
.svg :deep(svg) { max-width: 100%; height: auto; }
.err { margin: 0; font-size: 12px; color: var(--danger); white-space: pre-wrap; padding: 8px; }
</style>
