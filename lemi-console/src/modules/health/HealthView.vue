<script setup lang="ts">
// 健康检查页（/health/:id?）：左边连接列表（状态、延迟），右边所选连接的色块时间线、延迟曲线、可用率、最近的检查与设置。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Btn from '@/components/ui/Btn.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Segmented from '@/components/ui/Segmented.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Switch from '@/components/ui/Switch.vue'
import Icon from '@/components/icons/Icon.vue'
import LatencyChart, { type LatPoint } from './LatencyChart.vue'
import ChannelPicker from '@/modules/notify/ChannelPicker.vue'
import { api } from '@/api/client'
import { on } from '@/lib/realtime'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { formatDate, formatRelative } from '@/lib/time'
import { errorText, i18n } from '@/i18n'
import { loadHealth } from './index'

const route = useRoute()
const router = useRouter()
const session = useSession()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t

const items = ref<any[]>([])
const loaded = ref(false)
const id = computed(() => String(route.params.id || ''))
const current = computed(() => items.value.find((x) => x.connection_id === id.value))
const range = ref<'day' | '7d' | '30d'>('day')
const ranges = computed(() => (['day', '7d', '30d'] as const).map((r) => ({ value: r, label: t('health.range.' + r) })))
const tl = ref<any>(null)
const checks = ref<any[]>([])
const checksCursor = ref(0)
const onlyFailed = ref(false)
const cfg = ref<any>(null)
const cfgCustom = ref(false)
const saving = ref(false)
const checking = ref(false)

async function loadList() {
  const ws = session.current?.id
  if (!ws) return
  try {
    items.value = (await api.get(`/workspaces/${ws}/health`)).connections || []
    if (!id.value && items.value.length && !ui.isMobile) router.replace('/health/' + items.value[0].connection_id)
  } catch (e) { toast.error(errorText(e)) } finally { loaded.value = true }
}
async function loadTimeline() {
  if (!id.value) return
  try {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || ''
    tl.value = await api.get(`/workspaces/${session.current?.id}/health/${id.value}/timeline?range=${range.value}&tz=${encodeURIComponent(tz)}`)
  } catch (e) { tl.value = null; toast.error(errorText(e)) }
}
async function loadChecks(reset = true) {
  if (!id.value) return
  try {
    const q = new URLSearchParams({ limit: '30' })
    if (onlyFailed.value) q.set('failed', '1')
    if (!reset && checksCursor.value) q.set('cursor', String(checksCursor.value))
    const r = await api.get(`/workspaces/${session.current?.id}/health/${id.value}/checks?${q}`)
    checks.value = reset ? r.checks || [] : [...checks.value, ...(r.checks || [])]
    checksCursor.value = r.next_cursor || 0
  } catch (e) { toast.error(errorText(e)) }
}
async function loadConfig() {
  if (!id.value) return
  try {
    const r = await api.get(`/workspaces/${session.current?.id}/health/${id.value}/config`)
    cfg.value = { ...r.config, channels: [...(r.config.channels || [])] }
    cfgCustom.value = !!r.custom
  } catch (e) { cfg.value = null }
}
function loadDetail() { tl.value = null; checks.value = []; cfg.value = null; loadTimeline(); loadChecks(); loadConfig() }

onMounted(() => { loadList(); loadDetail() })
watch(id, loadDetail)
watch(range, loadTimeline)
watch(onlyFailed, () => loadChecks())
watch(() => session.current?.id, () => { router.replace('/health'); loadList() })

// 实时：列表状态与当前连接的检查记录
let refreshTimer: ReturnType<typeof setTimeout> | null = null
const off = on('health', (d: any) => {
  const it = items.value.find((x) => x.connection_id === d?.connection_id)
  if (it) Object.assign(it, { status: d.status, fails: d.fails, last_check_at: d.at, last_latency_ms: d.ok ? d.latency_ms : it.last_latency_ms, last_error: d.ok ? '' : d.error })
  if (d?.connection_id === id.value) {
    checks.value = [{ id: d.at, at: d.at, ok: d.ok, latency_ms: d.latency_ms, error: d.error, status: d.status, manual: d.manual }, ...checks.value].slice(0, 200)
    if (refreshTimer) clearTimeout(refreshTimer)
    refreshTimer = setTimeout(loadTimeline, 1500)
  }
})
onBeforeUnmount(() => { off(); if (refreshTimer) clearTimeout(refreshTimer) })

