<script setup lang="ts">
// 消息流：用户消息、回复正文（Markdown + 图表/表格/图引用块）、思考过程（折叠）、工具调用卡片、错误。
import { ref } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import RichText from '@/components/rich/RichText.vue'
import ToolCard from './ToolCard.vue'
import type { Item } from './types'

defineProps<{ items: Item[] }>()
const openThink = ref<Record<string, boolean>>({})
const HIDDEN_TOOLS = ['todo_write']
</script>

<template>
  <div class="ml">
    <template v-for="it in items" :key="it.key">
      <div v-if="it.kind === 'user'" class="user"><div class="bubble" :class="{ steer: it.steer }">{{ it.text }}</div></div>
      <div v-else-if="it.kind === 'text'" class="text"><RichText :text="it.text" /></div>
      <div v-else-if="it.kind === 'reasoning'" class="think" :class="{ open: openThink[it.key] }">
        <button class="th" @click="openThink[it.key] = !openThink[it.key]">
          <Icon name="sparkle" :size="13" />
          <span>{{ it.live ? $t('agent.thinking') : $t('agent.thought') }}</span>
          <Icon :name="openThink[it.key] ? 'chevron-up' : 'chevron-down'" :size="12" />
        </button>
        <div v-if="openThink[it.key]" class="tbody">{{ it.text }}</div>
      </div>
      <ToolCard v-else-if="it.kind === 'tool' && !HIDDEN_TOOLS.includes(it.call.name)" :call="it.call" :result="it.result" />
      <div v-else-if="it.kind === 'error'" class="error"><Icon name="error" :size="14" /><span>{{ it.text }}</span></div>
    </template>
  </div>
</template>

<style scoped>
.ml { display: flex; flex-direction: column; gap: 4px; }
.user { display: flex; justify-content: flex-end; margin: 10px 0 6px; }
.bubble { max-width: 88%; background: var(--bg-sunken); border-radius: 12px; padding: 7px 11px; font-size: 13.5px; line-height: 1.55; white-space: pre-wrap; word-break: break-word; }
.bubble.steer { border: 1px dashed var(--border-strong); }
.text { padding: 2px 2px; }
.think { margin: 2px 0; }
.th { display: inline-flex; align-items: center; gap: 5px; border: 0; background: none; color: var(--text-3); font-size: 12px; padding: 2px 4px; border-radius: var(--radius-s); }
.th:hover { background: var(--bg-hover); color: var(--text-2); }
.tbody { font-size: 12px; color: var(--text-2); white-space: pre-wrap; line-height: 1.55; padding: 4px 10px; border-left: 2px solid var(--border); margin: 2px 0 6px 6px; max-height: 260px; overflow: auto; }
.error { display: flex; align-items: flex-start; gap: 6px; color: var(--danger); background: var(--danger-bg); border-radius: var(--radius-m); padding: 8px 10px; font-size: 12.5px; margin: 6px 0; }
.error .lm-icon { margin-top: 2px; }
</style>
