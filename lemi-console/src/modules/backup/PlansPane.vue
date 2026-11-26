<script setup lang="ts">
// 计划：按连接分组列出备份计划与最近一次结果、下次运行；运行中显示实时进度。
import { computed } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Icon from '@/components/icons/Icon.vue'
import Menu from '@/components/ui/Menu.vue'
import ProgressBar from './ProgressBar.vue'
import { useConns } from '@/stores/conns'
import { useSession } from '@/stores/session'
import { formatDate, formatRelative } from '@/lib/time'
import { describe } from '@/modules/agent-tasks/schedule'
import { i18n } from '@/i18n'
import { formatBytes, live, statusClass, type Plan, type Run } from './api'

const props = defineProps<{ plans: Plan[]; lastRuns: Record<string, Run>; connFilter: string; loaded: boolean }>()
const emit = defineEmits<{ edit: [p: Plan]; create: [connId: string]; run: [p: Plan]; remove: [p: Plan]; openRun: [id: string]; showRuns: [p: Plan] }>()
const conns = useConns()
const session = useSession()
const t = i18n.global.t

const groups = computed(() => {
  const ids = new Set<string>(props.plans.map((p) => p.connection_id))
  if (session.atLeast('admin')) for (const c of conns.list) if (c.driver === 'mysql' || c.driver === 'sqlite') ids.add(c.id)
  let list = [...ids].map((id) => ({ conn: conns.byId[id], id, plans: props.plans.filter((p) => p.connection_id === id) }))
  if (props.connFilter) list = list.filter((g) => g.id === props.connFilter)
  return list.filter((g) => g.conn || g.plans.length).sort((a, b) => (a.conn?.name || '').localeCompare(b.conn?.name || ''))
})

function running(p: Plan) {
  return Object.values(live).find((x) => x.kind === 'backup' && x.plan_id === p.id && !x.done)
}
function planMenu(p: Plan) {
  const items: any[] = [{ label: t('backup.viewRuns'), icon: 'history', run: () => emit('showRuns', p) }]
  if (session.atLeast('admin')) {
    items.unshift({ label: t('common.edit'), icon: 'edit', run: () => emit('edit', p) })
    items.push({ separator: true }, { label: t('common.delete'), icon: 'trash', danger: true, run: () => emit('remove', p) })
  }
  return items
}
</script>

<template>
  <div class="pp">
    <div v-if="loaded && !groups.length" class="lm-empty big">
      <Icon name="archive" :size="28" />
      <div>{{ $t('backup.noPlans') }}</div>
      <div class="lm-faint">{{ $t('backup.noPlansHint') }}</div>
      <Btn v-if="session.atLeast('admin')" size="sm" icon="plus" @click="emit('create', '')">{{ $t('backup.newPlan') }}</Btn>
    </div>
    <div v-for="g in groups" :key="g.id" class="grp">
      <div class="gh">
        <Icon :name="g.conn?.driver === 'sqlite' ? 'file' : 'server'" :size="15" />
        <span class="gname lm-ellipsis">{{ g.conn?.name || g.id }}</span>
        <span v-if="conns.health[g.id]" class="hdot" :class="conns.health[g.id].status" />
        <span v-if="g.conn?.env === 'prod'" class="lm-badge danger">PROD</span>
        <span class="lm-spacer" />
        <IconBtn v-if="session.atLeast('admin') && g.conn" icon="plus" :size="15" :label="$t('backup.newPlan')" @click="emit('create', g.id)" />
      </div>
      <div v-if="!g.plans.length" class="none">
        <span>{{ $t('backup.noPlanForConn') }}</span>
        <button type="button" class="link" @click="emit('create', g.id)">{{ $t('backup.addPlanForConn') }}</button>
      </div>
      <div v-for="p in g.plans" :key="p.id" class="plan" :class="{ off: !p.enabled }">
        <div class="pmain" @click="emit('showRuns', p)">
          <div class="pt">
            <span class="pname lm-ellipsis">{{ p.name }}</span>
            <span class="lm-badge">{{ $t('backup.method.' + p.method) }}</span>
            <Icon v-if="p.encrypt" name="lock" :size="12" class="lk" />
            <span v-if="!p.enabled" class="lm-badge">{{ $t('backup.disabled') }}</span>
          </div>
          <div class="ps lm-ellipsis">
            <Icon name="clock" :size="12" />{{ p.schedule.kind === 'manual' ? $t('backup.manualOnly') : describe(p.schedule) }}
            <template v-if="p.enabled && p.next_run_at"> · {{ $t('backup.nextRun', { t: formatDate(p.next_run_at, true) }) }}</template>
            <template v-if="p.retry_at"> · <span class="warn">{{ $t('backup.retryAt', { n: p.retry_attempt, t: formatDate(p.retry_at, true) }) }}</span></template>
            <template v-if="p.databases.length"> · {{ p.databases.join(', ') }}</template>
          </div>
          <ProgressBar v-if="running(p)" :p="running(p)!" compact class="prog" />
        </div>
        <button v-if="lastRuns[p.id]" type="button" class="last" @click="emit('openRun', lastRuns[p.id].id)">
          <span class="lm-badge" :class="statusClass[lastRuns[p.id].status]">{{ $t('backup.status.' + lastRuns[p.id].status) }}</span>
          <span class="lt">{{ formatRelative(lastRuns[p.id].started_at) }}<template v-if="lastRuns[p.id].size"> · {{ formatBytes(lastRuns[p.id].size) }}</template></span>
          <span v-if="lastRuns[p.id].verify_status" class="vs" :class="statusClass[lastRuns[p.id].verify_status]" :title="$t('backup.verify.' + lastRuns[p.id].verify_status)"><Icon :name="lastRuns[p.id].verify_status === 'ok' ? 'check' : lastRuns[p.id].verify_status === 'failed' ? 'warning' : 'clock'" :size="12" /></span>
        </button>
        <span v-else class="last never">{{ $t('backup.neverRun') }}</span>
        <div class="acts">
          <Btn v-if="session.atLeast('member')" size="sm" :icon="running(p) || p.busy ? undefined : 'play'" :loading="!!running(p) || !!p.busy" :disabled="!!running(p) || !!p.busy" @click="emit('run', p)">{{ $t('backup.runNow') }}</Btn>
          <Menu :items="planMenu(p)" align="end"><IconBtn icon="more" :size="15" :label="$t('common.more')" /></Menu>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.pp { display: flex; flex-direction: column; gap: 14px; }