async function checkNow() {
  checking.value = true
  try {
    await api.post(`/workspaces/${session.current?.id}/health/${id.value}/check`)
    await Promise.all([loadList(), loadChecks(), loadTimeline()])
  } catch (e) { toast.error(errorText(e)) } finally { checking.value = false }
}
async function saveConfig() {
  saving.value = true
  try {
    const c = cfg.value
    await api.put(`/workspaces/${session.current?.id}/health/${id.value}/config`, {
      enabled: c.enabled, interval_sec: Number(c.interval_sec), timeout_sec: Number(c.timeout_sec), fail_threshold: Number(c.fail_threshold), channels: c.channels,
    })
    toast.success(t('common.saved'))
    cfgCustom.value = true
    await loadList()
    loadHealth()
  } catch (e) { toast.error(errorText(e)) } finally { saving.value = false }
}

const hourly = computed(() => tl.value?.step === 'hour')
const latPoints = computed<LatPoint[]>(() => (tl.value?.buckets || []).map((b: any) => ({ t: b.start, avg: b.avg_latency_ms ?? null, p95: b.p95_latency_ms ?? null })))
function pct(v: number | null | undefined) { return v === null || v === undefined ? '—' : (Math.floor(v * 10000) / 100).toFixed(2) + '%' }
function ms(v: number | null | undefined) { return v === null || v === undefined ? '—' : v + ' ms' }
function bucketTitle(b: any) {
  const when = hourly.value ? formatDate(b.start, true) : formatDate(b.start)
  if (!b.checks) return `${when} · ${t('health.bucket.none')}`
  return `${when} · ${t('health.uptime')} ${pct(b.uptime)} · ${b.checks} / ${b.failures} · ${ms(b.avg_latency_ms)}`
}
const showList = computed(() => !ui.isMobile || !id.value)
const statusOf = (it: any) => (it.enabled ? it.status : 'disabled')
</script>

