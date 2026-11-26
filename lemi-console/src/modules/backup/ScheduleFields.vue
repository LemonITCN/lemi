<script setup lang="ts">
// 计划表达式编辑（每小时 / 每天 / 每周 / 每月 / cron / 仅手动 + 时区），与定时任务同一套计划格式。
import { computed } from 'vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import { KINDS, cleanSpec, describe, timezones, type Spec } from '@/modules/agent-tasks/schedule'
import { i18n } from '@/i18n'

const spec = defineModel<Spec>({ required: true })
const props = defineProps<{ kinds?: Spec['kind'][]; manualLabel?: string }>()
const t = i18n.global.t
const tzs = timezones()
const kindOptions = computed(() => (props.kinds || KINDS).map((k) => ({ value: k, label: k === 'manual' && props.manualLabel ? props.manualLabel : t('tasks.kind.' + k) })))
const preview = computed(() => describe(cleanSpec(spec.value)))

function setKind(k: Spec['kind']) { spec.value = { ...spec.value, kind: k } }
function patch(p: Partial<Spec>) { spec.value = { ...spec.value, ...p } }
function toggleDay(d: number) {
  const s = new Set(spec.value.weekdays || [])
  if (s.has(d)) s.delete(d)
  else s.add(d)
  patch({ weekdays: [...s] })
}
</script>

<template>
  <div>
    <div class="kinds">
      <button v-for="k in kindOptions" :key="k.value" type="button" class="kind" :class="{ on: spec.kind === k.value }" @click="setKind(k.value)">{{ k.label }}</button>
    </div>
    <div v-if="spec.kind !== 'manual'" class="lm-form sched">
      <div v-if="spec.kind === 'hourly'" class="lm-field">
        <span class="lm-label">{{ $t('tasks.minute') }}</span>
        <input :value="spec.minute" type="number" min="0" max="59" class="lm-select" @input="patch({ minute: Number(($event.target as HTMLInputElement).value) })" />
      </div>
      <div v-if="spec.kind === 'monthly'" class="lm-field">
        <span class="lm-label">{{ $t('tasks.day') }}<InfoTip :text="$t('tasks.dayHint')" /></span>
        <select :value="spec.day" class="lm-select" @change="patch({ day: Number(($event.target as HTMLSelectElement).value) })">
          <option v-for="d in 31" :key="d" :value="d">{{ $t('tasks.dayN', { n: d }) }}</option>
        </select>
      </div>
      <div v-if="spec.kind === 'weekly'" class="lm-field full">
        <span class="lm-label">{{ $t('tasks.weekdays') }}</span>
        <div class="days">
          <button v-for="d in [1, 2, 3, 4, 5, 6, 0]" :key="d" type="button" class="day" :class="{ on: spec.weekdays?.includes(d) }" @click="toggleDay(d)">{{ $t('tasks.wd.' + d) }}</button>
        </div>
      </div>
      <div v-if="spec.kind === 'daily' || spec.kind === 'weekly' || spec.kind === 'monthly'" class="lm-field">
        <span class="lm-label">{{ $t('tasks.time') }}</span>
        <input :value="spec.time" type="time" class="lm-select" @input="patch({ time: ($event.target as HTMLInputElement).value })" />
      </div>
      <div v-if="spec.kind === 'cron'" class="lm-field">
        <span class="lm-label">{{ $t('tasks.cron') }}<InfoTip :width="280" :text="$t('tasks.cronHint')" /></span>
        <input :value="spec.cron" class="lm-select lm-mono" placeholder="0 3 * * *" @input="patch({ cron: ($event.target as HTMLInputElement).value })" />
      </div>
      <div class="lm-field">
        <span class="lm-label">{{ $t('tasks.timezone') }}</span>
        <select :value="spec.timezone || ''" class="lm-select" @change="patch({ timezone: ($event.target as HTMLSelectElement).value })">
          <option value="">{{ $t('tasks.serverTz') }}</option>
          <option v-for="z in tzs" :key="z" :value="z">{{ z }}</option>
        </select>
      </div>
    </div>
    <div v-if="spec.kind !== 'manual' || !manualLabel" class="preview">{{ preview }}</div>
  </div>
</template>

<style scoped>
.kinds { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 12px; }
.kind, .day { height: 28px; padding: 0 12px; border: 1px solid var(--border-strong); background: var(--bg); border-radius: var(--radius-m); font-size: 12.5px; color: var(--text-2); }
.kind:hover, .day:hover { background: var(--bg-hover); }
.kind.on, .day.on { border-color: var(--accent); background: var(--accent-soft); color: var(--accent); }
.days { display: flex; flex-wrap: wrap; gap: 6px; }
.day { min-width: 44px; padding: 0 8px; }
.sched { margin-bottom: 8px; }
.preview { font-size: 12.5px; color: var(--text-2); background: var(--bg-soft); border-radius: var(--radius-m); padding: 6px 10px; display: inline-block; }
</style>
