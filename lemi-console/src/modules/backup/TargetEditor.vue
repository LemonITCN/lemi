<script setup lang="ts">
// 新建 / 编辑存储目标：类型（本地目录只对实例管理员开放）、按类型渲染的配置、测试连接（SFTP 首次连接确认主机指纹）、
// 百度网盘扫码授权。密钥字段只写不读。
import { computed, ref, watch } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Icon from '@/components/icons/Icon.vue'
import FieldsForm from '@/modules/notify/FieldsForm.vue'
import BaiduAuth from './BaiduAuth.vue'
import { api, ApiError } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText, i18n, isZh } from '@/i18n'
import type { Target, TargetKind } from './api'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ target: Target | null; kinds: TargetKind[] }>()
const emit = defineEmits<{ saved: [t: Target] }>()
const session = useSession()
const toast = useToast()
const t = i18n.global.t

const kind = ref('')
const name = ref('')
const config = ref<Record<string, any>>({})
const clear = ref<string[]>([])
const busy = ref(false)
const testing = ref(false)
const testMsg = ref<{ ok: boolean; text: string } | null>(null)
const hostKey = ref<{ fingerprint: string; key_type?: string; mismatch: boolean } | null>(null)
const saved = ref<Target | null>(null)

watch(open, (o) => {
  if (!o) return
  saved.value = props.target
  kind.value = props.target?.kind || ''
  name.value = props.target?.name || ''
  config.value = { ...(props.target?.config || {}) }
  clear.value = []
  testMsg.value = null
  hostKey.value = null
}, { immediate: true })

const info = computed(() => props.kinds.find((k) => k.id === kind.value))
const kindChoices = computed(() => props.kinds.filter((k) => !k.admin_only || session.isAdmin))
const kname = (k?: TargetKind) => (k ? (isZh() ? k.name : k.name_en || k.name) : '')
const icon = (id: string) => ({ local: 'folder', sftp: 'terminal', webdav: 'globe', ftp: 'upload', smb: 'server', baidu: 'cloud' } as Record<string, string>)[id] || 'cloud-up'

function chooseKind(id: string) {
  kind.value = id
  if (!name.value) name.value = kname(props.kinds.find((k) => k.id === id))
}
function body() { return { name: name.value.trim(), kind: kind.value, config: config.value, clear: clear.value } }

async function test() {
  testing.value = true
  testMsg.value = null
  hostKey.value = null
  try {
    const r = await api.post(`/workspaces/${session.current?.id}/backups/targets/test`, { ...body(), id: saved.value?.id })
    testMsg.value = { ok: true, text: t('backup.targets.testOk', { ms: r.latency_ms ?? 0 }) }
  } catch (e) {
    if (e instanceof ApiError && (e.code === 'sftp_host_key_unknown' || e.code === 'sftp_host_key_mismatch')) {
      hostKey.value = { fingerprint: e.extra?.fingerprint, key_type: e.extra?.key_type, mismatch: e.code === 'sftp_host_key_mismatch' }
    } else {
      testMsg.value = { ok: false, text: errorText(e) }
    }
  } finally { testing.value = false }
}
function acceptKey() {
  if (!hostKey.value) return
  config.value = { ...config.value, host_key_sha256: hostKey.value.fingerprint }
  hostKey.value = null
  test()
}

async function save() {
  busy.value = true
  try {
    const ws = session.current?.id
    const r: Target = saved.value
      ? await api.put(`/workspaces/${ws}/backups/targets/${saved.value.id}`, body())
      : await api.post(`/workspaces/${ws}/backups/targets`, body())
    toast.success(t('common.saved'))
    emit('saved', r)
    if (kind.value === 'baidu' && !saved.value) { saved.value = r; return } // 留在对话框里扫码授权
    open.value = false
  } catch (e) {
    if (e instanceof ApiError && e.code.startsWith('sftp_host_key')) hostKey.value = { fingerprint: e.extra?.fingerprint, key_type: e.extra?.key_type, mismatch: e.code === 'sftp_host_key_mismatch' }
    else toast.error(errorText(e))
  } finally { busy.value = false }
}
const baiduAuthorized = computed(() => (saved.value?.secrets_set || []).includes('access_token'))
</script>

