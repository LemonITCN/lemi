<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import Logo from '@/components/brand/Logo.vue'
import { modules, railItems, rightPanel, type RailItem } from '@/modules/registry'
import { useSession } from '@/stores/session'
import { useUI } from '@/stores/ui'
import { useTabs } from '@/stores/tabs'
import { startRealtime, setWorkspace } from '@/lib/realtime'
import { startResize } from '@/lib/resize'
import { setLocale, i18n } from '@/i18n'

const session = useSession()
const ui = useUI()
const tabsStore = useTabs()
const route = useRoute()
const router = useRouter()
const t = i18n.global.t

const rail = computed(() => railItems())
const current = computed<RailItem | undefined>(() => rail.value.find((r) => r.id === ui.rail) || rail.value[0])
const panelComp = shallowRef<any>(null)
watch(current, (r) => { panelComp.value = r?.panel ? defineAsyncComponent(r.panel) : null }, { immediate: true })
const rp = rightPanel()
const rightComp = rp ? defineAsyncComponent(rp.component) : null

/** 手机：当前显示哪一屏 */
const mobileView = computed(() => (route.query.m as string) || 'work')
function setMobile(v: string) { router.replace({ query: { ...route.query, m: v } }) }

function clickRail(r: RailItem) {
  if (r.route) {
    router.push(r.route)
    return
  }
  if (ui.rail === r.id && ui.leftOpen && route.name === 'workbench') {
    ui.leftOpen = false
    return
  }
  ui.rail = r.id
  ui.leftOpen = true
  if (route.name !== 'workbench' && !ui.isMobile) router.push('/')
  if (ui.isMobile) setMobile('panel')
}

const wsItems = computed<MenuItem[]>(() => [
  { heading: t('shell.workspaces') },
  ...session.workspaces.map((w) => ({ label: w.name, checked: w.id === session.current?.id, run: () => session.selectWorkspace(w.id) })),
  { separator: true },
  { label: t('shell.workspaceSettings'), icon: 'settings', run: () => router.push('/settings/workspace') },
])

const userItems = computed<MenuItem[]>(() => [
  { heading: session.user?.display_name || session.user?.username },
  { label: t('shell.account'), icon: 'user', run: () => router.push('/settings/account') },
  { label: t('common.settings'), icon: 'settings', run: () => router.push('/settings') },
  { separator: true },
  { heading: t('shell.theme') },
  { label: t('shell.themeSystem'), checked: ui.theme === 'system', run: () => (ui.theme = 'system') },
  { label: t('shell.themeLight'), checked: ui.theme === 'light', run: () => (ui.theme = 'light') },
  { label: t('shell.themeDark'), checked: ui.theme === 'dark', run: () => (ui.theme = 'dark') },
  { separator: true },
  { label: '中文', checked: i18n.global.locale.value === 'zh-CN', run: () => setLocale('zh-CN') },
  { label: 'English', checked: i18n.global.locale.value === 'en', run: () => setLocale('en') },
  { separator: true },
  { label: t('auth.logout'), icon: 'logout', run: async () => { await session.logout(); router.replace({ name: 'login' }) } },
])

watch(() => session.current?.id, (id) => setWorkspace(id || ''), { immediate: true })
// 手机上从对象树或智能体打开标签后自动切到工作区
watch(() => tabsStore.activeId, (id, old) => { if (ui.isMobile && id && id !== old && mobileView.value !== 'work') setMobile('work') })

onMounted(() => {
  startRealtime()
  for (const m of modules) m.setup?.()
})

const showLeft = computed(() => ui.leftOpen && !!panelComp.value && route.name === 'workbench')
const initial = computed(() => (session.current?.name || '?').slice(0, 1).toUpperCase())
</script>

