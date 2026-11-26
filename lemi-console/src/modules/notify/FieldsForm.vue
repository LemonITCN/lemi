<script setup lang="ts">
// 按服务端给的字段描述渲染配置表单（通知渠道、备份存储目标共用）。
// 密钥字段只写不读：已设置时占位提示「留空则保持不变」，可选密钥可以显式清除（加入 clear 列表）。
import InfoTip from '@/components/ui/InfoTip.vue'
import Switch from '@/components/ui/Switch.vue'
import { isZh } from '@/i18n'
import type { DynField } from './store'

const props = defineProps<{ fields: DynField[]; secretsSet?: string[]; keptText: string; clearText: string }>()
const config = defineModel<Record<string, any>>('config', { required: true })
const clear = defineModel<string[]>('clear', { default: () => [] })

function label(f: DynField) { return isZh() ? f.label : f.label_en || f.label }
function visible(f: DynField) {
  if (!f.show_if) return true
  return Object.entries(f.show_if).every(([k, vals]) => {
    let v = config.value[k]
    if (v === undefined) v = props.fields.find((x) => x.key === k)?.default
    return vals.includes(String(v ?? ''))
  })
}
function isSet(f: DynField) { return f.secret && (props.secretsSet || []).includes(f.key) && !clear.value.includes(f.key) }
function opts(f: DynField) {
  return (f.options || []).map((o) => (typeof o === 'string' ? { value: o, label: o } : o))
}
function full(f: DynField) { return f.type === 'textarea' || f.type === 'list' || f.type === 'bool' || ['url', 'host', 'endpoint', 'path', 'private_key'].includes(f.key) }
function listText(f: DynField) {
  const v = config.value[f.key]
  return Array.isArray(v) ? v.join('\n') : (v || '')
}
function setList(f: DynField, text: string) {
  config.value = { ...config.value, [f.key]: text.split(/[\n,，;；]+/).map((s) => s.trim()).filter(Boolean) }
}
function set(f: DynField, v: any) {
  config.value = { ...config.value, [f.key]: v }
  if (f.secret && v) clear.value = clear.value.filter((k) => k !== f.key)
}
function clearSecret(f: DynField) {
  const c = { ...config.value }
  delete c[f.key]
  config.value = c
  if (!clear.value.includes(f.key)) clear.value = [...clear.value, f.key]
}
function boolValue(f: DynField) {
  const v = config.value[f.key]
  return v === undefined ? !!f.default : v === true || v === 'true'
}
</script>

<template>
  <div class="lm-form">
    <template v-for="f in fields" :key="f.key">
      <div v-if="visible(f)" class="lm-field" :class="{ full: full(f) }">
        <label v-if="f.type === 'bool'" class="bool">
          <Switch :model-value="boolValue(f)" @update:model-value="(v: boolean) => set(f, v)" />
          <span>{{ label(f) }}</span>
          <InfoTip v-if="f.help" :width="300" :text="f.help" />
        </label>
        <template v-else>
          <span class="lm-label">
            {{ label(f) }}<span v-if="f.required" class="req">*</span>
            <InfoTip v-if="f.help" :width="300" :text="f.help" />
            <button v-if="isSet(f) && !f.required" type="button" class="clear" @click="clearSecret(f)">{{ clearText }}</button>
          </span>
          <select v-if="f.type === 'select'" class="lm-select" :value="config[f.key] ?? f.default ?? ''" @change="set(f, ($event.target as HTMLSelectElement).value)">
            <option v-for="o in opts(f)" :key="o.value" :value="o.value">{{ o.label }}</option>
          </select>
          <textarea v-else-if="f.type === 'textarea'" class="lm-textarea" :class="{ 'lm-mono': f.secret || f.key === 'private_key' || f.key === 'body' }" rows="4"
            :value="config[f.key] ?? ''" :placeholder="isSet(f) ? keptText : f.placeholder || ''" spellcheck="false"
            @input="set(f, ($event.target as HTMLTextAreaElement).value)" />
          <textarea v-else-if="f.type === 'list'" class="lm-textarea" rows="3" :value="listText(f)" :placeholder="f.placeholder || ''"
            @input="setList(f, ($event.target as HTMLTextAreaElement).value)" />
          <input v-else class="lm-select" :type="f.type === 'password' ? 'password' : f.type === 'number' ? 'number' : 'text'"
            :value="config[f.key] ?? (f.type === 'number' && f.default !== undefined ? f.default : '')"
            :placeholder="isSet(f) ? keptText : f.placeholder || (f.default !== undefined && f.type !== 'number' ? String(f.default) : '')"
            :autocomplete="f.type === 'password' ? 'new-password' : 'off'" spellcheck="false"
            @input="set(f, f.type === 'number' ? (($event.target as HTMLInputElement).value === '' ? undefined : Number(($event.target as HTMLInputElement).value)) : ($event.target as HTMLInputElement).value)" />
        </template>
      </div>
    </template>
  </div>
</template>

<style scoped>
.bool { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.req { color: var(--danger); margin-left: 1px; }
.clear { margin-left: auto; border: 0; background: none; color: var(--accent); font-size: 12px; padding: 0 2px; }
.clear:hover { text-decoration: underline; }
</style>
