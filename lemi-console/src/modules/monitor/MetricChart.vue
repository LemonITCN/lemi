<script setup lang="ts">
// 监控曲线：ECharts 按需注册的折线图。颜色全部从 CSS 变量读取（.lm-monitor 上定义，深浅主题各一套），
// 主题切换时重设；容器尺寸变化时 resize；卸载时 dispose。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { formatClock } from './format'

echarts.use([LineChart, GridComponent, TooltipComponent, CanvasRenderer])

export interface MetricSeries { name: string; slot: 1 | 2; data: [number, number | null][] }

const props = withDefaults(defineProps<{ series: MetricSeries[]; format: (v: number) => string; axisFormat?: (v: number) => string; min?: number; max?: number; height?: number }>(), { height: 120 })

const el = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null
let ro: ResizeObserver | null = null
let mo: MutationObserver | null = null

function cssVar(name: string): string {
  return el.value ? getComputedStyle(el.value).getPropertyValue(name).trim() : ''
}

function esc(s: string) {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!)
}

function option() {
  const c = {
    s1: cssVar('--viz-1'), s2: cssVar('--viz-2'), grid: cssVar('--viz-grid'), axis: cssVar('--viz-axis'), label: cssVar('--viz-label'),
    tipBg: cssVar('--viz-tip-bg'), tipText: cssVar('--viz-tip-text'), tipMuted: cssVar('--viz-tip-muted'), surface: cssVar('--viz-surface'),
  }
  const single = props.series.length === 1
  return {
    animation: false,
    grid: { left: 2, right: 10, top: 10, bottom: 2, containLabel: true },
    tooltip: {
      trigger: 'axis',
      confine: true,
      backgroundColor: c.tipBg,
      borderWidth: 0,
      padding: [8, 10],
      textStyle: { color: c.tipText, fontSize: 12 },
      extraCssText: 'box-shadow: var(--shadow-pop); border-radius: 6px;',
      axisPointer: { type: 'line', lineStyle: { color: c.axis, width: 1, type: 'solid' } },
      formatter: (params: any) => {
        const list = Array.isArray(params) ? params : [params]
        if (!list.length) return ''
        const at = Number(list[0].value?.[0] ?? list[0].axisValue)
        const rows = list.map((p: any) => {
          const v = p.value?.[1]
          const color = props.series[p.seriesIndex]?.slot === 2 ? c.s2 : c.s1
          return `<div style="display:flex;align-items:center;gap:8px;margin-top:4px">`
            + `<span style="width:12px;height:2px;border-radius:1px;background:${color}"></span>`
            + `<b style="font-weight:600;color:${c.tipText}">${v === null || v === undefined ? '—' : esc(props.format(Number(v)))}</b>`
            + `<span style="color:${c.tipMuted}">${esc(p.seriesName)}</span></div>`
        }).join('')
        return `<div style="color:${c.tipMuted};font-size:11.5px">${formatClock(at)}</div>${rows}`
      },
    },
    xAxis: {
      type: 'time',
      boundaryGap: false,
      splitNumber: 3,
      axisLine: { lineStyle: { color: c.axis } },
      axisTick: { show: false },
      splitLine: { show: false },
      axisLabel: { color: c.label, fontSize: 10.5, hideOverlap: true, formatter: (v: number) => formatClock(v, false) },
    },
    yAxis: {
      type: 'value',
      min: props.min,
      max: props.max,
      splitNumber: 3,
      axisLine: { show: false },
      axisTick: { show: false },
      splitLine: { lineStyle: { color: c.grid, width: 1, type: 'solid' } },
      axisLabel: { color: c.label, fontSize: 10.5, formatter: (v: number) => (props.axisFormat || props.format)(v) },
    },
    series: props.series.map((s) => {
      const color = s.slot === 2 ? c.s2 : c.s1
      return {
        type: 'line',
        name: s.name,
        data: s.data,
        showSymbol: false,
        symbol: 'circle',
        symbolSize: 8,
        connectNulls: false,
        lineStyle: { width: 2, color, cap: 'round', join: 'round' },
        itemStyle: { color, borderColor: c.surface, borderWidth: 2 },
        emphasis: { disabled: true },
        // 单条曲线加 10% 的面积底色，多条只画线
        areaStyle: single ? { color, opacity: 0.1 } : undefined,
      }
    }),
  }
}

function apply(full = false) {
  if (!chart) return
  chart.setOption(option() as any, { notMerge: full, lazyUpdate: true })
}

onMounted(() => {
  if (!el.value) return
  chart = echarts.init(el.value, undefined, { renderer: 'canvas' })
  apply(true)
  ro = new ResizeObserver(() => chart?.resize())
  ro.observe(el.value)
  mo = new MutationObserver(() => apply(true))
  mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'data-accent'] })
})

watch(() => props.series, () => apply(), { deep: true })

onBeforeUnmount(() => {
  ro?.disconnect()
  mo?.disconnect()
  chart?.dispose()
  chart = null
})
</script>

<template>
  <div ref="el" class="metric-chart" :style="{ height: height + 'px' }" />
</template>

<style scoped>
.metric-chart { width: 100%; min-width: 0; }
</style>
