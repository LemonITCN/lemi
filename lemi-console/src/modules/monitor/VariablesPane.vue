<script setup lang="ts">
// 全局变量（MySQL SHOW GLOBAL VARIABLES）/ 编译选项（SQLite PRAGMA compile_options）。
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import KvTable from './KvTable.vue'
import { formatClock } from './format'
import { useMonitor } from './useMonitor'

const props = defineProps<{ connId: string; placeholder: string }>()
const m = useMonitor<Record<string, string>>(() => props.connId, 'variables')
m.load()
</script>

<template>
  <div class="vars">
    <div v-if="m.error.value" class="err-box" role="alert">
      <p>{{ m.error.value }}</p>
      <Btn size="sm" icon="refresh" @click="m.load()">{{ $t('common.retry') }}</Btn>
    </div>
    <div v-else-if="!m.data.value" class="lm-empty">{{ $t('common.loading') }}</div>
    <div v-else class="wrap" :class="{ dim: m.loading.value }">
      <KvTable :data="m.data.value" :placeholder="placeholder">
        <template #tools>
          <span v-if="m.at.value" class="lm-faint upd">{{ formatClock(m.at.value) }}</span>
          <IconBtn icon="refresh" :size="16" :label="$t('common.refresh')" :disabled="m.loading.value" @click="m.load()" />
        </template>
      </KvTable>
    </div>
  </div>
</template>

<style scoped>
.vars { min-width: 0; }
.wrap { transition: opacity .15s; }
.dim { opacity: .55; }
.upd { font-size: 12px; font-variant-numeric: tabular-nums; }
.err-box { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 10px 12px; border-radius: var(--radius-m); background: var(--danger-bg); }
.err-box p { margin: 0; color: var(--danger); font-size: 13px; word-break: break-word; }
</style>