<template>
  <div class="shell" :class="{ mobile: ui.isMobile }">
    <div v-if="session.insecure" class="banner">{{ $t('auth.insecure') }}</div>
    <div class="body">
      <!-- 电脑与平板 -->
      <template v-if="!ui.isMobile">
        <nav class="rail">
          <Menu :items="wsItems" side="right" :width="240">
            <button class="ws" :title="session.current?.name"><span>{{ initial }}</span></button>
          </Menu>
          <button v-for="r in rail" :key="r.id" class="rail-btn" :class="{ on: (r.route ? route.path.startsWith(r.route) : ui.rail === r.id && showLeft) }" :title="$t(r.label)" @click="clickRail(r)">
            <Icon :name="r.icon" :size="20" />
          </button>
          <span class="lm-spacer" />
          <button v-if="rp" class="rail-btn" :class="{ on: ui.aiOpen }" :title="$t(rp.label)" @click="ui.aiOpen = !ui.aiOpen"><Icon :name="rp.icon" :size="20" /></button>
          <Menu :items="userItems" side="right" :width="220">
            <button class="rail-btn" :title="session.user?.username"><Icon name="user" :size="20" /></button>
          </Menu>
        </nav>
        <aside v-if="showLeft" class="left" :style="{ width: ui.leftWidth + 'px' }">
          <component :is="panelComp" />
          <div class="grip" @pointerdown="startResize($event, () => ui.leftWidth, (w) => (ui.leftWidth = w), 200, 600)" />
        </aside>
        <main class="main"><RouterView /></main>
        <aside v-if="rightComp && ui.aiOpen" class="right" :style="{ width: (ui.isTablet ? Math.min(ui.rightWidth, 380) : ui.rightWidth) + 'px' }">
          <div class="grip left-grip" @pointerdown="startResize($event, () => ui.rightWidth, (w) => (ui.rightWidth = w), 320, 900, -1)" />
          <component :is="rightComp" />
        </aside>
      </template>
      <!-- 手机：一次显示一屏，底部导航切换 -->
      <template v-else>
        <div class="m-top">
          <Menu :items="wsItems" :width="240"><button class="m-ws"><Logo :size="22" /><span class="lm-ellipsis">{{ session.current?.name }}</span><Icon name="chevron-down" :size="14" /></button></Menu>
          <span class="lm-spacer" />
          <Menu :items="userItems" align="end" :width="220"><IconBtn icon="user" :label="session.user?.username || ''" /></Menu>
        </div>
        <div class="m-body">
          <div v-show="mobileView === 'panel'" class="m-screen"><component :is="panelComp" /></div>
          <div v-show="mobileView === 'work' || route.name !== 'workbench'" class="m-screen"><RouterView /></div>
          <div v-if="rightComp" v-show="mobileView === 'ai'" class="m-screen"><component :is="rightComp" /></div>
        </div>
        <nav class="m-nav">
          <button v-for="r in rail.filter((x) => x.mobile)" :key="r.id" :class="{ on: mobileView === 'panel' && ui.rail === r.id }" @click="clickRail(r)">
            <Icon :name="r.icon" :size="20" /><span>{{ $t(r.label) }}</span>
          </button>
          <button :class="{ on: mobileView === 'work' }" @click="setMobile('work'); route.name !== 'workbench' && router.push('/')"><Icon name="tabs" :size="20" /><span>{{ $t('shell.workbench') }}</span></button>
          <button v-if="rp" :class="{ on: mobileView === 'ai' }" @click="setMobile('ai')"><Icon :name="rp.icon" :size="20" /><span>{{ $t(rp.label) }}</span></button>
        </nav>
      </template>
    </div>
  </div>
</template>

<style scoped>
.shell { height: 100%; display: flex; flex-direction: column; background: var(--bg); }
.banner { background: var(--danger); color: #fff; font-size: 12.5px; padding: 5px 12px; text-align: center; }
.body { flex: 1; display: flex; min-height: 0; }
.rail { width: 48px; flex: none; display: flex; flex-direction: column; align-items: center; gap: 2px; padding: 8px 0; background: var(--bg-sunken); border-right: 1px solid var(--border); }
.ws { width: 32px; height: 32px; border-radius: 8px; border: 0; background: var(--accent); color: #fff; font-weight: 650; margin-bottom: 8px; }
.rail-btn { width: 36px; height: 36px; display: flex; align-items: center; justify-content: center; border: 0; background: none; border-radius: 8px; color: var(--text-2); position: relative; }
.rail-btn:hover { background: var(--bg-hover); color: var(--text); }
.rail-btn.on { color: var(--accent); background: var(--accent-soft); }
.left { position: relative; flex: none; border-right: 1px solid var(--border); background: var(--bg-soft); display: flex; flex-direction: column; min-width: 0; }
.right { position: relative; flex: none; border-left: 1px solid var(--border); background: var(--bg); display: flex; flex-direction: column; min-width: 0; }
.grip { position: absolute; top: 0; bottom: 0; right: -3px; width: 6px; cursor: col-resize; z-index: 5; }
.left-grip { left: -3px; right: auto; }
.main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.shell.mobile .body { flex-direction: column; }
.m-top { height: 46px; flex: none; display: flex; align-items: center; padding: 0 8px 0 12px; border-bottom: 1px solid var(--border); padding-top: env(safe-area-inset-top); }
.m-ws { display: flex; align-items: center; gap: 8px; border: 0; background: none; font-weight: 600; max-width: 70vw; }
.m-body { flex: 1; min-height: 0; position: relative; }
.m-screen { position: absolute; inset: 0; display: flex; flex-direction: column; overflow: hidden; }
.m-nav { flex: none; display: flex; border-top: 1px solid var(--border); background: var(--bg-soft); padding-bottom: env(safe-area-inset-bottom); }
.m-nav button { flex: 1; display: flex; flex-direction: column; align-items: center; gap: 2px; padding: 6px 0; border: 0; background: none; color: var(--text-2); font-size: 11px; }
.m-nav button.on { color: var(--accent); }
</style>
