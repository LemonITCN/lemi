<script setup lang="ts">
// 智能体右侧面板：会话列表与新建、消息流、计划清单、待处理的审批与提问、输入框。
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import MessageList from './MessageList.vue'
import InteractionCard from './InteractionCard.vue'
import Composer from './Composer.vue'
import { useAgent } from './store'
import { draftItems, toItems } from './types'
import { useSession } from '@/stores/session'
import { useUI } from '@/stores/ui'
import { useToast } from '@/stores/toast'
import { confirmDialog } from '@/lib/confirm'
import { formatRelative } from '@/lib/time'
import { errorText, i18n } from '@/i18n'

const agent = useAgent()
const session = useSession()
const ui = useUI()
const toast = useToast()
const router = useRouter()
const t = i18n.global.t

const showList = ref(false)
const filter = ref('')
const scroller = ref<HTMLDivElement>()
const composer = ref<InstanceType<typeof Composer>>()
const renaming = ref(false)
const newTitle = ref('')
const todoOpen = ref(true)

const canUse = computed(() => session.atLeast('member'))
const items = computed(() => [...toItems(agent.messages), ...draftItems(agent.draft)])
const filtered = computed(() => {
  const q = filter.value.trim().toLowerCase()
  return agent.conversations.filter((c) => !q || c.title.toLowerCase().includes(q))
})
const doneTodos = computed(() => agent.todos.filter((x) => x.status === 'completed').length)
const activeSubs = computed(() => agent.subagents.filter((s) => !s.done))

async function init() {
  if (!session.current || !canUse.value) return
  try {
    await Promise.all([agent.listLoaded ? null : agent.loadList(), agent.models.length ? null : agent.loadModels()])
  } catch (e) { toast.error(errorText(e)) }
}
onMounted(init)
watch(() => session.current?.id, () => nextTick(init))

async function pick(id: string) {
  showList.value = false
  try { await agent.open(id) } catch (e) { toast.error(errorText(e)) }
  scrollBottom(true)
}

function newChat() {
  showList.value = false
  agent.newChat()
  nextTick(() => composer.value?.focus())
}

const convMenu = computed<MenuItem[]>(() => agent.conv ? [
  { label: t('common.rename'), icon: 'edit', run: () => { newTitle.value = agent.conv!.title; renaming.value = true } },
  { label: t('agent.archive'), icon: 'archive', run: async () => { try { await agent.patch(agent.conv!.id, { archived: true }); agent.newChat() } catch (e) { toast.error(errorText(e)) } } },
  { separator: true },
  { label: t('common.delete'), icon: 'trash', danger: true, run: async () => {
    if (await confirmDialog({ title: t('agent.deleteTitle'), message: t('agent.deleteConfirm', { name: agent.conv!.title }), danger: true, okText: t('common.delete') })) {
      try { await agent.remove(agent.conv!.id) } catch (e) { toast.error(errorText(e)) }
    }
  } },
] : [])

async function rename() {
  try { await agent.patch(agent.conv!.id, { title: newTitle.value }); renaming.value = false } catch (e) { toast.error(errorText(e)) }
}

// 自动滚到底：用户停在底部附近时跟随新内容
let stick = true
function onScroll() {
  const el = scroller.value
  if (el) stick = el.scrollHeight - el.scrollTop - el.clientHeight < 80
}
function scrollBottom(force = false) {
  nextTick(() => {
    const el = scroller.value
    if (el && (force || stick)) el.scrollTop = el.scrollHeight
  })
}
watch(() => [items.value.length, agent.draft?.text.length, agent.draft?.tools.length, agent.pending.length], () => scrollBottom())

const noticeText = computed(() => {
  const n = agent.notice
  if (!n) return ''
  return n.text === 'compacted' ? t('agent.compacted') : n.text
})
</script>

