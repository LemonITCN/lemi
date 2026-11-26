<script setup lang="ts">
import Btn from '@/components/ui/Btn.vue'
import { useToast } from '@/stores/toast'
import { i18n } from '@/i18n'
const props = defineProps<{ codes: string[] }>()
const toast = useToast()
async function copy() {
  await navigator.clipboard.writeText(props.codes.join('\n'))
  toast.success(i18n.global.t('common.copied'))
}
function download() {
  const blob = new Blob(['Lemi 恢复码 / recovery codes\n\n' + props.codes.join('\n') + '\n'], { type: 'text/plain' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = 'lemi-recovery-codes.txt'
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 1000)
}
</script>

<template>
  <div class="codes">
    <code v-for="c in codes" :key="c">{{ c }}</code>
  </div>
  <div class="row">
    <Btn size="sm" icon="copy" @click="copy">{{ $t('common.copy') }}</Btn>
    <Btn size="sm" icon="download" @click="download">{{ $t('common.download') }}</Btn>
  </div>
</template>

<style scoped>
.codes { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; background: var(--bg-soft); border-radius: 8px; padding: 12px; }
.codes code { font-family: var(--font-mono); font-size: 13.5px; text-align: center; user-select: all; }
.row { display: flex; gap: 8px; margin-top: 10px; justify-content: center; }
</style>
