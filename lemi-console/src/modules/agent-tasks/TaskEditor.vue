<script setup lang="ts">
// 定时任务编辑：计划、提示词、模型与思考档位、连接档位（只能收紧）、通知。修改要空间管理员。
import { computed, onMounted, ref, watch } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Switch from '@/components/ui/Switch.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import GrantLevels, { type Choice } from '@/modules/agent/settings/GrantLevels.vue'
import type { ModelInfo } from '@/modules/agent/types'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'
import { KINDS, cleanSpec, defaultSpec, describe, timezones, type Spec } from './schedule'

const props = defineProps<{ task: any | null; canEdit: boolean }>()
const emit = defineEmits<{ saved: [task: any] }>()
const session = useSession()
const toast = useToast()
const t = i18n.global.t

const name = ref('')
const prompt = ref('')
const spec = ref<Spec>(defaultSpec())
const model = ref('')
const effort = ref('')
const levels = ref<Record<string, string>>({})
const channels = ref('')
const notifyOn = ref('always')
const enabled = ref(true)
const choices = ref<Choice[]>([])
const models = ref<ModelInfo[]>([])
const defaultModel = ref('')
const busy = ref(false)
const tzs = timezones()

function fill() {
  const tk = props.task
  if (!tk) {
    name.value = ''
    prompt.value = ''
    spec.value = defaultSpec()
    model.value = ''
    effort.value = ''
    channels.value = ''
    notifyOn.value = 'always'
    enabled.value = true
    levels.value = Object.fromEntries(choices.value.map((c) => [c.id, c.max_level === 'hidden' ? 'hidden' : 'read']))
    return
  }
  name.value = tk.name
  prompt.value = tk.prompt
  spec.value = { ...defaultSpec(), ...tk.schedule, timezone: tk.schedule?.timezone || '' }
  model.value = tk.model || ''
  effort.value = tk.effort || ''
  channels.value = (tk.channels || []).join(', ')
  notifyOn.value = tk.notify_on || 'always'
  enabled.value = !!tk.enabled
  levels.value = Object.fromEntries(choices.value.map((c) => [c.id, tk.connections?.[c.id] || 'hidden']))
}

onMounted(async () => {
  const ws = session.current?.id
  if (!ws) return
  try {
    const [c, m] = await Promise.all([api.get(`/me/mcp/choices?workspace=${ws}`), api.get(`/workspaces/${ws}/ai/models`)])
    choices.value = c.connections || []
    models.value = m.models || []
    defaultModel.value = m.default || ''
  } catch (e) { toast.error(errorText(e)) }
  fill()
})
watch(() => props.task?.id, fill)

const efforts = computed(() => models.value.find((m) => m.ref === (model.value || defaultModel.value))?.efforts || [])
watch(model, () => { if (effort.value && !efforts.value.includes(effort.value)) effort.value = '' })
const modelGroups = computed(() => {
  const g: Record<string, ModelInfo[]> = {}
  for (const m of models.value) (g[m.provider_name] ||= []).push(m)
  return Object.entries(g)
})
const kindOptions = KINDS.map((k) => ({ value: k, label: t('tasks.kind.' + k) }))
const preview = computed(() => describe(cleanSpec(spec.value)))
const granted = computed(() => Object.values(levels.value).filter((l) => l && l !== 'hidden').length)

function toggleDay(d: number) {
  const s = new Set(spec.value.weekdays || [])
  if (s.has(d)) s.delete(d)
  else s.add(d)
  spec.value = { ...spec.value, weekdays: [...s] }
}