<template>
  <div class="ap">
    <div class="top">
      <IconBtn :icon="showList ? 'chevron-left' : 'list'" :label="$t('agent.history')" :active="showList" @click="showList = !showList" />
      <span class="title lm-ellipsis">{{ showList ? $t('agent.history') : agent.conv?.title || $t('agent.newChat') }}</span>
      <span v-if="agent.conv?.settings.mode === 'plan' && !showList" class="lm-badge accent">{{ $t('agent.planMode') }}</span>
      <span class="lm-spacer" />
      <Menu v-if="agent.conv && !showList" :items="convMenu" align="end" :width="180"><IconBtn icon="more" :label="$t('common.more')" /></Menu>
      <IconBtn icon="plus" :label="$t('agent.newChat')" @click="newChat" />
      <IconBtn v-if="!ui.isMobile" icon="close" :label="$t('common.close')" @click="ui.aiOpen = false" />
    </div>

    <div v-if="!canUse" class="blocked">
      <Icon name="lock" :size="28" :stroke="1.3" />
      <p>{{ $t('agent.needMember') }}</p>
    </div>
    <div v-else-if="!agent.configured" class="blocked">
      <Icon name="plug" :size="28" :stroke="1.3" />
      <p>{{ $t('agent.notConfigured') }}</p>
      <Btn v-if="session.isAdmin" size="sm" @click="router.push('/settings/ai-providers')">{{ $t('agent.goConfigure') }}</Btn>
    </div>

    <!-- 会话列表 -->
    <div v-else-if="showList" class="list">
      <div class="search"><Icon name="search" :size="14" /><input v-model="filter" :placeholder="$t('agent.searchChats')" /></div>
      <div class="items">
        <button v-for="c in filtered" :key="c.id" class="conv" :class="{ on: c.id === agent.convId }" @click="pick(c.id)">
          <span class="ct lm-ellipsis">{{ c.title || $t('agent.untitled') }}</span>
          <span v-if="c.running" class="dot" />
          <span class="cd">{{ formatRelative(c.updated_at) }}</span>
        </button>
        <div v-if="agent.listLoaded && !filtered.length" class="lm-empty">{{ $t('agent.noChats') }}</div>
      </div>
    </div>

    <template v-else>
      <div v-if="agent.todos.length" class="todo">
        <button class="todo-h" @click="todoOpen = !todoOpen">
          <Icon name="list-check" :size="14" />
          <span>{{ $t('agent.plan') }}</span>
          <span class="lm-badge">{{ doneTodos }}/{{ agent.todos.length }}</span>
          <span class="lm-spacer" />
          <Icon :name="todoOpen ? 'chevron-up' : 'chevron-down'" :size="12" />
        </button>
        <ul v-if="todoOpen">
          <li v-for="(x, i) in agent.todos" :key="i" :class="x.status">
            <span class="box"><Icon v-if="x.status === 'completed'" name="check" :size="10" /><span v-else-if="x.status === 'in_progress'" class="pulse" /></span>
            <span class="tx">{{ x.content }}</span>
          </li>
        </ul>
      </div>

      <div ref="scroller" class="scroll" @scroll="onScroll">
        <div v-if="agent.loading" class="lm-empty">{{ $t('common.loading') }}</div>
        <div v-else-if="!items.length && !agent.running" class="empty">
          <Icon name="assistant" :size="30" :stroke="1.2" />
          <p class="et">{{ $t('agent.emptyTitle') }}</p>
          <p class="ed">{{ $t('agent.emptyDesc') }}</p>
        </div>
        <div v-if="agent.summary && agent.messages.length" class="checkpoint">
          <Icon name="archive" :size="13" />{{ $t('agent.checkpoint') }}
        </div>
        <MessageList :items="items" />
        <div v-if="activeSubs.length" class="subs">
          <div v-for="s in activeSubs" :key="s.task_id" class="sub">
            <span class="spin" /><span class="sd">{{ s.description }}</span>
            <span class="lm-badge">{{ s.type }}</span>
            <span class="ss lm-ellipsis">{{ s.steps.length ? s.steps[s.steps.length - 1].title || s.steps[s.steps.length - 1].name : '' }}</span>
          </div>
        </div>
        <div v-if="agent.running && !agent.pending.length" class="status">
          <span class="spin" />
          <span v-if="agent.compacting">{{ $t('agent.compacting') }}</span>
          <span v-else-if="agent.retrying">{{ $t('agent.retrying', { n: agent.retrying.attempt, s: Math.round((agent.retrying.wait_ms || 0) / 1000), why: agent.retrying.label }) }}</span>
          <span v-else>{{ $t('agent.working') }}</span>
        </div>
        <div v-if="noticeText" class="notice">{{ noticeText }}</div>
        <div v-if="agent.runState === 'stopped'" class="notice">{{ $t('agent.stopped') }}</div>
        <div v-if="agent.runError" class="err"><Icon name="error" :size="14" />{{ agent.runError }}</div>
        <div v-for="(q, i) in agent.queuedTexts" :key="i" class="queued"><Icon name="clock" :size="12" />{{ q }}</div>
      </div>

      <div v-if="agent.pending.length" class="pending">
        <InteractionCard v-for="p in agent.pending" :key="p.id" :it="p" />
      </div>
      <Composer ref="composer" />
    </template>

    <Modal v-model:open="renaming" :title="$t('common.rename')">
      <TextField v-model="newTitle" autofocus @keydown.enter="rename" />
      <template #footer><Btn @click="renaming = false">{{ $t('common.cancel') }}</Btn><Btn variant="primary" @click="rename">{{ $t('common.save') }}</Btn></template>
    </Modal>
  </div>
