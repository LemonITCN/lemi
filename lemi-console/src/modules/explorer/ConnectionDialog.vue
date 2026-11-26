<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Switch from '@/components/ui/Switch.vue'
import Segmented from '@/components/ui/Segmented.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import SqliteFilePicker from './SqliteFilePicker.vue'
import { useConns, type Conn, type DriverInfo, type FormField } from '@/stores/conns'
import { useSession } from '@/stores/session'
import { api, ApiError } from '@/api/client'
import { errorText, i18n, isZh } from '@/i18n'

const props = defineProps<{ conn: Conn | null }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ saved: [Conn] }>()
const conns = useConns()
const session = useSession()
const t = i18n.global.t

const driverId = ref(props.conn?.driver || '')
const tab = ref('general')
const f = reactive({
  name: props.conn?.name || '',
  color: props.conn?.color || '',
  folder: props.conn?.folder || '',
  env: props.conn?.env || '',
  read_only: props.conn?.read_only || false,
  ai_level: props.conn?.ai_level || 'read',
  ai_allow: (props.conn?.ai_allow || []).join('\n'),
  notes: props.conn?.notes || '',
  params: { ...(props.conn?.params || {}) } as Record<string, any>,
  ssh: { enabled: false, host: '', port: 22, user: '', auth_type: 'password', password: '', private_key: '', passphrase: '', host_key: '', ...(props.conn?.network?.ssh || {}) } as Record<string, any>,
  proxy: { type: '', host: '', port: 1080, username: '', password: '', ...(props.conn?.network?.proxy || {}) } as Record<string, any>,
})
const secretsSet = new Set(props.conn?.secrets_set || [])
const busy = ref(false)
const testing = ref(false)
const err = ref('')
const testOk = ref('')
const hostKey = ref<{ fingerprint: string; key_type?: string; mismatch?: boolean } | null>(null)

onMounted(() => conns.loadDrivers())

const drv = computed<DriverInfo | undefined>(() => conns.driver(driverId.value))

function chooseDriver(d: DriverInfo) {
  driverId.value = d.id
  for (const fl of d.form) if (f.params[fl.key] === undefined && fl.default !== undefined) f.params[fl.key] = fl.default
  if (!f.name) f.name = d.name.split(' ')[0]
}

const tabs = computed(() => {
  const out = [{ value: 'general', label: t('conn.tabGeneral') }]
  if (drv.value?.form.some((x) => x.group === 'tls')) out.push({ value: 'tls', label: 'SSL / TLS' })
  if (drv.value?.network) out.push({ value: 'ssh', label: t('conn.tabSSH') }, { value: 'proxy', label: t('conn.tabProxy') })
  out.push({ value: 'ai', label: 'AI' }, { value: 'advanced', label: t('common.advanced') })
  return out
})

function fieldsOf(group: string) {
  return (drv.value?.form || []).filter((x) => (x.group || 'basic') === group && visible(x))
}
function visible(x: FormField) {
  if (!x.show_if) return true
  return Object.entries(x.show_if).every(([k, vals]) => vals.includes(String(f.params[k] ?? '')))
}
function label(x: FormField) { return !isZh() && x.label_en ? x.label_en : x.label }

const colors = ['', '#d44c47', '#d9730d', '#cb912f', '#448361', '#337ea9', '#9065b0', '#c14c8a']
const aiLevels = computed(() => [
  { value: 'hidden', label: t('conn.ai.hidden'), desc: t('conn.ai.hiddenDesc') },
  { value: 'read', label: t('conn.ai.read'), desc: t('conn.ai.readDesc') },
  { value: 'write_confirm', label: t('conn.ai.writeConfirm'), desc: t('conn.ai.writeConfirmDesc') },
  { value: 'write_auto', label: t('conn.ai.writeAuto'), desc: t('conn.ai.writeAutoDesc') },
  { value: 'full', label: t('conn.ai.full'), desc: t('conn.ai.fullDesc'), disabled: !session.extra.system?.allow_ai_full_access },
])

function payload() {
  const params: Record<string, any> = {}
  for (const x of drv.value?.form || []) {
    let v = f.params[x.key]
    if (x.type === 'number' && v !== '' && v !== undefined && v !== null) v = Number(v)
    if (v !== undefined) params[x.key] = v
  }
  const network: Record<string, any> = {}
  if (drv.value?.network) {
    network.ssh = { ...f.ssh, port: Number(f.ssh.port) || 22 }
    network.proxy = { ...f.proxy, port: Number(f.proxy.port) || 0 }
  }
  return {
    name: f.name, driver: driverId.value, params, network, color: f.color, folder: f.folder, env: f.env, read_only: f.read_only,
    ai_level: f.ai_level, ai_allow: f.ai_allow.split(/[\n,]/).map((s) => s.trim()).filter(Boolean), notes: f.notes,
  }
}

