<script setup lang="ts">
// 设置 → 通知渠道：渠道列表（启停、测试、编辑、删除）与发送记录。查看要空间成员，修改要空间管理员。
import { computed, onMounted, ref, watch } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Switch from '@/components/ui/Switch.vue'
import Icon from '@/components/icons/Icon.vue'
import ChannelEditor from './ChannelEditor.vue'
import { useChannels, kindName, type Channel } from './store'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { confirmDialog } from '@/lib/confirm'
import { formatDate } from '@/lib/time'
import { errorText, i18n, isZh } from '@/i18n'

const store = useChannels()
const session = useSession()
const toast = useToast()
const t = i18n.global.t
const canEdit = computed(() => session.atLeast('admin'))
const editing = ref<Channel | null>(null)
const editorOpen = ref(false)
const testing = ref('')
const logs = ref<any[]>([])
const logCursor = ref('')
const logsLoading = ref(false)

async function load() {
  try { await store.load(true) } catch (e) { toast.error(errorText(e)) }
  loadLogs(true)
}
async function loadLogs(reset = false) {
  const ws = session.current?.id
  if (!ws) return
  logsLoading.value = true
  try {
    const q = new URLSearchParams({ limit: '30' })
    if (!reset && logCursor.value) q.set('cursor', logCursor.value)
    const r = await api.get(`/workspaces/${ws}/notify/logs?${q}`)
    logs.value = reset ? r.logs || [] : [...logs.value, ...(r.logs || [])]
    logCursor.value = r.next_cursor || ''
  } catch (e) { toast.error(errorText(e)) } finally { logsLoading.value = false }
}
onMounted(load)
watch(() => session.current?.id, load)

function openEditor(c: Channel | null) {
  editing.value = c
  editorOpen.value = true
}
function eventsText(c: Channel) {
  if (!c.events?.length) return t('notify.eventsAll')
  return c.events.map((id) => {
    const e = store.events.find((x) => x.id === id)
    return e ? (isZh() ? e.name : e.name_en) : id
  }).join(isZh() ? '、' : ', ')
}

async function toggle(c: Channel, on: boolean) {
  const ws = session.current?.id
  try {
    await api.put(`/workspaces/${ws}/notify/channels/${c.id}`, { name: c.name, kind: c.kind, enabled: on, config: { ...(c.config || {}) } })
    await store.load(true)
  } catch (e) { toast.error(errorText(e)) }
}
async function test(c: Channel) {
  testing.value = c.id
  try {
    const r = await api.post(`/workspaces/${session.current?.id}/notify/channels/${c.id}/test`)
    toast.success(t('notify.testOk', { ms: r.latency_ms ?? 0 }))
  } catch (e) { toast.error(t('notify.testFailed', { e: errorText(e) })) } finally {
    testing.value = ''
    loadLogs(true)
  }
}
async function remove(c: Channel) {
  if (!(await confirmDialog({ title: t('common.delete'), message: t('notify.deleteConfirm', { name: c.name }), danger: true, okText: t('common.delete') }))) return
  try {
    await api.del(`/workspaces/${session.current?.id}/notify/channels/${c.id}`)
    await store.load(true)
  } catch (e) { toast.error(errorText(e)) }
}
const statusClass: Record<string, string> = { ok: 'success', failed: 'danger', sending: 'accent' }
</script>

<template>
  <div>
    <div class="lm-section">
      <div class="h">
        <p class="desc">{{ $t('notify.channelsIntro') }}</p>
        <Btn v-if="canEdit" size="sm" variant="primary" icon="plus" class="nb" @click="openEditor(null)">{{ $t('notify.new') }}</Btn>
      </div>
      <div v-if="!store.list.length" class="lm-card lm-empty">{{ $t('notify.empty') }}</div>
      <div v-else class="lm-card list">
        <div v-for="c in store.list" :key="c.id" class="row">
          <Icon :name="c.kind === 'email' ? 'mail' : c.kind === 'webhook' ? 'link' : 'bell'" :size="16" class="ic" />
          <div class="main">
            <div class="name lm-ellipsis" :class="{ off: !c.enabled }">{{ c.name }}</div>
            <div class="sub lm-ellipsis">{{ kindName(store.kind(c.kind)) }}<template v-if="c.url_host"> · {{ c.url_host }}</template> · {{ eventsText(c) }}</div>
          </div>
          <Switch v-if="canEdit" :model-value="c.enabled" @update:model-value="(v: boolean) => toggle(c, v)" />
          <template v-if="canEdit">
            <Btn size="sm" variant="ghost" :loading="testing === c.id" @click="test(c)">{{ $t('notify.test') }}</Btn>
            <IconBtn icon="edit" :size="15" :label="$t('common.edit')" @click="openEditor(c)" />
            <IconBtn icon="trash" :size="15" :label="$t('common.delete')" @click="remove(c)" />
          </template>
        </div>
      </div>
      <p class="note"><Icon name="info" :size="13" />{{ $t('notify.personal') }}</p>
    </div>

    <div class="lm-section">
      <div class="h"><h3>{{ $t('notify.logs') }}</h3><span class="lm-spacer" /><IconBtn icon="refresh" :size="15" :label="$t('common.refresh')" @click="loadLogs(true)" /></div>
      <div v-if="!logs.length" class="lm-card lm-empty">{{ $t('notify.logsEmpty') }}</div>
      <div v-else class="lm-card logs">
        <div v-for="l in logs" :key="l.id" class="log">
          <span class="lm-badge" :class="statusClass[l.status]">{{ $t('notify.logStatus.' + l.status) }}</span>
          <div class="main">
            <div class="lm-ellipsis title">{{ l.title }}</div>
            <div class="sub lm-ellipsis">{{ l.channel_name }} · {{ formatDate(l.created_at, true) }}<template v-if="l.attempts > 1"> · {{ $t('notify.attempts', { n: l.attempts }) }}</template></div>
            <div v-if="l.error" class="err">{{ l.error }}</div>
          </div>
        </div>
        <div v-if="logCursor" class="more"><Btn size="sm" variant="ghost" :loading="logsLoading" @click="loadLogs()">{{ $t('backup.runs.loadMore') }}</Btn></div>
      </div>
    </div>
    <ChannelEditor v-model:open="editorOpen" :channel="editing" />
  </div>
</template>

<style scoped>
.h { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; }
.h h3 { margin: 0; font-size: 14px; font-weight: 600; }
.desc { margin: 0 0 12px; color: var(--text-2); font-size: 13px; flex: 1; line-height: 1.6; }
.h .nb { flex: none; align-self: flex-start; }
.lm-section > .h { align-items: flex-start; gap: 16px; }
.row, .log { display: flex; align-items: center; gap: 10px; padding: 10px 12px; border-bottom: 1px solid var(--border); }
.row:last-child, .log:last-child { border-bottom: 0; }
.ic { color: var(--text-2); flex: none; }
.main { flex: 1; min-width: 0; }
.name { font-size: 13.5px; font-weight: 500; }
.name.off { color: var(--text-3); }
.sub { font-size: 12px; color: var(--text-3); margin-top: 2px; }
.title { font-size: 13px; }
.err { font-size: 12px; color: var(--danger); margin-top: 2px; word-break: break-word; }
.log { align-items: flex-start; }
.log .lm-badge { margin-top: 1px; flex: none; }
.more { padding: 8px; text-align: center; }
.note { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--text-3); margin: 10px 0 0; }
@media (max-width: 760px) {
  .row { flex-wrap: wrap; }
  .row .main { flex-basis: calc(100% - 30px); }
}
</style>
