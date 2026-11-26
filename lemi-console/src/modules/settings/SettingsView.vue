<script setup lang="ts">
import { computed, defineAsyncComponent, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import { settingsPages, type SettingsPage } from '@/modules/registry'
import { useSession } from '@/stores/session'
import { useUI } from '@/stores/ui'

const route = useRoute()
const router = useRouter()
const session = useSession()
const ui = useUI()

const pages = computed(() => settingsPages().filter((p) => {
  if (p.group === 'admin') return session.isAdmin
  if (p.group === 'workspace') return !!session.current && (!p.minRole || session.atLeast(p.minRole))
  return true
}))
const groups = computed(() => [
  { id: 'account', label: 'settings.groupAccount', pages: pages.value.filter((p) => p.group === 'account') },
  { id: 'workspace', label: 'settings.groupWorkspace', pages: pages.value.filter((p) => p.group === 'workspace') },
  { id: 'admin', label: 'settings.groupAdmin', pages: pages.value.filter((p) => p.group === 'admin') },
].filter((g) => g.pages.length))

const currentId = computed(() => (route.params.section as string) || (ui.isMobile ? '' : 'account'))
const page = computed<SettingsPage | undefined>(() => pages.value.find((p) => p.id === currentId.value))
const comp = shallowRef<any>(null)
watch(page, (p) => { comp.value = p ? defineAsyncComponent(p.component) : null }, { immediate: true })
</script>

<template>
  <div class="settings">
    <nav v-if="!ui.isMobile || !page" class="nav">
      <div class="nav-head"><button class="back" @click="router.push('/')"><Icon name="chevron-left" :size="16" /></button>{{ $t('common.settings') }}</div>
      <template v-for="g in groups" :key="g.id">
        <div class="group">{{ $t(g.label) }}<span v-if="g.id === 'workspace' && session.current" class="ws lm-ellipsis"> · {{ session.current.name }}</span></div>
        <RouterLink v-for="p in g.pages" :key="p.id" :to="'/settings/' + p.id" class="item" :class="{ on: p.id === currentId }">
          <Icon :name="p.icon" :size="16" /><span>{{ $t(p.label) }}</span>
        </RouterLink>
      </template>
    </nav>
    <section v-if="page" class="page">
      <div v-if="ui.isMobile" class="m-back"><button class="back" @click="router.push('/settings')"><Icon name="chevron-left" :size="16" />{{ $t('common.settings') }}</button></div>
      <div class="inner">
        <h2>{{ $t(page.label) }}</h2>
        <component :is="comp" :key="page.id" :page="page" />
      </div>
    </section>
  </div>
</template>

<style scoped>
.settings { flex: 1; display: flex; min-height: 0; }
.nav { width: 230px; flex: none; border-right: 1px solid var(--border); background: var(--bg-soft); padding: 8px; overflow: auto; }
.nav-head { display: flex; align-items: center; gap: 6px; font-weight: 600; padding: 6px 4px 10px; }
.back { border: 0; background: none; display: inline-flex; align-items: center; gap: 4px; padding: 4px; border-radius: 6px; color: var(--text-2); }
.back:hover { background: var(--bg-hover); }
.group { font-size: 11.5px; color: var(--text-3); padding: 12px 8px 4px; font-weight: 500; display: flex; min-width: 0; }
.ws { flex: 1; min-width: 0; }
.item { display: flex; align-items: center; gap: 8px; height: 30px; padding: 0 8px; border-radius: 6px; color: var(--text-2); text-decoration: none; font-size: 13px; }
.item:hover { background: var(--bg-hover); color: var(--text); }
.item.on { background: var(--accent-soft); color: var(--accent); }
.page { flex: 1; overflow: auto; }
.inner { max-width: 880px; padding: 24px 32px 64px; }
.inner h2 { font-size: 18px; font-weight: 600; margin: 0 0 20px; }
.m-back { padding: 8px; border-bottom: 1px solid var(--border); }
@media (max-width: 760px) { .nav { width: 100%; border: 0; } .inner { padding: 16px; } }
</style>