async function test() {
  testing.value = true
  err.value = ''
  testOk.value = ''
  hostKey.value = null
  try {
    const r = await api.post(`/workspaces/${session.current!.id}/connections/test`, { id: props.conn?.id || '', ...payload() })
    testOk.value = t('explorer.testOk', { product: r.server.product, version: r.server.version, ms: r.latency_ms })
  } catch (e) {
    if (e instanceof ApiError && (e.code === 'ssh_host_key_unknown' || e.code === 'ssh_host_key_mismatch')) {
      hostKey.value = { fingerprint: e.extra?.fingerprint, key_type: e.extra?.key_type, mismatch: e.code === 'ssh_host_key_mismatch' }
      tab.value = 'ssh'
    } else {
      err.value = errorText(e)
    }
  } finally {
    testing.value = false
  }
}

function trustHostKey() {
  if (!hostKey.value) return
  f.ssh.host_key = hostKey.value.fingerprint
  hostKey.value = null
  test()
}

async function save() {
  busy.value = true
  err.value = ''
  try {
    const body = payload()
    const r = props.conn ? await api.put(`/connections/${props.conn.id}`, body) : await api.post(`/workspaces/${session.current!.id}/connections`, body)
    conns.invalidate(r.id)
    emit('saved', r)
  } catch (e) {
    err.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal v-model:open="open" :title="conn ? $t('conn.editTitle') : $t('conn.newTitle')" :width="680" :padded="false">
    <div v-if="!driverId" class="pick">
      <p class="lm-muted">{{ $t('conn.chooseType') }}</p>
      <div class="drivers">
        <button v-for="d in conns.drivers" :key="d.id" class="drv" @click="chooseDriver(d)">
          <Icon :name="d.file_based ? 'file' : 'server'" :size="22" />
          <span>{{ d.name }}</span>
        </button>
      </div>
      <p class="soon">{{ $t('conn.moreSoon') }}</p>
    </div>
    <template v-else>
      <div class="tabs"><Segmented v-model="tab" :options="tabs" /></div>
      <div class="body">
        <div v-show="tab === 'general'" class="lm-form">
          <TextField v-model="f.name" :label="$t('common.name')" class="full" autofocus />
          <template v-for="x in fieldsOf('basic')" :key="x.key">
            <div :class="['lm-field', { full: ['textarea', 'sqlite_file', 'bool'].includes(x.type) || x.key === 'host' }]">
              <template v-if="x.type === 'sqlite_file'">
                <span class="lm-label">{{ label(x) }}<InfoTip v-if="x.help" :text="x.help" /></span>
                <SqliteFilePicker v-model="f.params[x.key]" />
              </template>
              <template v-else-if="x.type === 'bool'">
                <label class="switch-row"><Switch v-model="f.params[x.key]" /><span>{{ label(x) }}</span><InfoTip v-if="x.help" :text="x.help" /></label>
              </template>
              <template v-else-if="x.type === 'select'">
                <span class="lm-label">{{ label(x) }}</span>
                <select v-model="f.params[x.key]" class="lm-select"><option v-for="o in x.options" :key="o.value" :value="o.value">{{ o.label }}</option></select>
              </template>
              <template v-else>
                <TextField v-model="f.params[x.key]" :type="x.type === 'password' ? 'password' : x.type === 'number' ? 'number' : 'text'"
                  :placeholder="x.secret && secretsSet.has(x.key) ? $t('conn.secretKept') : x.placeholder" :autocomplete="x.type === 'password' ? 'new-password' : 'off'">
                  <template #label>{{ label(x) }}<InfoTip v-if="x.help" :text="x.help" /></template>
                </TextField>
              </template>
            </div>
          </template>
          <div class="lm-field">
            <span class="lm-label">{{ $t('conn.env') }}<InfoTip :text="$t('conn.envHelp')" /></span>
            <select v-model="f.env" class="lm-select">
              <option value="">{{ $t('common.none') }}</option>
              <option value="prod">{{ $t('explorer.env.prod') }}</option>
              <option value="test">{{ $t('explorer.env.test') }}</option>
              <option value="dev">{{ $t('explorer.env.dev') }}</option>
            </select>
          </div>
          <TextField v-model="f.folder" :label="$t('conn.folder')" />
          <div class="lm-field full">
            <span class="lm-label">{{ $t('conn.color') }}</span>
            <div class="colors">
              <button v-for="c in colors" :key="c" class="sw" :class="{ on: f.color === c }" :style="{ background: c || 'var(--bg-active)' }" @click="f.color = c" />
            </div>
          </div>
        </div>
        <div v-show="tab === 'tls'" class="lm-form">
          <template v-for="x in fieldsOf('tls')" :key="x.key">
            <div :class="['lm-field', { full: x.type === 'textarea' || x.type === 'select' }]">
              <span class="lm-label">{{ label(x) }}</span>
              <select v-if="x.type === 'select'" v-model="f.params[x.key]" class="lm-select"><option v-for="o in x.options" :key="o.value" :value="o.value">{{ o.label }}</option></select>
              <textarea v-else-if="x.type === 'textarea'" v-model="f.params[x.key]" class="lm-textarea lm-mono" rows="4" spellcheck="false"
                :placeholder="x.secret && secretsSet.has(x.key) ? $t('conn.secretKept') : '-----BEGIN ...'" />
              <TextField v-else v-model="f.params[x.key]" />
            </div>
          </template>
        </div>
        <div v-show="tab === 'ssh'" class="lm-form">
          <label class="switch-row full"><Switch v-model="f.ssh.enabled" /><span>{{ $t('conn.sshEnable') }}</span><InfoTip :text="$t('conn.sshHelp')" /></label>
          <template v-if="f.ssh.enabled">
            <TextField v-model="f.ssh.host" :label="$t('conn.sshHost')" />
            <TextField v-model="f.ssh.port" type="number" :label="$t('conn.port')" />
            <TextField v-model="f.ssh.user" :label="$t('auth.username')" />
            <div class="lm-field">
              <span class="lm-label">{{ $t('conn.sshAuth') }}</span>
              <select v-model="f.ssh.auth_type" class="lm-select"><option value="password">{{ $t('auth.password') }}</option><option value="key">{{ $t('conn.sshKey') }}</option></select>
            </div>
            <TextField v-if="f.ssh.auth_type === 'password'" v-model="f.ssh.password" type="password" :label="$t('auth.password')" class="full"
              :placeholder="secretsSet.has('ssh.password') ? $t('conn.secretKept') : ''" autocomplete="new-password" />
            <template v-else>
              <div class="lm-field full">
                <span class="lm-label">{{ $t('conn.sshKey') }}</span>
                <textarea v-model="f.ssh.private_key" class="lm-textarea lm-mono" rows="5" spellcheck="false"
                  :placeholder="secretsSet.has('ssh.private_key') ? $t('conn.secretKept') : '-----BEGIN OPENSSH PRIVATE KEY-----'" />
              </div>
              <TextField v-model="f.ssh.passphrase" type="password" :label="$t('conn.sshPassphrase')" :placeholder="secretsSet.has('ssh.passphrase') ? $t('conn.secretKept') : ''" autocomplete="new-password" />
            </template>
            <div class="lm-field full">
              <span class="lm-label">{{ $t('conn.sshHostKey') }}<InfoTip :text="$t('conn.sshHostKeyHelp')" /></span>
              <div class="hk"><code>{{ f.ssh.host_key || $t('conn.sshHostKeyNone') }}</code><Btn v-if="f.ssh.host_key" size="sm" variant="ghost" @click="f.ssh.host_key = ''">{{ $t('common.clear') }}</Btn></div>
            </div>
            <div v-if="hostKey" class="confirm-key full" :class="{ bad: hostKey.mismatch }">
              <Icon :name="hostKey.mismatch ? 'warning' : 'shield'" :size="18" />
              <div>
                <p>{{ hostKey.mismatch ? $t('conn.hostKeyMismatch') : $t('conn.hostKeyFirst') }}</p>
                <code>{{ hostKey.key_type }} {{ hostKey.fingerprint }}</code>
                <div class="row"><Btn size="sm" :variant="hostKey.mismatch ? 'danger' : 'primary'" @click="trustHostKey">{{ $t('conn.trustHostKey') }}</Btn></div>
              </div>
            </div>
          </template>
        </div>
        <div v-show="tab === 'proxy'" class="lm-form">
          <div class="lm-field full">
            <span class="lm-label">{{ $t('conn.proxyType') }}<InfoTip :text="$t('conn.proxyHelp')" /></span>
            <select v-model="f.proxy.type" class="lm-select"><option value="">{{ $t('conn.proxyNone') }}</option><option value="socks5">SOCKS5</option><option value="http">HTTP CONNECT</option></select>
          </div>
          <template v-if="f.proxy.type">
            <TextField v-model="f.proxy.host" :label="$t('conn.proxyHost')" />
            <TextField v-model="f.proxy.port" type="number" :label="$t('conn.port')" />
            <TextField v-model="f.proxy.username" :label="$t('auth.username') + ' (' + $t('common.optional') + ')'" />
            <TextField v-model="f.proxy.password" type="password" :label="$t('auth.password')" :placeholder="secretsSet.has('proxy.password') ? $t('conn.secretKept') : ''" autocomplete="new-password" />
          </template>
        </div>
        <div v-show="tab === 'ai'" class="ai">
          <p class="lm-muted small">{{ $t('conn.aiIntro') }}</p>
          <label v-for="l in aiLevels" :key="l.value" class="ai-opt" :class="{ on: f.ai_level === l.value, dis: l.disabled }">
            <input v-model="f.ai_level" type="radio" :value="l.value" :disabled="l.disabled" />
            <div><b>{{ l.label }}</b><span>{{ l.desc }}</span></div>
          </label>
          <div class="lm-field">
            <span class="lm-label">{{ $t('conn.aiAllow') }}<InfoTip :text="$t('conn.aiAllowHelp')" /></span>
            <textarea v-model="f.ai_allow" class="lm-textarea lm-mono" rows="3" placeholder="shop.orders&#10;shop.order_*&#10;analytics.*" spellcheck="false" />
          </div>
        </div>
        <div v-show="tab === 'advanced'" class="lm-form">
          <template v-for="x in fieldsOf('advanced')" :key="x.key">
            <div class="lm-field">
              <template v-if="x.type === 'select'">
                <span class="lm-label">{{ label(x) }}</span>
                <select v-model="f.params[x.key]" class="lm-select"><option v-for="o in x.options" :key="o.value" :value="o.value">{{ o.label }}</option></select>
              </template>
              <TextField v-else v-model="f.params[x.key]" :type="x.type === 'number' ? 'number' : 'text'" :placeholder="x.placeholder">
                <template #label>{{ label(x) }}<InfoTip v-if="x.help" :text="x.help" /></template>
              </TextField>
            </div>
          </template>
          <label class="switch-row full"><Switch v-model="f.read_only" /><span>{{ $t('conn.readOnly') }}</span><InfoTip :text="$t('conn.readOnlyHelp')" /></label>
          <div class="lm-field full">
            <span class="lm-label">{{ $t('conn.notes') }}</span>
            <textarea v-model="f.notes" class="lm-textarea" rows="3" />
          </div>
        </div>
      </div>
    </template>
    <template #footer>
      <p v-if="err" class="err">{{ err }}</p>
      <p v-else-if="testOk" class="ok"><Icon name="check" :size="14" />{{ testOk }}</p>
      <span class="lm-spacer" />
      <template v-if="driverId">
        <Btn v-if="!conn" variant="ghost" @click="driverId = ''">{{ $t('common.back') }}</Btn>
        <Btn icon="plug" :loading="testing" @click="test">{{ $t('conn.test') }}</Btn>
        <Btn variant="primary" :loading="busy" :disabled="!f.name" @click="save">{{ $t('common.save') }}</Btn>
      </template>
    </template>
  </Modal>
</template>

<style scoped>
.pick { padding: 8px 20px 24px; }
.drivers { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 10px; margin-top: 12px; }
.drv { display: flex; flex-direction: column; align-items: center; gap: 8px; padding: 18px 10px; border: 1px solid var(--border-strong); border-radius: var(--radius-l); background: var(--bg); }
.drv:hover { border-color: var(--accent); background: var(--accent-soft); }
.soon { color: var(--text-3); font-size: 12px; margin: 14px 0 0; }
.tabs { padding: 4px 20px 0; overflow-x: auto; }
.body { padding: 16px 20px 20px; overflow: auto; max-height: 58vh; }
.switch-row { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.colors { display: flex; gap: 8px; }
.sw { width: 22px; height: 22px; border-radius: 50%; border: 2px solid transparent; }
.sw.on { border-color: var(--text); }
.hk { display: flex; align-items: center; gap: 8px; }
.hk code { font-family: var(--font-mono); font-size: 12px; color: var(--text-2); word-break: break-all; }
.confirm-key { display: flex; gap: 10px; padding: 12px; border-radius: var(--radius-m); background: var(--accent-soft); }
.confirm-key.bad { background: var(--danger-bg); color: var(--danger); }
.confirm-key p { margin: 0 0 6px; font-size: 13px; }
.confirm-key code { font-family: var(--font-mono); font-size: 12px; word-break: break-all; }
.confirm-key .row { margin-top: 8px; }
.ai { display: flex; flex-direction: column; gap: 8px; }
.small { font-size: 12.5px; margin: 0 0 4px; }
.ai-opt { display: flex; gap: 10px; align-items: flex-start; padding: 10px 12px; border: 1px solid var(--border); border-radius: var(--radius-m); cursor: pointer; }
.ai-opt.on { border-color: var(--accent); background: var(--accent-soft); }
.ai-opt.dis { opacity: .5; cursor: not-allowed; }
.ai-opt input { margin-top: 3px; }
.ai-opt div { display: flex; flex-direction: column; gap: 2px; font-size: 13px; }
.ai-opt span { color: var(--text-2); font-size: 12px; }
.err { color: var(--danger); font-size: 12.5px; margin: 0; max-width: 60%; }
.ok { color: var(--success); font-size: 12.5px; margin: 0; display: flex; align-items: center; gap: 4px; }
</style>
