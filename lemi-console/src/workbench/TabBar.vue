<script setup lang="ts">
import { computed } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import { tabType, type Tab } from '@/modules/registry'
import { useTabs } from '@/stores/tabs'
import { useUI } from '@/stores/ui'
import { i18n } from '@/i18n'

const tabs = useTabs()
const ui = useUI()
const t = i18n.global.t

function icon(tab: Tab) {
  const tt = tabType(tab.type)
  if (!tt) return 'page'
  return typeof tt.icon === 'function' ? tt.icon(tab) : tt.icon
}

function ctx(tab: Tab): MenuItem[] {
  return [
    { label: t('shell.closeTab'), run: () => tabs.close(tab.id) },
    { label: t('shell.closeOthers'), run: () => tabs.closeOthers(tab.id) },
    { label: t('shell.closeAll'), run: () => tabs.closeAll() },
  ]
}

const mobileItems = computed<MenuItem[]>(() => tabs.tabs.map((x) => ({ label: x.title, icon: icon(x), checked: x.id === tabs.activeId, run: () => tabs.activate(x.id) })))

let dragFrom = -1
function onDrop(i: number) {
  if (dragFrom >= 0 && dragFrom !== i) tabs.move(dragFrom, i)
  dragFrom = -1
}
</script>

<template>
  <div v-if="tabs.tabs.length" class="bar">
    <template v-if="!ui.isMobile">
      <div class="scroller">
        <div v-for="(x, i) in tabs.tabs" :key="x.id" class="tab" :class="{ on: x.id === tabs.activeId }" draggable="true"
          :title="x.title" @click="tabs.activate(x.id)" @mousedown.middle.prevent="tabs.close(x.id)"
          @dragstart="dragFrom = i" @dragover.prevent @drop="onDrop(i)">
          <Icon :name="icon(x)" :size="14" />
          <span class="title lm-ellipsis">{{ x.title }}</span>
          <span v-if="x.fromAI" class="ai" :title="$t('shell.fromAI')">AI</span>
          <span v-if="x.dirty" class="dot" :title="$t('shell.unsaved')" />
          <Menu :items="ctx(x)" :width="160"><button class="more" @click.stop><Icon name="chevron-down" :size="12" /></button></Menu>
          <button class="x" :aria-label="$t('shell.closeTab')" @click.stop="tabs.close(x.id)"><Icon name="close" :size="12" /></button>
        </div>
      </div>
    </template>
    <template v-else>
      <Menu :items="mobileItems" :width="260">
        <button class="m-tab"><Icon :name="tabs.active ? icon(tabs.active) : 'page'" :size="15" /><span class="lm-ellipsis">{{ tabs.active?.title }}</span><Icon name="chevron-down" :size="14" /><span class="count">{{ tabs.tabs.length }}</span></button>
      </Menu>
      <span class="lm-spacer" />
      <button v-if="tabs.active" class="x m-x" @click="tabs.close(tabs.activeId)"><Icon name="close" :size="16" /></button>
    </template>
  </div>
</template>

<style scoped>
.bar { height: 34px; flex: none; display: flex; align-items: stretch; border-bottom: 1px solid var(--border); background: var(--bg-soft); }
.scroller { display: flex; overflow-x: auto; scrollbar-width: none; flex: 1; }
.scroller::-webkit-scrollbar { display: none; }
.tab { display: flex; align-items: center; gap: 6px; padding: 0 6px 0 10px; max-width: 240px; min-width: 90px; border-right: 1px solid var(--border); cursor: default;
  color: var(--text-2); font-size: 12.5px; user-select: none; position: relative; }
.tab:hover { background: var(--bg-hover); }
.tab.on { background: var(--bg); color: var(--text); }
.tab.on::after { content: ''; position: absolute; left: 0; right: 0; top: 0; height: 2px; background: var(--accent); }
.title { flex: 1; min-width: 0; }
.ai { font-size: 9.5px; font-weight: 700; color: var(--accent); border: 1px solid var(--accent); border-radius: 3px; padding: 0 3px; line-height: 13px; }
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--warning); flex: none; }
.x, .more { border: 0; background: none; padding: 3px; border-radius: 4px; color: var(--text-3); display: flex; }
.more { display: none; }
.tab:hover .more { display: flex; }
.x:hover, .more:hover { background: var(--bg-active); color: var(--text); }
.m-tab { display: flex; align-items: center; gap: 6px; border: 0; background: none; padding: 0 12px; max-width: 80vw; font-size: 13px; }
.count { font-size: 11px; background: var(--bg-active); border-radius: 8px; padding: 0 6px; color: var(--text-2); }
.m-x { padding: 0 12px; }
</style>