<template>
  <div class="hv">
    <nav v-if="showList" class="nav">
      <div class="lm-panel-head">
        <button class="back" :title="$t('common.back')" @click="router.push('/')"><Icon name="chevron-left" :size="16" /></button>
        <span>{{ $t('health.title') }}</span>
        <InfoTip :width="320" :text="$t('health.intro')" />
        <span class="lm-spacer" />
        <IconBtn icon="refresh" :size="15" :label="$t('common.refresh')" @click="loadList" />
      </div>
      <div class="items">
        <button v-for="it in items" :key="it.connection_id" class="it" :class="{ on: it.connection_id === id }" @click="router.push('/health/' + it.connection_id)">
          <span class="dot" :class="statusOf(it)" />
          <span class="main">
            <span class="name lm-ellipsis">{{ it.name }}</span>
            <span class="sub lm-ellipsis">{{ $t('health.status.' + statusOf(it)) }}<template v-if="it.enabled && it.last_check_at"><template v-if="it.status === 'up'"> · {{ it.last_latency_ms }} ms</template> · {{ formatRelative(it.last_check_at) }}</template></span>
          </span>
        </button>
        <div v-if="loaded && !items.length" class="lm-empty">{{ $t('health.empty') }}</div>
      </div>
    </nav>

    <section v-if="!showList || !ui.isMobile" class="body">
      <div v-if="ui.isMobile" class="m-back"><button class="back" @click="router.push('/health')"><Icon name="chevron-left" :size="16" />{{ $t('health.title') }}</button></div>
      <div v-if="current" class="inner">
        <div class="title">
          <span class="dot big" :class="statusOf(current)" />
          <h2 class="lm-ellipsis">{{ current.name }}</h2>
          <span class="lm-badge" :class="{ success: statusOf(current) === 'up', danger: statusOf(current) === 'down' }">{{ $t('health.status.' + statusOf(current)) }}</span>
          <span class="lm-spacer" />
          <Btn v-if="session.atLeast('member')" size="sm" icon="pulse" :loading="checking" @click="checkNow">{{ $t('health.checkNow') }}</Btn>
        </div>
        <div class="facts">
          <span v-if="current.last_check_at">{{ $t('health.lastCheck', { t: formatRelative(current.last_check_at) }) }}</span>
          <span v-else>{{ $t('health.never') }}</span>
          <span v-if="current.last_check_at && current.status === 'up'">{{ $t('health.latency') }} {{ current.last_latency_ms }} ms</span>
          <span v-if="current.last_error" class="err lm-ellipsis" :title="current.last_error">{{ current.last_error }}</span>
        </div>

        <div class="block">
          <div class="bh"><h3>{{ $t('health.timeline') }}</h3><span class="lm-spacer" /><Segmented v-model="range" :options="ranges" /></div>
          <template v-if="tl">
            <div class="sum">
              <div><span class="k">{{ $t('health.uptime') }}</span><b>{{ pct(tl.summary.uptime) }}</b></div>
              <div><span class="k">{{ $t('health.checks') }}</span><b>{{ tl.summary.checks }}</b></div>
              <div><span class="k">{{ $t('health.failures') }}</span><b :class="{ bad: tl.summary.failures }">{{ tl.summary.failures }}</b></div>
              <div><span class="k">{{ $t('health.avgLatency') }}</span><b>{{ ms(tl.summary.avg_latency_ms) }}</b></div>
              <div><span class="k">{{ $t('health.p95') }}</span><b>{{ ms(tl.summary.p95_latency_ms) }}</b></div>
            </div>
            <div class="blocks" :class="{ many: tl.buckets.length > 24 }">
              <span v-for="b in tl.buckets" :key="b.start" class="blk" :class="b.status" :title="bucketTitle(b)" />
            </div>
            <div class="axis"><span>{{ hourly ? '00:00' : formatDate(tl.buckets[0]?.start || 0).slice(5) }}</span><span>{{ hourly ? '23:00' : formatDate(tl.buckets[tl.buckets.length - 1]?.start || 0).slice(5) }}</span></div>
            <div class="legend">
              <span><i class="blk up" />{{ $t('health.bucket.up') }}</span><span><i class="blk partial" />{{ $t('health.bucket.partial') }}</span>
              <span><i class="blk down" />{{ $t('health.bucket.down') }}</span><span><i class="blk none" />{{ $t('health.bucket.none') }}</span>
            </div>
            <h3 class="mt">{{ $t('health.latencyChart') }}</h3>
            <div class="viz"><LatencyChart :points="latPoints" :hourly="hourly" :labels="{ avg: $t('health.avgLatency'), p95: $t('health.p95') }" /></div>
          </template>
        </div>

        <div v-if="cfg" class="block">
          <h3>{{ $t('health.config') }}</h3>
          <p v-if="!cfgCustom" class="hint">{{ $t('health.defaultHint') }}</p>
          <fieldset class="cfg" :disabled="!session.atLeast('admin')">
            <label class="sw"><Switch v-model="cfg.enabled" :disabled="!session.atLeast('admin')" />{{ $t('health.enabled') }}</label>
            <div class="lm-form">
              <div class="lm-field"><span class="lm-label">{{ $t('health.interval') }}<InfoTip :text="$t('health.intervalHelp')" /></span><input v-model.number="cfg.interval_sec" type="number" min="10" class="lm-select" /></div>
              <div class="lm-field"><span class="lm-label">{{ $t('health.timeout') }}<InfoTip :text="$t('health.timeoutHelp')" /></span><input v-model.number="cfg.timeout_sec" type="number" min="1" max="120" class="lm-select" /></div>
              <div class="lm-field"><span class="lm-label">{{ $t('health.threshold') }}</span><input v-model.number="cfg.fail_threshold" type="number" min="1" max="100" class="lm-select" /></div>
              <div class="lm-field"><span class="lm-label">{{ $t('health.channels') }}</span><ChannelPicker v-model="cfg.channels" :disabled="!session.atLeast('admin')" /></div>
            </div>
            <Btn v-if="session.atLeast('admin')" class="save" variant="primary" size="sm" :loading="saving" @click="saveConfig">{{ $t('common.save') }}</Btn>
          </fieldset>
        </div>

        <div class="block">
          <div class="bh"><h3>{{ $t('health.recent') }}</h3><span class="lm-spacer" /><label class="sw small"><Switch v-model="onlyFailed" />{{ $t('health.onlyFailed') }}</label></div>
          <div v-if="!checks.length" class="lm-card lm-empty">{{ $t('health.noData') }}</div>
          <div v-else class="lm-card">
            <div v-for="c in checks" :key="c.id + '-' + c.at" class="ck">
              <span class="dot" :class="c.ok ? 'up' : 'down'" />
              <span class="when">{{ formatDate(c.at, true) }}</span>
              <span class="lat">{{ c.ok ? c.latency_ms + ' ms' : $t('health.fail') }}</span>
              <span v-if="c.manual" class="lm-badge">{{ $t('health.manual') }}</span>
              <span v-if="c.error" class="cerr lm-ellipsis" :title="c.error">{{ c.error }}</span>
            </div>
            <div v-if="checksCursor" class="more"><Btn size="sm" variant="ghost" @click="loadChecks(false)">{{ $t('health.loadMore') }}</Btn></div>
          </div>
        </div>
      </div>
      <div v-else class="lm-empty pick"><Icon name="pulse" :size="28" /><div>{{ items.length ? $t('health.pick') : $t('health.empty') }}</div></div>
    </section>
  </div>
</template>

