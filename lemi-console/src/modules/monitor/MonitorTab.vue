<script setup lang="ts">
// 服务器监控：MySQL 分进程 / 状态 / 变量 / 表大小 / 慢查询；SQLite 分文件状态 / 编译选项 / 表大小。
// 各分区只在显示时挂载；自动刷新的分区在标签不活动时暂停。
import { computed, ref, watch } from 'vue'
import type { Tab } from '@/modules/registry'
import Icon from '@/components/icons/Icon.vue'
import Segmented from '@/components/ui/Segmented.vue'
import ProcessesPane from './ProcessesPane.vue'
import StatusPane from './StatusPane.vue'
import VariablesPane from './VariablesPane.vue'
import TableSizesPane from './TableSizesPane.vue'
import SlowPane from './SlowPane.vue'
import SqliteStatusPane from './SqliteStatusPane.vue'
import { useConns } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { useUI } from '@/stores/ui'
import { dialectOf } from '@/lib/dialect'
import { i18n } from '@/i18n'

type Section = 'processes' | 'status' | 'variables' | 'tables' | 'slow'

const props = defineProps<{ tab: Tab; active: boolean }>()
const conns = useConns()
const tabs = useTabs()
const ui = useUI()
const t = i18n.global.t

const conn = computed(() => conns.byId[props.tab.params.connectionId])
const connMissing = computed(() => !conn.value && !conns.loading && conns.list.length > 0)
const sqlite = computed(() => dialectOf(conn.value) === 'sqlite')
const canKill = computed(() => conn.value?.access === 'manage')

const sections = computed(() => sqlite.value
  ? [{ value: 'status', label: t('monitor.sec.file') }, { value: 'variables', label: t('monitor.sec.compile') }, { value: 'tables', label: t('monitor.sec.tables') }]
  : [
    { value: 'processes', label: t('monitor.sec.processes') }, { value: 'status', label: t('monitor.sec.status') }, { value: 'variables', label: t('monitor.sec.variables') },
    { value: 'tables', label: t('monitor.sec.tables') }, { value: 'slow', label: t('monitor.sec.slow') },
  ])

const section = ref<Section>((props.tab.params.section as Section) || '')
// 连接加载完后再决定默认分区（SQLite 没有进程列表与慢查询）
watch([conn, sections], () => {
  if (!conn.value) return
  if (!sections.value.some((s) => s.value === section.value)) section.value = sections.value[0].value as Section
}, { immediate: true })
watch(section, (s) => tabs.setParams(props.tab.id, { section: s }))

// 刷新页面后恢复的标签在第一次切到前台时才开始取数
const everActive = ref(props.active)
watch(() => props.active, (a) => { if (a) everActive.value = true })
</script>

<template>
  <div class="monitor lm-monitor">
    <div class="lm-toolbar">
      <Icon name="pulse" :size="16" class="ticon" />
      <span class="title lm-ellipsis">{{ conn?.name || '' }}</span>
      <span v-if="conn?.env === 'prod'" class="lm-badge danger">{{ $t('explorer.env.prod') }}</span>
      <span class="lm-faint kind">{{ sqlite ? $t('monitor.titleSqlite') : $t('monitor.title') }}</span>
    </div>
    <div v-if="conn" class="subbar"><div class="seg-wrap"><Segmented v-model="section" :options="sections" /></div></div>
    <div class="body">
      <div v-if="connMissing" class="state"><Icon name="plug" :size="28" :stroke="1.3" /><p>{{ $t('monitor.connMissing') }}</p></div>
      <div v-else-if="!conn" class="state"><span class="spin" /><p>{{ $t('common.loading') }}</p></div>
      <div v-else-if="everActive" class="content">
        <ProcessesPane v-if="section === 'processes'" :tab="tab" :conn-id="conn.id" :conn-name="conn.name" :active="active" :can-kill="canKill" :prod="conn.env === 'prod'" :mobile="ui.isMobile" />
        <template v-else-if="section === 'status'">
          <SqliteStatusPane v-if="sqlite" :conn-id="conn.id" />
          <StatusPane v-else :conn-id="conn.id" :active="active" />
        </template>
        <VariablesPane v-else-if="section === 'variables'" :key="conn.id" :conn-id="conn.id" :placeholder="sqlite ? $t('monitor.searchCompile') : $t('monitor.searchVars')" />
        <TableSizesPane v-else-if="section === 'tables'" :conn-id="conn.id" :mobile="ui.isMobile" :sqlite="sqlite" />
        <SlowPane v-else-if="section === 'slow'" :conn-id="conn.id" :mobile="ui.isMobile" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.monitor { display: flex; flex-direction: column; flex: 1; min-height: 0; height: 100%; min-width: 0; }
.ticon { color: var(--text-2); margin: 0 2px 0 4px; }
.title { font-weight: 500; font-size: 13px; min-width: 0; }
.kind { font-size: 12px; white-space: nowrap; }
.subbar { flex: none; display: flex; align-items: center; padding: 6px 12px; border-bottom: 1px solid var(--border); min-width: 0; }
.seg-wrap { min-width: 0; overflow-x: auto; scrollbar-width: none; }
.seg-wrap::-webkit-scrollbar { display: none; }
.body { flex: 1; min-height: 0; overflow: auto; position: relative; }
.content { padding: 12px 16px 40px; min-width: 0; }
.state { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px; color: var(--text-3); padding: 24px; text-align: center; }
.state p { margin: 0; font-size: 13px; }
.spin { width: 18px; height: 18px; border-radius: 50%; border: 2px solid var(--border-strong); border-right-color: transparent; animation: spin .7s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (max-width: 760px) { .content { padding: 10px 10px 40px; } .subbar { padding: 6px 10px; } }
</style>

<style>
/* 监控图表的颜色角色（数据可视化色板第 1、2 槽，深浅主题各一套，已按色盲分离度与对比度校验） */
.lm-monitor {
  --viz-1: #2a78d6;
  --viz-2: #eb6834;
  --viz-track: #e3eefb;
  --viz-surface: var(--bg);
  --viz-grid: rgba(55, 53, 47, .08);
  --viz-axis: rgba(55, 53, 47, .22);
  --viz-label: var(--text-3);
  --viz-tip-bg: var(--bg-elev);
  --viz-tip-text: var(--text);
  --viz-tip-muted: var(--text-2);
}
html[data-theme="dark"] .lm-monitor {
  --viz-1: #3987e5;
  --viz-2: #d95926;
  --viz-track: #1d2a3a;
  --viz-grid: rgba(255, 255, 255, .07);
  --viz-axis: rgba(255, 255, 255, .2);
}
</style>
