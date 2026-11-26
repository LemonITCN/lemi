<script setup lang="ts">
// 值面板：查看与编辑单个单元格的完整值（长文本、JSON 格式化、二进制十六进制与图片预览）。
import { computed, onMounted, ref } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import Segmented from '@/components/ui/Segmented.vue'
import type { Cell, ResultColumn } from '@/lib/meta'
import { b64ToBytes, bytesToHex, hexToB64, isBin, isDefault, isTrunc, plainText } from './model'
import { copyText } from '@/lib/cm'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'

const props = defineProps<{ column: ResultColumn; value: Cell | undefined; editable: boolean; loadFull?: () => Promise<Cell> }>()
const emit = defineEmits<{ close: []; save: [Cell] }>()
const toast = useToast()
const t = i18n.global.t

const cur = ref<Cell | undefined>(props.value)
const loading = ref(false)
const binary = computed(() => isBin(cur.value) || props.column.kind === 'binary')
const mode = ref(binary.value ? 'hex' : props.column.kind === 'json' ? 'json' : 'text')
const text = ref('')
const isNull = ref(cur.value === null || cur.value === undefined)
const truncated = computed(() => isTrunc(cur.value) || (isBin(cur.value) && b64ToBytes(cur.value.$bin).length < cur.value.len))

function fill() {
  const v = cur.value
  isNull.value = v === null || v === undefined
  if (isBin(v)) text.value = bytesToHex(b64ToBytes(v.$bin)).toUpperCase().replace(/(..)/g, '$1 ').trim()
  else if (isDefault(v)) text.value = ''
  else {
    text.value = plainText(v)
    if (mode.value === 'json') pretty()
  }
}

function pretty() {
  try { text.value = JSON.stringify(JSON.parse(text.value), null, 2) } catch {}
}

async function loadAll() {
  if (!props.loadFull) return
  loading.value = true
  try {
    cur.value = await props.loadFull()
    fill()
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  fill()
  if (truncated.value) loadAll()
})

const image = computed(() => {
  const v = cur.value
  if (!isBin(v)) return ''
  const b = b64ToBytes(v.$bin)
  const sig = bytesToHex(b, 12)
  const mime = sig.startsWith('89504e47') ? 'image/png' : sig.startsWith('ffd8ff') ? 'image/jpeg' : sig.startsWith('47494638') ? 'image/gif' : sig.startsWith('52494646') && sig.slice(16, 24) === '57454250' ? 'image/webp' : ''
  return mime ? `data:${mime};base64,${v.$bin}` : ''
})

const jsonError = computed(() => {
  if (mode.value !== 'json' || isNull.value || !text.value.trim()) return ''
  try { JSON.parse(text.value); return '' } catch (e: any) { return e.message }
})

function save() {
  if (isNull.value) return emit('save', null)
  if (binary.value) {
    const hex = text.value.replace(/\s+/g, '')
    if (!/^([0-9a-fA-F]{2})*$/.test(hex)) return toast.error(t('grid.badHex'))
    return emit('save', { $bin: hexToB64(hex), len: hex.length / 2 })
  }
  if (mode.value === 'json' && !jsonError.value && text.value.trim()) {
    try { return emit('save', JSON.stringify(JSON.parse(text.value))) } catch {}
  }
  emit('save', text.value)
}

async function copy() { if (await copyText(text.value)) toast.success(t('common.copied')) }
const modes = computed(() => binary.value ? [{ value: 'hex', label: 'HEX' }] : [{ value: 'text', label: t('grid.text') }, { value: 'json', label: 'JSON' }])
</script>

<template>
  <Modal :open="true" :title="`${column.name} · ${column.type.toLowerCase()}`" :width="640" @update:open="(v: boolean) => !v && emit('close')">
    <div class="ve">
      <div class="bar">
        <Segmented v-model="mode" :options="modes" @update:model-value="mode === 'json' && pretty()" />
        <span class="lm-spacer" />
        <span v-if="truncated && !loading" class="lm-badge warning">{{ $t('grid.truncated') }}</span>
        <span v-if="loading" class="lm-faint small">{{ $t('common.loading') }}</span>
        <label v-if="editable && column.nullable !== false" class="nullck"><input v-model="isNull" type="checkbox" /> NULL</label>
      </div>
      <img v-if="image" :src="image" class="img" alt="" />
      <textarea v-model="text" class="lm-textarea ta" :class="{ mono: true, dim: isNull }" :readonly="!editable || isNull" spellcheck="false" rows="14" />
      <p v-if="jsonError" class="err">{{ jsonError }}</p>
    </div>
    <template #footer>
      <Btn icon="copy" variant="ghost" @click="copy">{{ $t('common.copy') }}</Btn>
      <span class="lm-spacer" />
      <Btn @click="emit('close')">{{ editable ? $t('common.cancel') : $t('common.close') }}</Btn>
      <Btn v-if="editable" variant="primary" :disabled="loading || (truncated && !!loadFull)" @click="save">{{ $t('common.apply') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.ve { display: flex; flex-direction: column; gap: 10px; }
.bar { display: flex; align-items: center; gap: 10px; }
.small { font-size: 12px; }
.nullck { display: flex; align-items: center; gap: 4px; font-size: 12.5px; font-family: var(--font-mono); }
.ta { font-family: var(--font-mono); font-size: 12.5px; min-height: 260px; }
.ta.dim { opacity: .5; }
.img { max-width: 100%; max-height: 240px; object-fit: contain; border-radius: 6px; background: var(--bg-soft); align-self: flex-start; }
.err { margin: 0; color: var(--danger); font-size: 12px; }
</style>