</template>

<style scoped>
.ap { flex: 1; display: flex; flex-direction: column; min-height: 0; background: var(--bg); }
.top { height: 40px; flex: none; display: flex; align-items: center; gap: 4px; padding: 0 6px; border-bottom: 1px solid var(--border); }
.title { font-weight: 600; font-size: 13px; min-width: 0; padding-left: 2px; }
.blocked { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px; color: var(--text-3); padding: 24px; text-align: center; font-size: 13px; }
.list { flex: 1; display: flex; flex-direction: column; min-height: 0; }
.search { display: flex; align-items: center; gap: 6px; margin: 8px; padding: 0 8px; height: 30px; border: 1px solid var(--border); border-radius: var(--radius-m); color: var(--text-3); }
.search input { flex: 1; border: 0; outline: none; background: none; font-size: 13px; color: var(--text); }
.items { flex: 1; overflow: auto; padding: 0 6px 8px; }
.conv { width: 100%; display: flex; align-items: center; gap: 8px; height: 34px; padding: 0 8px; border: 0; background: none; border-radius: var(--radius-m); text-align: left; font-size: 13px; color: var(--text); }
.conv:hover { background: var(--bg-hover); }
.conv.on { background: var(--accent-soft); }
.ct { flex: 1; min-width: 0; }
.cd { font-size: 11.5px; color: var(--text-3); flex: none; }
.dot { width: 6px; height: 6px; border-radius: 50%; background: var(--accent); flex: none; }
.todo { flex: none; border-bottom: 1px solid var(--border); background: var(--bg-soft); max-height: 40%; overflow: auto; }
.todo-h { width: 100%; display: flex; align-items: center; gap: 6px; height: 32px; padding: 0 10px; border: 0; background: none; font-size: 12.5px; font-weight: 500; color: var(--text-2); }
.todo ul { list-style: none; margin: 0; padding: 0 10px 8px; display: flex; flex-direction: column; gap: 3px; }
.todo li { display: flex; align-items: flex-start; gap: 7px; font-size: 12.5px; line-height: 1.45; }
.todo .box { width: 13px; height: 13px; border: 1px solid var(--border-strong); border-radius: 3px; flex: none; margin-top: 2px; display: flex; align-items: center; justify-content: center; }
.todo li.completed .box { background: var(--success); border-color: var(--success); color: #fff; }
.todo li.completed .tx, .todo li.cancelled .tx { color: var(--text-3); text-decoration: line-through; }
.todo li.in_progress .tx { font-weight: 500; }
.todo li.in_progress .box { border-color: var(--accent); }
.pulse { width: 5px; height: 5px; border-radius: 50%; background: var(--accent); animation: pl 1.2s ease-in-out infinite; }
@keyframes pl { 50% { opacity: .3; } }
.scroll { flex: 1; overflow: auto; padding: 8px 12px 12px; min-height: 0; }
.empty { display: flex; flex-direction: column; align-items: center; text-align: center; color: var(--text-3); padding: 48px 16px 24px; gap: 6px; }
.et { margin: 6px 0 0; font-size: 14px; font-weight: 600; color: var(--text-2); }
.ed { margin: 0; font-size: 12.5px; line-height: 1.6; max-width: 300px; }
.checkpoint { display: flex; align-items: center; gap: 6px; font-size: 11.5px; color: var(--text-3); justify-content: center; padding: 4px 0 8px; }
.subs { display: flex; flex-direction: column; gap: 4px; margin: 6px 0; }
.sub { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-2); border: 1px solid var(--border); border-radius: var(--radius-m); padding: 5px 8px; }
.sd { font-weight: 500; color: var(--text); }
.ss { color: var(--text-3); flex: 1; min-width: 0; }
.status { display: flex; align-items: center; gap: 7px; font-size: 12px; color: var(--text-3); padding: 8px 2px; }
.notice { font-size: 12px; color: var(--text-3); padding: 4px 2px; }
.err { display: flex; align-items: flex-start; gap: 6px; color: var(--danger); background: var(--danger-bg); border-radius: var(--radius-m); padding: 8px 10px; font-size: 12.5px; margin: 6px 0; }
.queued { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-3); border: 1px dashed var(--border-strong); border-radius: var(--radius-m); padding: 5px 8px; margin: 4px 0; }
.pending { flex: none; padding: 8px 10px 0; display: flex; flex-direction: column; gap: 8px; max-height: 55%; overflow: auto; }
.spin { width: 11px; height: 11px; border: 1.5px solid var(--border-strong); border-top-color: var(--accent); border-radius: 50%; animation: sp .8s linear infinite; flex: none; }
@keyframes sp { to { transform: rotate(360deg); } }
</style>
