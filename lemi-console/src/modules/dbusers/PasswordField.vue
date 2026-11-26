<script setup lang="ts">
// 口令输入：默认隐藏，可切换显示；「生成」用 crypto.getRandomValues 生成随机口令，生成后可复制。
import { ref } from 'vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import { useToast } from '@/stores/toast'
import { copyText } from '@/lib/cm'
import { i18n } from '@/i18n'

const model = defineModel<string>({ default: '' })
const props = withDefaults(defineProps<{ label: string; min?: number; length?: number; error?: string }>(), { min: 8, length: 20 })
const shown = ref(false)
const toast = useToast()
const t = i18n.global.t

const UPPER = 'ABCDEFGHJKLMNPQRSTUVWXYZ'
const LOWER = 'abcdefghijkmnopqrstuvwxyz'
const DIGIT = '23456789'
const SYMBOL = '-_.+=@#%'
const ALL = UPPER + LOWER + DIGIT + SYMBOL

/** 均匀随机取字符（拒绝采样，避免取模偏差）。 */
function pick(chars: string, n: number): string {
  const out: string[] = []
  const limit = Math.floor(0x100000000 / chars.length) * chars.length
  const buf = new Uint32Array(n * 2 + 8)
  while (out.length < n) {
    crypto.getRandomValues(buf)
    for (const x of buf) {
      if (x < limit) out.push(chars[x % chars.length])
      if (out.length === n) break
    }
  }
  return out.join('')
}

/** 生成口令：大小写字母、数字、符号各至少一个（满足 validate_password 的常见策略）。 */
function generate() {
  for (let i = 0; i < 50; i++) {
    const p = pick(ALL, props.length)
    if ([UPPER, LOWER, DIGIT, SYMBOL].every((set) => [...p].some((c) => set.includes(c)))) {
      model.value = p
      shown.value = true
      return
    }
  }
}

async function copy() {
  if (model.value && (await copyText(model.value))) toast.success(t('common.copied'))
}
</script>

<template>
  <div class="pw">
    <span class="lm-label">{{ label }}</span>
    <span class="row" :class="{ invalid: !!error }">
      <input v-model="model" class="inp lm-mono" :type="shown ? 'text' : 'password'" autocomplete="new-password" spellcheck="false" :minlength="min" :aria-label="label" />
      <IconBtn :icon="shown ? 'eye-off' : 'eye'" :size="15" :label="shown ? $t('dbusers.hide') : $t('dbusers.show')" @click="shown = !shown" />
      <IconBtn icon="key" :size="15" :label="$t('dbusers.generate')" @click="generate" />
      <IconBtn icon="copy" :size="15" :label="$t('common.copy')" :disabled="!model" @click="copy" />
    </span>
    <span v-if="error" class="err">{{ error }}</span>
  </div>
</template>

<style scoped>
.pw { display: flex; flex-direction: column; min-width: 0; }
.row { display: flex; align-items: center; gap: 2px; height: 34px; padding: 0 3px 0 0; border: 1px solid var(--border-strong); border-radius: var(--radius-m); background: var(--bg); }
.row:focus-within { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
.row.invalid { border-color: var(--danger); }
.inp { flex: 1; min-width: 0; height: 100%; border: 0; outline: none; background: transparent; padding: 0 10px; font-size: 13px; }
.err { font-size: 12px; color: var(--danger); margin-top: 4px; }
</style>
