<script setup lang="ts">
// 输入框：模型与思考档位、计划模式、上下文占用；运行中可以继续发（插话），也可以排队到本轮结束后；停止。
import { computed, nextTick, ref, watch } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import Switch from '@/components/ui/Switch.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import { PopoverRoot, PopoverTrigger, PopoverPortal, PopoverContent } from 'reka-ui'
import { useAgent } from './store'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { errorText, i18n } from '@/i18n'

const agent = useAgent()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t
const text = ref('')
const ta = ref<HTMLTextAreaElement>()
const sending = ref(false)

// 新会话还没创建时，设置先放在本地
const local = ref({ model: '', effort: '', mode: 'normal' as 'normal' | 'plan' })
const settings = computed(() => agent.conv?.settings || local.value)
const model = computed(() => agent.models.find((m) => m.ref === (settings.value.model || agent.defaultModel)) || agent.models.find((m) => m.ref === agent.defaultModel))
const efforts = computed(() => (model.value?.efforts || []).filter((e) => e !== 'default'))

async function setSetting(patch: Partial<{ model: string; effort: string; mode: 'normal' | 'plan' }>) {
  if (!agent.conv) {
    local.value = { ...local.value, ...patch }
    return
  }
  try {
    await agent.patch(agent.conv.id, { settings: { ...agent.conv.settings, ...patch } })
  } catch (e) { toast.error(errorText(e)) }
}

const modelItems = computed<MenuItem[]>(() => {
  const out: MenuItem[] = []
  let prov = ''
  for (const m of agent.models) {
    if (m.provider_name !== prov) {
      prov = m.provider_name
      out.push({ heading: prov })
    }
    out.push({ label: m.name, hint: m.context ? Math.round(m.context / 1000) + 'K' : '', checked: m.ref === model.value?.ref, run: () => setSetting({ model: m.ref, effort: '' }) })
  }
  if (!out.length) out.push({ label: t('agent.noModels'), disabled: true })
  return out
})

const effortItems = computed<MenuItem[]>(() => [
  { label: t('agent.effort.default'), checked: !settings.value.effort, run: () => setSetting({ effort: '' }) },
  ...efforts.value.map((e) => ({ label: t('agent.effort.' + e, e), checked: settings.value.effort === e, run: () => setSetting({ effort: e }) })),
])

const plan = computed({
  get: () => settings.value.mode === 'plan',
  set: (v: boolean) => setSetting({ mode: v ? 'plan' : 'normal' }),
})

// 上下文占用
const ctxPct = computed(() => {
  const u = agent.usage
  const win = model.value?.context || u?.window || 0
  if (!u?.context || !win) return 0
  const limit = u.limit || Math.max(win - Math.max(win * 0.15, 16000), win * 0.75)
  return Math.min(100, Math.round((u.context / limit) * 100))
})
const total = computed(() => agent.conv?.usage)
function k(n?: number) { return n ? (n >= 10000 ? (n / 1000).toFixed(0) + 'K' : n.toLocaleString()) : '0' }

function autosize() {
  const el = ta.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = Math.min(el.scrollHeight, 220) + 'px'
}
watch(text, () => nextTick(autosize))

async function submit(delivery: 'steer' | 'queue' = 'steer') {
  const v = text.value.trim()
  if (!v || sending.value) return
  sending.value = true
  try {
    await agent.send(v, { delivery, model: settings.value.model || undefined, effort: settings.value.effort || undefined, mode: settings.value.mode })
    text.value = ''
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    sending.value = false
    nextTick(() => ta.value?.focus())
  }
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    submit(e.altKey && agent.running ? 'queue' : 'steer')
  }
}

const sendItems = computed<MenuItem[]>(() => [
  { label: t('agent.steer'), hint: 'Enter', run: () => submit('steer') },
  { label: t('agent.queue'), hint: 'Alt+Enter', run: () => submit('queue') },
])

defineExpose({ focus: () => ta.value?.focus(), insert: (s: string) => { text.value = (text.value ? text.value + ' ' : '') + s; nextTick(() => ta.value?.focus()) } })
</script>