<template>
  <Modal v-model:open="open" :title="target ? $t('backup.targets.edit') : $t('backup.targets.new')" :width="660">
    <div v-if="!kind">
      <p class="lm-muted">{{ $t('backup.targets.chooseKind') }}</p>
      <div class="grid">
        <button v-for="k in kindChoices" :key="k.id" type="button" class="kd" @click="chooseKind(k.id)">
          <Icon :name="icon(k.id)" :size="20" /><span>{{ kname(k) }}</span>
        </button>
      </div>
      <p v-if="!session.isAdmin" class="note"><Icon name="info" :size="13" />{{ $t('backup.targets.adminOnly') }}</p>
    </div>
    <template v-else>
      <div class="head"><span class="lm-badge"><Icon :name="icon(kind)" :size="12" />&nbsp;{{ kname(info) }}</span></div>
      <div class="lm-form block"><TextField v-model="name" class="full" :label="$t('common.name')" /></div>
      <FieldsForm v-if="info" v-model:config="config" v-model:clear="clear" :fields="info.fields" :secrets-set="saved?.secrets_set"
        :kept-text="$t('backup.targets.secretKept')" :clear-text="$t('backup.targets.clearSecret')" />
      <div v-if="hostKey" class="hk" :class="{ bad: hostKey.mismatch }">
        <div class="hkh"><Icon :name="hostKey.mismatch ? 'warning' : 'shield'" :size="15" />{{ $t('backup.targets.hostKeyTitle') }}</div>
        <p>{{ hostKey.mismatch ? $t('backup.targets.hostKeyMismatch') : $t('backup.targets.hostKeyUnknown') }}</p>
        <div class="fp lm-mono">{{ hostKey.fingerprint }}<span v-if="hostKey.key_type" class="lm-faint"> · {{ hostKey.key_type }}</span></div>
        <Btn size="sm" :variant="hostKey.mismatch ? 'danger' : 'primary'" @click="acceptKey">{{ $t('backup.targets.hostKeyAccept') }}</Btn>
      </div>
      <div v-if="testMsg" class="msg" :class="{ ok: testMsg.ok }"><Icon :name="testMsg.ok ? 'success' : 'error'" :size="14" /><span>{{ testMsg.text }}</span></div>
      <BaiduAuth v-if="kind === 'baidu' && saved" :target-id="saved.id" :authorized="baiduAuthorized" @done="toast.success($t('backup.targets.baiduDone'))" />
      <p v-else-if="kind === 'baidu'" class="note"><Icon name="info" :size="13" />{{ $t('backup.targets.saveFirst') }}</p>
    </template>
    <template v-if="kind" #footer>
      <Btn v-if="!target && !saved" variant="ghost" @click="kind = ''">{{ $t('common.back') }}</Btn>
      <span class="lm-spacer" />
      <Btn :loading="testing" icon="plug" @click="test">{{ $t('backup.targets.test') }}</Btn>
      <Btn variant="primary" :loading="busy" :disabled="!name.trim()" @click="save">{{ $t('common.save') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 8px; margin-top: 10px; }
.kd { display: flex; flex-direction: column; align-items: center; gap: 8px; padding: 14px 8px; border: 1px solid var(--border-strong); border-radius: var(--radius-l); background: var(--bg); font-size: 12.5px; color: var(--text); text-align: center; }
.kd:hover { border-color: var(--accent); background: var(--accent-soft); }
.head { margin-bottom: 12px; }
.head .lm-badge { display: inline-flex; align-items: center; }
.block { margin-bottom: 14px; }
.note { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--text-3); margin: 12px 0 0; }
.hk { margin-top: 16px; padding: 12px; border: 1px solid var(--accent); border-radius: var(--radius-m); background: var(--accent-soft); display: flex; flex-direction: column; gap: 8px; align-items: flex-start; }
.hk.bad { border-color: var(--danger); background: var(--danger-bg); }
.hkh { display: flex; align-items: center; gap: 6px; font-weight: 600; font-size: 13px; }
.hk p { margin: 0; font-size: 12.5px; color: var(--text-2); line-height: 1.5; }
.fp { font-size: 13px; word-break: break-all; background: var(--bg); padding: 6px 8px; border-radius: var(--radius-s); align-self: stretch; }
.msg { margin-top: 14px; display: flex; align-items: flex-start; gap: 6px; font-size: 12.5px; color: var(--danger); background: var(--danger-bg); padding: 8px 10px; border-radius: var(--radius-m); word-break: break-word; }
.msg > :first-child { margin-top: 2px; flex: none; }
.msg.ok { color: var(--success); background: rgba(47, 138, 91, .1); }
</style>
