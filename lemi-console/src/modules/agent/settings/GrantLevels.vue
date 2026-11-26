<script setup lang="ts">
// 授权里每个连接的档位选择（MCP 授权、访问令牌、定时任务共用）：只能选不高于上限的档位。
import { i18n } from '@/i18n'

export interface Choice { id: string; name: string; driver: string; env: string; ai_level: string; max_level: string }
const ORDER = ['hidden', 'read', 'write_auto', 'full']

defineProps<{ choices: Choice[] }>()
const model = defineModel<Record<string, string>>({ required: true })
const t = i18n.global.t

function options(max: string) {
  const i = ORDER.indexOf(max)
  return ORDER.slice(0, i + 1).map((v) => ({ value: v, label: t('agent.level.' + v) }))
}
</script>

<template>
  <div class="gl">
    <div v-for="c in choices" :key="c.id" class="row">
      <span class="cn lm-ellipsis">{{ c.name }}</span>
      <span v-if="c.env === 'prod'" class="lm-badge danger">{{ $t('agent.prodTag') }}</span>
      <span class="lm-spacer" />
      <select :value="model[c.id] || 'hidden'" class="lm-select sel" @change="model = { ...model, [c.id]: ($event.target as HTMLSelectElement).value }">
        <option v-for="o in options(c.max_level)" :key="o.value" :value="o.value">{{ o.label }}</option>
      </select>
    </div>
    <div v-if="!choices.length" class="lm-empty">{{ $t('agent.noVisibleConns') }}</div>
  </div>
</template>

<style scoped>
.gl { display: flex; flex-direction: column; border: 1px solid var(--border); border-radius: var(--radius-m); max-height: 260px; overflow: auto; }
.row { display: flex; align-items: center; gap: 8px; padding: 6px 8px; border-bottom: 1px solid var(--border); font-size: 13px; }
.row:last-child { border-bottom: 0; }
.cn { min-width: 0; }
.sel { width: 160px; height: 28px; flex: none; }
</style>