.big { display: flex; flex-direction: column; align-items: center; gap: 8px; margin-top: 8vh; }
.grp { border: 1px solid var(--border); border-radius: var(--radius-l); background: var(--bg); overflow: hidden; }
.gh { display: flex; align-items: center; gap: 8px; padding: 8px 10px 8px 12px; background: var(--bg-soft); border-bottom: 1px solid var(--border); color: var(--text-2); }
.gname { font-size: 13px; font-weight: 600; color: var(--text); }
.hdot { width: 7px; height: 7px; border-radius: 50%; background: var(--text-3); flex: none; }
.hdot.up { background: var(--success); }
.hdot.down { background: var(--danger); }
.none { padding: 12px; font-size: 12.5px; color: var(--text-3); display: flex; gap: 8px; }
.link { border: 0; background: none; color: var(--accent); padding: 0; font-size: 12.5px; }
.plan { display: flex; align-items: center; gap: 12px; padding: 10px 12px; border-bottom: 1px solid var(--border); }
.plan:last-child { border-bottom: 0; }
.plan.off .pname { color: var(--text-3); }
.pmain { flex: 1; min-width: 0; cursor: pointer; }
.pt { display: flex; align-items: center; gap: 6px; min-width: 0; }
.pname { font-size: 13.5px; font-weight: 500; }
.lk { color: var(--text-3); flex: none; }
.ps { font-size: 12px; color: var(--text-3); margin-top: 3px; display: flex; align-items: center; gap: 4px; }
.ps :deep(svg) { flex: none; }
.warn { color: var(--warning); }
.prog { margin-top: 6px; max-width: 420px; }
.last { display: inline-flex; align-items: center; gap: 6px; border: 0; background: none; padding: 4px 6px; border-radius: var(--radius-m); font-size: 12px; color: var(--text-2); flex: none; }
.last:hover { background: var(--bg-hover); }
.last.never { color: var(--text-3); }
.vs { display: inline-flex; }
.vs.success { color: var(--success); }
.vs.danger { color: var(--danger); }
.vs.accent { color: var(--accent); }
.acts { display: flex; align-items: center; gap: 4px; flex: none; }
@media (max-width: 760px) {
  .plan { flex-wrap: wrap; gap: 8px; }
  .pmain { flex-basis: 100%; }
  .last { padding-left: 0; }
  .acts { margin-left: auto; }
}
</style>
