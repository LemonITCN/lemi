<script setup lang="ts">
// 下拉菜单：items 描述菜单项，触发器放在默认插槽里。
import { DropdownMenuRoot, DropdownMenuTrigger, DropdownMenuPortal, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuLabel } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'

export interface MenuItem { label?: string; icon?: string; danger?: boolean; disabled?: boolean; hint?: string; separator?: boolean; heading?: string; checked?: boolean; run?: () => void }
defineProps<{ items: MenuItem[]; align?: 'start' | 'center' | 'end'; side?: 'top' | 'bottom' | 'left' | 'right'; width?: number }>()
const open = defineModel<boolean>('open', { default: false })
</script>

<template>
  <DropdownMenuRoot v-model:open="open" :modal="false">
    <DropdownMenuTrigger as-child><slot /></DropdownMenuTrigger>
    <DropdownMenuPortal>
      <DropdownMenuContent class="lm-menu" :align="align || 'start'" :side="side || 'bottom'" :side-offset="4" :collision-padding="8" :style="{ minWidth: (width || 200) + 'px' }">
        <template v-for="(it, i) in items" :key="i">
          <DropdownMenuSeparator v-if="it.separator" class="lm-menu-sep" />
          <DropdownMenuLabel v-else-if="it.heading" class="lm-menu-heading">{{ it.heading }}</DropdownMenuLabel>
          <DropdownMenuItem v-else class="lm-menu-item" :class="{ danger: it.danger }" :disabled="it.disabled" @select="it.run?.()">
            <Icon v-if="it.icon" :name="it.icon" :size="16" />
            <span class="lm-menu-label">{{ it.label }}</span>
            <Icon v-if="it.checked" name="check" :size="15" class="lm-menu-check" />
            <span v-else-if="it.hint" class="lm-menu-hint">{{ it.hint }}</span>
          </DropdownMenuItem>
        </template>
      </DropdownMenuContent>
    </DropdownMenuPortal>
  </DropdownMenuRoot>
</template>

<style>
.lm-menu { background: var(--bg-elev); border-radius: var(--radius-m); box-shadow: var(--shadow-pop); padding: 4px; z-index: 1200; max-height: 70vh; overflow: auto; animation: lm-menu-in .1s; }
.lm-menu-item { display: flex; align-items: center; gap: 8px; height: 30px; padding: 0 8px; border-radius: 4px; color: var(--text); cursor: pointer; outline: none; font-size: 13.5px; user-select: none; }
.lm-menu-item[data-highlighted] { background: var(--bg-hover); }
.lm-menu-item[data-disabled] { opacity: .45; cursor: default; }
.lm-menu-item .lm-icon { color: var(--text-2); }
.lm-menu-item.danger, .lm-menu-item.danger .lm-icon { color: var(--danger); }
.lm-menu-label { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.lm-menu-hint { color: var(--text-3); font-size: 12px; }
.lm-menu-check { color: var(--accent) !important; }
.lm-menu-sep { height: 1px; background: var(--border); margin: 4px 0; }
.lm-menu-heading { font-size: 11.5px; color: var(--text-3); padding: 6px 8px 2px; font-weight: 500; }
@keyframes lm-menu-in { from { opacity: 0; transform: translateY(-2px); } }
</style>