<style scoped>
.hv { flex: 1; display: flex; min-height: 0;
  --viz-1: #2a78d6; --viz-2: #eb6834; --viz-grid: rgba(55, 53, 47, .08); --viz-axis: rgba(55, 53, 47, .22); --viz-label: var(--text-3);
  --viz-tip-bg: var(--bg-elev); --viz-tip-text: var(--text); --viz-tip-muted: var(--text-2);
  --hb-up: #3f9b6b; --hb-down: #d9534f; --hb-partial: #e0a33a; --hb-none: rgba(55, 53, 47, .1); }
html[data-theme="dark"] .hv { --viz-1: #3987e5; --viz-2: #d95926; --viz-grid: rgba(255, 255, 255, .07); --viz-axis: rgba(255, 255, 255, .2);
  --hb-up: #4fb37f; --hb-down: #e66a64; --hb-partial: #e0a53a; --hb-none: rgba(255, 255, 255, .09); }
.nav { width: 270px; flex: none; border-right: 1px solid var(--border); background: var(--bg-soft); display: flex; flex-direction: column; min-height: 0; }
.back { border: 0; background: none; display: inline-flex; align-items: center; gap: 4px; padding: 4px; border-radius: 6px; color: var(--text-2); }
.back:hover { background: var(--bg-hover); }
.items { flex: 1; overflow: auto; padding: 4px 6px 8px; }
.it { width: 100%; display: flex; align-items: center; gap: 10px; padding: 6px 8px; border: 0; background: none; border-radius: var(--radius-m); text-align: left; }
.it:hover { background: var(--bg-hover); }
.it.on { background: var(--accent-soft); }
.main { display: flex; flex-direction: column; min-width: 0; flex: 1; }
.name { font-size: 13px; }
.sub { font-size: 11.5px; color: var(--text-3); }
.dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--border-strong); }
.dot.big { width: 10px; height: 10px; }
.dot.up { background: var(--success); }
.dot.down { background: var(--danger); }
.body { flex: 1; min-width: 0; overflow: auto; }
.inner { max-width: 980px; padding: 24px 32px 48px; }
.title { display: flex; align-items: center; gap: 8px; min-width: 0; }
.title h2 { margin: 0; font-size: 20px; font-weight: 650; min-width: 0; }
.facts { display: flex; flex-wrap: wrap; gap: 6px 16px; font-size: 12.5px; color: var(--text-2); margin: 8px 0 20px; min-width: 0; }
.facts .err { color: var(--danger); max-width: 100%; }
.block { margin-bottom: 28px; }
.block h3 { font-size: 14px; font-weight: 600; margin: 0 0 10px; }
.bh { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.bh h3 { margin: 0; }
.mt { margin-top: 18px !important; }
.sum { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 8px; margin-bottom: 12px; }
.sum > div { display: flex; flex-direction: column; gap: 2px; border: 1px solid var(--border); border-radius: var(--radius-m); padding: 8px 10px; min-width: 0; }
.sum .k { font-size: 11.5px; color: var(--text-3); }
.sum b { font-size: 15px; font-weight: 600; font-variant-numeric: tabular-nums; }
.sum b.bad { color: var(--danger); }
.blocks { display: flex; gap: 3px; height: 30px; }
.blocks.many { gap: 2px; }
.blk { flex: 1; min-width: 3px; border-radius: 2px; background: var(--hb-none); display: inline-block; }
.blk.up { background: var(--hb-up); }
.blk.down { background: var(--hb-down); }
.blk.partial { background: var(--hb-partial); }
.axis { display: flex; justify-content: space-between; font-size: 11px; color: var(--text-3); margin-top: 4px; }
.legend { display: flex; flex-wrap: wrap; gap: 4px 14px; font-size: 11.5px; color: var(--text-2); margin-top: 8px; }
.legend span { display: inline-flex; align-items: center; gap: 5px; }
.legend .blk { width: 10px; height: 10px; flex: none; }
.viz { border: 1px solid var(--border); border-radius: var(--radius-m); padding: 10px 10px 6px; }
.hint { font-size: 12.5px; color: var(--text-3); margin: -4px 0 10px; }
.cfg { border: 0; padding: 0; margin: 0; min-width: 0; }
.sw { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; margin-bottom: 12px; }
.sw.small { margin: 0; font-size: 12.5px; }
.save { margin-top: 14px; }
.ck { display: flex; align-items: center; gap: 10px; padding: 7px 12px; border-bottom: 1px solid var(--border); font-size: 12.5px; min-width: 0; }
.ck:last-child { border-bottom: 0; }
.when { color: var(--text-2); font-variant-numeric: tabular-nums; flex: none; }
.lat { width: 64px; flex: none; }
.cerr { color: var(--danger); flex: 1; min-width: 0; }
.more { padding: 8px; text-align: center; }
.pick { display: flex; flex-direction: column; align-items: center; gap: 10px; margin-top: 12vh; }
.m-back { padding: 8px 12px 0; }
@media (max-width: 760px) {
  .nav { width: auto; flex: 1; border-right: 0; }
  .inner { padding: 12px 16px 40px; }
  .title { flex-wrap: wrap; }
  .sum { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .sum b { font-size: 14px; }
  .bh { flex-wrap: wrap; }
  .ck { flex-wrap: wrap; gap: 4px 10px; }
  .cerr { flex-basis: 100%; }
}
</style>
