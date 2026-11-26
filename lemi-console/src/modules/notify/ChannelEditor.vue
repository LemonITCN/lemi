<script setup lang="ts">
// 新建 / 编辑通知渠道：类型、名称、按类型渲染的配置、订阅的事件；可以先测试再保存（编辑时沿用已保存的密钥）。
import { computed, ref, watch } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Switch from '@/components/ui/Switch.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import FieldsForm from './FieldsForm.vue'
import { useChannels, kindName, type Channel } from './store'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText, i18n, isZh } from '@/i18n'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ channel: Channel | null }>()
const emit = defineEmits<{ saved: [c: Channel] }>()
const store = useChannels()
const session = useSession()
const toast = useToast()
const t = i18n.global.t

const kind = ref('')
const name = ref('')
const enabled = ref(true)
const config = ref<Record<string, any>>({})
const clear = ref<string[]>([])
const events = ref<string[]>([])
const busy = ref(false)
const testing = ref(false)
const testMsg = ref<{ ok: boolean; text: string } | null>(null)

watch(open, (o) => {
  if (!o) return
  const c = props.channel
  kind.value = c?.kind || ''
  name.value = c?.name || ''
  enabled.value = c ? c.enabled : true
  config.value = { ...(c?.config || {}) }
  clear.value = []
  events.value = [...(c?.events || [])]
  testMsg.value = null
}, { immediate: true })

const info = computed(() => store.kind(kind.value))
const selectable = computed(() => store.events.filter((e) => e.id !== 'test'))
function evName(e: { name: string; name_en: string }) { return isZh() ? e.name : e.name_en }
function toggleEvent(id: string) {
  events.value = events.value.includes(id) ? events.value.filter((x) => x !== id) : [...events.value, id]
}
function chooseKind(id: string) {
  kind.value = id
  if (!name.value) name.value = kindName(store.kind(id))
}

function body() {
  return { name: name.value.trim(), kind: kind.value, enabled: enabled.value, config: config.value, clear: clear.value, events: events.value }
}

async function test() {
  const ws = session.current?.id
  if (!ws) return
  testing.value = true
  testMsg.value = null
  try {
    const r = await api.post(`/workspaces/${ws}/notify/channels/test`, { ...body(), id: props.channel?.id })
    testMsg.value = { ok: true, text: t('notify.testOk', { ms: r.latency_ms ?? 0 }) }
  } catch (e) {
    testMsg.value = { ok: false, text: t('notify.testFailed', { e: errorText(e) }) }
  } finally { testing.value = false }
}

async function save() {
  const ws = session.current?.id
  if (!ws) return
  busy.value = true
  try {
    const r = props.channel
      ? await api.put(`/workspaces/${ws}/notify/channels/${props.channel.id}`, body())
      : await api.post(`/workspaces/${ws}/notify/channels`, body())
    await store.load(true)
    toast.success(t('common.saved'))
    emit('saved', r)
    open.value = false
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
</script>

<template>
  <Modal v-model:open="open" :title="channel ? $t('notify.edit') : $t('notify.new')" :width="640">
    <div v-if="!kind" class="kinds">
      <p class="lm-muted">{{ $t('notify.chooseKind') }}</p>
      <div class="grid">
        <button v-for="k in store.kinds" :key="k.id" type="button" class="kd" @click="chooseKind(k.id)">
          <Icon :name="k.id === 'email' ? 'mail' : k.id === 'webhook' ? 'link' : 'bell'" :size="20" />
          <span>{{ kindName(k) }}</span>
        </button>
      </div>
    </div>
    <template v-else>
      <div class="head">
        <span class="lm-badge">{{ kindName(info) }}</span>
        <InfoTip v-if="info?.help" :width="320" :text="info.help" />
        <span class="lm-spacer" />
        <label class="en"><Switch v-model="enabled" />{{ $t('common.enabled') }}</label>
      </div>
      <div class="lm-form block">
        <TextField v-model="name" class="full" :label="$t('common.name')" />
      </div>
      <FieldsForm v-if="info" v-model:config="config" v-model:clear="clear" :fields="info.fields" :secrets-set="channel?.secrets_set"
        :kept-text="$t('notify.secretKept')" :clear-text="$t('notify.clearSecret')" />
      <div class="events">
        <span class="lm-label">{{ $t('notify.events') }}<InfoTip :width="300" :text="$t('notify.eventsHelp')" /></span>
        <div class="evs">
          <label v-for="e in selectable" :key="e.id" class="ev" :class="{ on: events.includes(e.id) }">
            <input type="checkbox" :checked="events.includes(e.id)" @change="toggleEvent(e.id)" />{{ evName(e) }}
          </label>
        </div>
        <div v-if="!events.length" class="all">{{ $t('notify.eventsAll') }}</div>
      </div>
      <div v-if="testMsg" class="msg" :class="{ ok: testMsg.ok }"><Icon :name="testMsg.ok ? 'success' : 'error'" :size="14" />{{ testMsg.text }}</div>
    </template>
    <template v-if="kind" #footer>
      <Btn v-if="!channel" variant="ghost" @click="kind = ''">{{ $t('common.back') }}</Btn>
      <span class="lm-spacer" />
      <Btn :loading="testing" icon="send" @click="test">{{ $t('notify.test') }}</Btn>
      <Btn variant="primary" :loading="busy" :disabled="!name.trim()" @click="save">{{ $t('common.save') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.kinds .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 8px; margin-top: 10px; }
.kd { display: flex; flex-direction: column; align-items: center; gap: 8px; padding: 16px 8px; border: 1px solid var(--border-strong); border-radius: var(--radius-l); background: var(--bg); font-size: 13px; color: var(--text); }
.kd:hover { border-color: var(--accent); background: var(--accent-soft); }
.head { display: flex; align-items: center; gap: 6px; margin-bottom: 14px; }
.en { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; }
.block { margin-bottom: 14px; }
.events { margin-top: 18px; }
.evs { display: flex; flex-wrap: wrap; gap: 6px; }
.ev { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 10px; border: 1px solid var(--border-strong); border-radius: 14px; font-size: 12.5px; cursor: pointer; }
.ev input { margin: 0; }
.ev.on { border-color: var(--accent); background: var(--accent-soft); color: var(--accent); }
.all { font-size: 12px; color: var(--text-3); margin-top: 6px; }
.msg { margin-top: 14px; display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--danger); background: var(--danger-bg); padding: 8px 10px; border-radius: var(--radius-m); word-break: break-word; }
.msg.ok { color: var(--success); background: rgba(47, 138, 91, .1); }
</style>
