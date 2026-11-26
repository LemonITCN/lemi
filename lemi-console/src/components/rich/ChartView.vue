<script setup lang="ts">
// ECharts 图表：option 来自服务端（已清洗、dataset 已填好）。按需加载 echarts，3D 图再加载 echarts-gl；
// 跟随深浅主题；容器尺寸变化时 resize；可下载 PNG。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = withDefaults(defineProps<{ option: Record<string, any>; gl?: boolean; height?: number }>(), { height: 300 })
const el = ref<HTMLDivElement>()
const error = ref('')
let chart: any = null
let echartsMod: any = null
let ro: ResizeObserver | null = null
let mo: MutationObserver | null = null

function dark() { return document.documentElement.dataset.theme === 'dark' }

async function render() {
  if (!el.value) return
  error.value = ''
  try {
    if (!echartsMod) {
      echartsMod = await import('echarts')
      if (props.gl) await import('echarts-gl')
    } else if (props.gl) {
      await import('echarts-gl')
    }
    chart?.dispose()
    chart = echartsMod.init(el.value, dark() ? 'dark' : undefined, { renderer: 'canvas' })
    const opt = JSON.parse(JSON.stringify(props.option || {}))
    opt.backgroundColor = 'transparent'
    chart.setOption(opt, true)
  } catch (e: any) {
    error.value = e?.message || String(e)
  }
}

/** 导出 PNG（数据地址）。 */
function dataURL(): string {
  if (!chart) return ''
  const bg = getComputedStyle(document.body).getPropertyValue('--bg').trim() || '#fff'
  return chart.getDataURL({ type: 'png', pixelRatio: 2, backgroundColor: bg })
}
defineExpose({ dataURL })

onMounted(() => {
  render()
  ro = new ResizeObserver(() => chart?.resize())
  ro.observe(el.value!)
  mo = new MutationObserver(() => render())
  mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
})
onBeforeUnmount(() => {
  ro?.disconnect()
  mo?.disconnect()
  chart?.dispose()
  chart = null
})
watch(() => props.option, render, { deep: false })
</script>

<template>
  <div class="cv">
    <div ref="el" class="canvas" :style="{ height: height + 'px' }" />
    <div v-if="error" class="err">{{ error }}</div>
  </div>
</template>

<style scoped>
.cv { position: relative; width: 100%; }
.canvas { width: 100%; }
.err { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; font-size: 12px; color: var(--danger); padding: 12px; text-align: center; }
</style>
