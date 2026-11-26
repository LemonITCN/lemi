<script setup lang="ts">
// 输入名称确认的危险操作对话框（清空表、删除表）。
import { computed, ref } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'

const props = defineProps<{ title: string; message: string; expected: string; prompt: string; okText: string; sql?: string; onDone: (v?: boolean) => void }>()
const open = ref(true)
const text = ref('')
const ok = computed(() => text.value.trim() === props.expected)

function close(v?: boolean) {
  open.value = false
  props.onDone(v)
}
</script>

<template>
  <Modal :open="open" :title="title" :width="440" @update:open="(v: boolean) => !v && close(false)">
    <p class="msg">{{ message }}</p>
    <code v-if="sql" class="sql">{{ sql }}</code>
    <form @submit.prevent="ok && close(true)">
      <TextField v-model="text" :label="prompt" mono autofocus autocomplete="off" />
    </form>
    <template #footer>
      <Btn @click="close(false)">{{ $t('common.cancel') }}</Btn>
      <Btn variant="danger" :disabled="!ok" @click="close(true)">{{ okText }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.msg { margin: 0 0 12px; color: var(--text-2); line-height: 1.6; }
.sql { display: block; margin-bottom: 14px; padding: 8px 10px; border-radius: var(--radius-m); background: var(--bg-sunken); font-family: var(--font-mono); font-size: 12.5px; word-break: break-all; }
</style>
