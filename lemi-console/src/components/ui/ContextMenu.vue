<script setup lang="ts">
// 右键菜单（触屏上长按）：items 同 Menu。
import { ContextMenuRoot, ContextMenuTrigger, ContextMenuPortal, ContextMenuContent, ContextMenuItem, ContextMenuSeparator, ContextMenuLabel } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'
import type { MenuItem } from './Menu.vue'
defineProps<{ items: MenuItem[]; width?: number; disabled?: boolean }>()
</script>

<template>
  <ContextMenuRoot :modal="false">
    <ContextMenuTrigger as-child :disabled="disabled"><slot /></ContextMenuTrigger>
    <ContextMenuPortal>
      <ContextMenuContent class="lm-menu" :collision-padding="8" :style="{ minWidth: (width || 200) + 'px' }">
        <template v-for="(it, i) in items" :key="i">
          <ContextMenuSeparator v-if="it.separator" class="lm-menu-sep" />
          <ContextMenuLabel v-else-if="it.heading" class="lm-menu-heading">{{ it.heading }}</ContextMenuLabel>
          <ContextMenuItem v-else class="lm-menu-item" :class="{ danger: it.danger }" :disabled="it.disabled" @select="it.run?.()">
            <Icon v-if="it.icon" :name="it.icon" :size="16" />
            <span class="lm-menu-label">{{ it.label }}</span>
            <Icon v-if="it.checked" name="check" :size="15" class="lm-menu-check" />
            <span v-else-if="it.hint" class="lm-menu-hint">{{ it.hint }}</span>
          </ContextMenuItem>
        </template>
      </ContextMenuContent>
    </ContextMenuPortal>
  </ContextMenuRoot>
</template>