<template>
  <div class="cp">
    <div class="box" :class="{ plan: plan }">
      <textarea ref="ta" v-model="text" rows="1" :placeholder="agent.running ? $t('agent.placeholderRunning') : $t('agent.placeholder')" @keydown="onKey" />
      <div class="bar">
        <Menu :items="modelItems" :width="260" side="top">
          <button class="pill" :title="$t('agent.model')"><Icon name="sparkle" :size="13" /><span class="lm-ellipsis">{{ model?.name || $t('agent.noModels') }}</span><Icon name="chevron-down" :size="11" /></button>
        </Menu>
        <Menu v-if="efforts.length" :items="effortItems" :width="160" side="top">
          <button class="pill" :title="$t('agent.effort.label')"><span>{{ settings.effort ? $t('agent.effort.' + settings.effort, settings.effort) : $t('agent.effort.default') }}</span><Icon name="chevron-down" :size="11" /></button>
        </Menu>
        <label class="plan-sw"><Switch v-model="plan" /><span>{{ ui.isMobile ? $t('agent.plan') : $t('agent.planMode') }}</span><InfoTip :text="$t('agent.planModeHint')" /></label>
        <span class="lm-spacer" />
        <PopoverRoot v-if="agent.usage?.context">
          <PopoverTrigger class="ring-btn" :aria-label="$t('agent.usage.label')"><span class="ring" :style="{ '--p': ctxPct }" /></PopoverTrigger>
          <PopoverPortal>
            <PopoverContent class="info-pop" side="top" align="end" :side-offset="6" :collision-padding="12" :style="{ maxWidth: '260px' }">
              <div class="usage">
                <div>{{ $t('agent.usage.context', { pct: ctxPct, n: k(agent.usage?.context) }) }}</div>
                <div v-if="total">{{ $t('agent.usage.total', { i: k(total.input), o: k(total.output), c: k(total.cache_read), r: total.requests }) }}</div>
                <div class="muted">{{ $t('agent.usage.hint') }}</div>
              </div>
            </PopoverContent>
          </PopoverPortal>
        </PopoverRoot>
        <IconBtn v-if="agent.running" icon="stop" :label="$t('agent.stop')" class="stop" @click="agent.stop()" />
        <Menu v-if="agent.running && text.trim()" :items="sendItems" :width="200" side="top" align="end">
          <button class="send" :disabled="sending"><Icon name="send" :size="15" /></button>
        </Menu>
        <button v-else-if="!agent.running" class="send" :disabled="!text.trim() || sending" :title="$t('agent.send')" @click="submit()"><Icon name="send" :size="15" /></button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.cp { padding: 8px 10px 10px; flex: none; }
.box { border: 1px solid var(--border-strong); border-radius: 12px; background: var(--bg); transition: border-color .15s; }
.box:focus-within { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
.box.plan { border-style: dashed; }
textarea { display: block; width: 100%; border: 0; outline: none; resize: none; background: none; padding: 10px 12px 4px; font: inherit; font-size: 13.5px; line-height: 1.5; max-height: 220px; color: var(--text); }
.bar { display: flex; align-items: center; gap: 4px; padding: 4px 6px 6px 8px; min-width: 0; }
.pill { display: inline-flex; align-items: center; gap: 4px; height: 24px; padding: 0 7px; border-radius: 12px; border: 0; background: none; font-size: 12px; color: var(--text-2); max-width: 150px; min-width: 0; white-space: nowrap; flex: none; }
.pill:first-child { flex: 0 1 auto; }
.pill:hover { background: var(--bg-hover); color: var(--text); }
.plan-sw { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: var(--text-2); margin-left: 4px; white-space: nowrap; }
.send { width: 28px; height: 28px; border-radius: 50%; border: 0; background: var(--accent); color: #fff; display: flex; align-items: center; justify-content: center; flex: none; }
.send:disabled { background: var(--bg-active); color: var(--text-3); }
.stop { color: var(--danger); }
.ring-btn { border: 0; background: none; padding: 4px; display: inline-flex; border-radius: 50%; }
.ring-btn:hover { background: var(--bg-hover); }
.ring { width: 16px; height: 16px; border-radius: 50%; flex: none; background: conic-gradient(var(--accent) calc(var(--p) * 1%), var(--border-strong) 0); -webkit-mask: radial-gradient(circle 5px, transparent 98%, #000 100%); mask: radial-gradient(circle 5px, transparent 98%, #000 100%); }
.usage { display: flex; flex-direction: column; gap: 3px; }
.usage .muted { color: var(--text-3); font-size: 11.5px; }
</style>
