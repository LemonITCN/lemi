<script setup lang="ts">
// 选择通知渠道（多选）：备份计划的成功/失败通知、健康检查的状态变化通知共用。
import { computed, onMounted } from 'vue'
import { PopoverRoot, PopoverTrigger, PopoverPortal, PopoverContent } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'
import { useChannels, kindName } from './store'
import { useSession } from '@/stores/session'

const model = defineModel<string[]>({ default: () => [] })
defineProps<{ disabled?: boolean }>()
const store = useChannels()
const session = useSession()
onMounted(() => store.load().catch(() => {}))

const selected = computed(() => model.value.map((id) => store.byId(id)).filter(Boolean) as any[])
function toggle(id: string) {
  model.value = model.value.includes(id) ? model.value.filter((x) => x !== id) : [...model.value, id]
}
</script>

<template>
  <PopoverRoot>
    <PopoverTrigger as-child :disabled="disabled">
      <button type="button" class="trigger" :disabled="disabled">
        <span v-if="!selected.length" class="ph">{{ $t('notify.none') }}</span>
        <span v-for="c in selected" :key="c.id" class="chip"><Icon name="bell" :size="12" />{{ c.name }}</span>
        <Icon name="chevron-down" :size="14" class="caret" />
      </button>
    </PopoverTrigger>
    <PopoverPortal>
      <PopoverContent class="pop" side="bottom" align="start" :side-offset="4">
        <div v-if="!store.list.length" class="empty">
          {{ $t('notify.noChannels') }}
          <RouterLink v-if="session.atLeast('admin')" to="/settings/notify-channels" class="link">{{ $t('notify.manageChannels') }}</RouterLink>
        </div>
        <label v-for="c in store.list" :key="c.id" class="opt" :class="{ off: !c.enabled }">
          <input type="checkbox" :checked="model.includes(c.id)" @change="toggle(c.id)" />
          <span class="lm-ellipsis">{{ c.name }}</span>
          <span class="k">{{ kindName(store.kind(c.kind)) }}</span>
        </label>
      </PopoverContent>
    </PopoverPortal>
  </PopoverRoot>
</template>

<style scoped>
.trigger { min-height: 34px; width: 100%; display: flex; flex-wrap: wrap; align-items: center; gap: 4px; padding: 4px 28px 4px 8px; border: 1px solid var(--border-strong);
  border-radius: var(--radius-m); background: var(--bg); text-align: left; position: relative; }
.trigger:disabled { opacity: .7; }
.trigger:focus-visible { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); outline: none; }
.ph { color: var(--text-3); font-size: 13px; }
.chip { display: inline-flex; align-items: center; gap: 4px; height: 22px; padding: 0 8px; border-radius: 11px; background: var(--bg-active); font-size: 12px; }
.caret { position: absolute; right: 8px; top: 50%; transform: translateY(-50%); color: var(--text-3); }
.pop { z-index: 1200; min-width: 240px; max-width: 360px; max-height: 300px; overflow: auto; padding: 6px; background: var(--bg-elev); border-radius: var(--radius-m); box-shadow: var(--shadow-pop); }
.opt { display: flex; align-items: center; gap: 8px; padding: 6px 8px; border-radius: var(--radius-s); font-size: 13px; cursor: pointer; }
.opt:hover { background: var(--bg-hover); }
.opt.off { color: var(--text-3); }
.k { margin-left: auto; font-size: 11.5px; color: var(--text-3); flex: none; }
.empty { padding: 10px; font-size: 12.5px; color: var(--text-3); display: flex; flex-direction: column; gap: 6px; }
.link { color: var(--accent); }
</style>
