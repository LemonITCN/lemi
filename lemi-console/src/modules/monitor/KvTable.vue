<script setup lang="ts">
// 可搜索的键值表（变量、全部状态值、SQLite 编译选项）。
import { computed, ref } from 'vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ data: Record<string, string> | null; placeholder?: string }>()
const q = ref('')
const rows = computed(() => {
  const s = q.value.trim().toLowerCase()
  const all = Object.entries(props.data || {}).sort((a, b) => a[0].localeCompare(b[0]))
  return s ? all.filter(([k, v]) => k.toLowerCase().includes(s) || String(v).toLowerCase().includes(s)) : all
})
const total = computed(() => Object.keys(props.data || {}).length)
</script>

<template>
  <div class="kv">
    <div class="kv-bar">
      <div class="search"><Icon name="search" :size="14" /><input v-model="q" :placeholder="placeholder || $t('common.search')" spellcheck="false" /></div>
      <span class="lm-faint cnt">{{ q.trim() ? `${rows.length} / ${total}` : total }}</span>
      <template v-if="$slots.tools"><span class="lm-spacer" /><slot name="tools" /></template>
    </div>
    <div v-if="!rows.length" class="lm-empty">{{ total ? $t('monitor.noMatch') : $t('common.empty') }}</div>
    <div v-else class="kv-list" role="table">
      <div v-for="[k, v] in rows" :key="k" class="kv-row" role="row">
        <span class="k lm-mono" role="cell">{{ k }}</span>
        <span class="v lm-mono" role="cell">{{ v === '' ? '—' : v }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.kv { min-width: 0; }
.kv-bar { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.search { display: flex; align-items: center; gap: 6px; height: 30px; padding: 0 8px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); color: var(--text-3); flex: 0 1 320px; min-width: 0; }
.search:focus-within { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.search input { flex: 1; min-width: 0; border: 0; outline: none; background: transparent; font-size: 13px; color: var(--text); }
.cnt { font-size: 12px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.kv-list { border: 1px solid var(--border); border-radius: var(--radius-l); overflow: hidden; }
.kv-row { display: grid; grid-template-columns: minmax(180px, 34%) minmax(0, 1fr); gap: 12px; padding: 5px 12px; border-bottom: 1px solid var(--border); font-size: 12.5px; }
.kv-row:last-child { border-bottom: 0; }
.kv-row:hover { background: var(--bg-soft); }
.k { color: var(--text-2); word-break: break-all; }
.v { color: var(--text); white-space: pre-wrap; word-break: break-all; }
@media (max-width: 760px) { .kv-row { grid-template-columns: 1fr; gap: 2px; padding: 6px 10px; } }
</style>
