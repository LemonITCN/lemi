<script setup lang="ts">
import { computed, defineAsyncComponent, type Component } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import TabBar from './TabBar.vue'
import { tabType, type Tab } from '@/modules/registry'
import { useTabs } from '@/stores/tabs'

const tabs = useTabs()
const cache = new Map<string, Component>()
function comp(t: Tab): Component | null {
  const tt = tabType(t.type)
  if (!tt) return null
  let c = cache.get(t.type)
  if (!c) {
    c = defineAsyncComponent(tt.component)
    cache.set(t.type, c)
  }
  return c
}
const list = computed(() => tabs.tabs)
</script>

<template>
  <div class="wb">
    <TabBar />
    <div class="content">
      <template v-for="t in list" :key="t.id">
        <div v-show="t.id === tabs.activeId" class="pane">
          <component :is="comp(t)" v-if="comp(t)" :tab="t" :active="t.id === tabs.activeId" />
          <div v-else class="empty">未知的标签类型：{{ t.type }}</div>
        </div>
      </template>
      <div v-if="!list.length" class="empty">
        <Icon name="database" :size="36" :stroke="1.2" />
        <p>{{ $t('shell.noTabs') }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.wb { flex: 1; display: flex; flex-direction: column; min-height: 0; }
.content { flex: 1; min-height: 0; position: relative; }
.pane { position: absolute; inset: 0; display: flex; flex-direction: column; min-height: 0; }
.empty { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px; color: var(--text-3); padding: 24px; text-align: center; }
.empty p { margin: 0; font-size: 13px; }
</style>
