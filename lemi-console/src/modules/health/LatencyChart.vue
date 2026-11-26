<script setup lang="ts">
// 延迟曲线：每个时间段的平均与 P95 延迟（只统计成功的检查）。颜色取 CSS 变量，主题切换时重绘。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { formatDate } from '@/lib/time'

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

export interface LatPoint { t: number; avg: number | null; p95: number | null }
const props = defineProps<{ points: LatPoint[]; hourly: boolean; labels: { avg: string; p95: string } }>()

const el = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null
let ro: ResizeObserver | null = null
let mo: MutationObserver | null = null

function css(n: string) { return el.value ? getComputedStyle(el.value).getPropertyValue(n).trim() : '' }
function esc(s: string) { return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!) }
function fmtT(ms: number) {
  const d = new Date(ms)
  return props.hourly ? `${String(d.getHours()).padStart(2, '0')}:00` : formatDate(ms).slice(5)
}

function option() {
  const c = { s1: css('--viz-1'), s2: css('--viz-2'), grid: css('--viz-grid'), axis: css('--viz-axis'), label: css('--viz-label'), tipBg: css('--viz-tip-bg'), tipText: css('--viz-tip-text'), tipMuted: css('--viz-tip-muted') }
  return {
    animation: false,
    grid: { left: 4, right: 12, top: 28, bottom: 4, containLabel: true },
    legend: { top: 0, right: 0, itemWidth: 12, itemHeight: 2, icon: 'rect', textStyle: { color: c.label, fontSize: 11.5 } },
    tooltip: {
      trigger: 'axis', confine: true, backgroundColor: c.tipBg, borderWidth: 0, padding: [8, 10], textStyle: { color: c.tipText, fontSize: 12 },
      extraCssText: 'box-shadow: var(--shadow-pop); border-radius: 6px;', axisPointer: { type: 'line', lineStyle: { color: c.axis, width: 1 } },
      formatter: (ps: any) => {
        const list = Array.isArray(ps) ? ps : [ps]
        if (!list.length) return ''
        const at = Number(list[0].value?.[0])
        const rows = list.map((p: any) => {
          const v = p.value?.[1]
          return `<div style="display:flex;align-items:center;gap:8px;margin-top:4px"><span style="width:12px;height:2px;background:${p.seriesIndex ? c.s2 : c.s1}"></span>`
            + `<b style="font-weight:600">${v === null || v === undefined ? '—' : esc(String(v)) + ' ms'}</b><span style="color:${c.tipMuted}">${esc(p.seriesName)}</span></div>`
        }).join('')
        return `<div style="color:${c.tipMuted};font-size:11.5px">${esc(props.hourly ? formatDate(at, true) : formatDate(at))}</div>${rows}`
      },
    },
    xAxis: { type: 'time', axisLine: { lineStyle: { color: c.axis } }, axisTick: { show: false }, splitLine: { show: false },
      axisLabel: { color: c.label, fontSize: 11, hideOverlap: true, formatter: (v: number) => fmtT(v) } },
    yAxis: { type: 'value', min: 0, splitNumber: 3, axisLabel: { color: c.label, fontSize: 11, formatter: (v: number) => `${v} ms` }, splitLine: { lineStyle: { color: c.grid } } },
    series: [
      { name: props.labels.avg, type: 'line', showSymbol: true, symbol: 'circle', symbolSize: 4, connectNulls: false, lineStyle: { width: 2, color: c.s1 }, itemStyle: { color: c.s1 }, data: props.points.map((p) => [p.t, p.avg]) },
      { name: props.labels.p95, type: 'line', showSymbol: true, symbol: 'circle', symbolSize: 3, connectNulls: false, lineStyle: { width: 1.5, color: c.s2, type: 'dashed' }, itemStyle: { color: c.s2 }, data: props.points.map((p) => [p.t, p.p95]) },
    ],
  }
}
function render() { chart?.setOption(option() as any, true) }
onMounted(() => {
  if (!el.value) return
  chart = echarts.init(el.value)
  render()
  ro = new ResizeObserver(() => chart?.resize())
  ro.observe(el.value)
  mo = new MutationObserver(render)
  mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'data-accent'] })
})
watch(() => [props.points, props.hourly], render, { deep: true })
onBeforeUnmount(() => { ro?.disconnect(); mo?.disconnect(); chart?.dispose(); chart = null })
</script>

<template>
  <div ref="el" class="lc" />
</template>

<style scoped>
.lc { height: 180px; width: 100%; }
</style>