async function save() {
  const ws = session.current?.id
  if (!ws) return
  busy.value = true
  try {
    const body = {
      name: name.value, prompt: prompt.value, schedule: cleanSpec(spec.value), model: model.value, effort: effort.value,
      connections: Object.fromEntries(Object.entries(levels.value).filter(([, l]) => l && l !== 'hidden')),
      channels: channels.value.split(/[\s,，]+/).map((s) => s.trim()).filter(Boolean),
      notify_on: notifyOn.value, enabled: enabled.value,
    }
    const r = props.task ? await api.put(`/agent-tasks/${props.task.id}`, body) : await api.post(`/workspaces/${ws}/agent-tasks`, body)
    toast.success(t('common.saved'))
    emit('saved', r.task)
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
</script>

<template>
  <fieldset class="ed" :disabled="!canEdit">
    <div class="lm-section">
      <div class="lm-form">
        <TextField v-model="name" class="full" :label="$t('tasks.name')" :placeholder="$t('tasks.namePh')" />
        <div class="lm-field full">
          <span class="lm-label">{{ $t('tasks.prompt') }}<InfoTip :width="300" :text="$t('tasks.promptHint')" /></span>
          <textarea v-model="prompt" class="lm-textarea prompt" :placeholder="$t('tasks.promptPh')" />
        </div>
      </div>
    </div>

    <div class="lm-section">
      <h3>{{ $t('tasks.schedule') }}</h3>
      <div class="kinds">
        <button v-for="k in kindOptions" :key="k.value" type="button" class="kind" :class="{ on: spec.kind === k.value }" @click="spec = { ...spec, kind: k.value }">{{ k.label }}</button>
      </div>
      <div class="lm-form sched">
        <div v-if="spec.kind === 'hourly'" class="lm-field">
          <span class="lm-label">{{ $t('tasks.minute') }}</span>
          <input v-model.number="spec.minute" type="number" min="0" max="59" class="lm-select" />
        </div>
        <div v-if="spec.kind === 'monthly'" class="lm-field">
          <span class="lm-label">{{ $t('tasks.day') }}<InfoTip :text="$t('tasks.dayHint')" /></span>
          <select v-model.number="spec.day" class="lm-select"><option v-for="d in 31" :key="d" :value="d">{{ $t('tasks.dayN', { n: d }) }}</option></select>
        </div>
        <div v-if="spec.kind === 'daily' || spec.kind === 'weekly' || spec.kind === 'monthly'" class="lm-field">
          <span class="lm-label">{{ $t('tasks.time') }}</span>
          <input v-model="spec.time" type="time" class="lm-select" />
        </div>
        <div v-if="spec.kind === 'weekly'" class="lm-field full">
          <span class="lm-label">{{ $t('tasks.weekdays') }}</span>
          <div class="days">
            <button v-for="d in [1, 2, 3, 4, 5, 6, 0]" :key="d" type="button" class="day" :class="{ on: spec.weekdays?.includes(d) }" @click="toggleDay(d)">{{ $t('tasks.wd.' + d) }}</button>
          </div>
        </div>
        <div v-if="spec.kind === 'cron'" class="lm-field">
          <span class="lm-label">{{ $t('tasks.cron') }}<InfoTip :width="280" :text="$t('tasks.cronHint')" /></span>
          <input v-model="spec.cron" class="lm-select lm-mono" placeholder="0 9 * * 1-5" />
        </div>
        <div v-if="spec.kind !== 'manual'" class="lm-field">
          <span class="lm-label">{{ $t('tasks.timezone') }}</span>
          <select v-model="spec.timezone" class="lm-select">
            <option value="">{{ $t('tasks.serverTz') }}</option>
            <option v-for="z in tzs" :key="z" :value="z">{{ z }}</option>
          </select>
        </div>
      </div>
      <div class="preview">{{ preview }}</div>
    </div>

    <div class="lm-section">
      <h3>{{ $t('tasks.model') }}</h3>
      <div class="lm-form">
        <div class="lm-field">
          <span class="lm-label">{{ $t('agent.model') }}</span>
          <select v-model="model" class="lm-select">
            <option value="">{{ $t('tasks.wsDefault') }}</option>
            <optgroup v-for="[p, list] in modelGroups" :key="p" :label="p">
              <option v-for="m in list" :key="m.ref" :value="m.ref">{{ m.name }}</option>
            </optgroup>
          </select>
        </div>
        <div class="lm-field">
          <span class="lm-label">{{ $t('agent.effort.label') }}</span>
          <select v-model="effort" class="lm-select" :disabled="!efforts.length">
            <option value="">{{ $t('agent.effort.default') }}</option>
            <option v-for="e in efforts" :key="e" :value="e">{{ $t('agent.effort.' + e) }}</option>
          </select>
        </div>
      </div>
    </div>

    <div class="lm-section">
      <h3>{{ $t('tasks.connections') }}<InfoTip :width="320" :text="$t('tasks.connectionsHint')" /></h3>
      <GrantLevels v-model="levels" :choices="choices" />
      <p v-if="!granted" class="warn">{{ $t('tasks.needConn') }}</p>
    </div>

    <div class="lm-section">
      <h3>{{ $t('tasks.notify') }}</h3>
      <div class="lm-form">
        <div class="lm-field">
          <span class="lm-label">{{ $t('tasks.notifyOn') }}</span>
          <select v-model="notifyOn" class="lm-select">
            <option value="always">{{ $t('tasks.notifyAlways') }}</option>
            <option value="failure">{{ $t('tasks.notifyFailure') }}</option>
            <option value="never">{{ $t('tasks.notifyNever') }}</option>
          </select>
        </div>
        <div class="lm-field">
          <span class="lm-label">{{ $t('tasks.channels') }}<InfoTip :width="280" :text="$t('tasks.channelsHint')" /></span>
          <input v-model="channels" class="lm-select lm-mono" :placeholder="$t('common.optional')" />
        </div>
      </div>
    </div>

    <div class="foot">
      <label class="en"><Switch v-model="enabled" :disabled="!canEdit" />{{ $t('tasks.enabled') }}</label>
      <span class="lm-spacer" />
      <Btn v-if="canEdit" variant="primary" :loading="busy" :disabled="!name.trim() || !prompt.trim() || !granted" @click="save">{{ task ? $t('common.save') : $t('tasks.create') }}</Btn>
    </div>
  </fieldset>
</template>

<style scoped>
.ed { border: 0; margin: 0; padding: 0; min-width: 0; }
.lm-section > h3 { display: flex; align-items: center; gap: 4px; margin-bottom: 10px; }
.prompt { min-height: 160px; font-size: 13.5px; line-height: 1.6; }
.kinds { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 12px; }
.kind, .day { height: 28px; padding: 0 12px; border: 1px solid var(--border-strong); background: var(--bg); border-radius: var(--radius-m); font-size: 12.5px; color: var(--text-2); }
.kind:hover, .day:hover { background: var(--bg-hover); }
.kind.on, .day.on { border-color: var(--accent); background: var(--accent-soft); color: var(--accent); }
.days { display: flex; flex-wrap: wrap; gap: 6px; }
.day { min-width: 44px; padding: 0 8px; }
.sched { margin-bottom: 8px; }
.preview { font-size: 12.5px; color: var(--text-2); background: var(--bg-soft); border-radius: var(--radius-m); padding: 6px 10px; display: inline-block; }
.warn { color: var(--warning); font-size: 12.5px; margin: 6px 0 0; }
.foot { display: flex; align-items: center; gap: 10px; border-top: 1px solid var(--border); padding-top: 16px; }
.en { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; }
fieldset:disabled .kind, fieldset:disabled .day { cursor: default; }
</style>
