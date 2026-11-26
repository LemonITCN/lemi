<script setup lang="ts">
// 新建 / 编辑备份计划：连接、方式（显示不可用原因）、范围、压缩、加密（备份密码只写不读）、存储目标、执行时间、
// 保留、还原校验（空间的校验连接）、成功 / 失败通知。修改要空间管理员。
import { computed, ref, watch } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Switch from '@/components/ui/Switch.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import ScheduleFields from './ScheduleFields.vue'
import ChannelPicker from '@/modules/notify/ChannelPicker.vue'
import { cleanSpec, type Spec } from '@/modules/agent-tasks/schedule'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useConns } from '@/stores/conns'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'
import type { Plan, Retention, Target } from './api'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ plan: Plan | null; connectionId?: string; targets: Target[] }>()
const emit = defineEmits<{ saved: [p: Plan]; newTarget: [] }>()
const session = useSession()
const conns = useConns()
const toast = useToast()
const t = i18n.global.t

interface Method { id: string; available: boolean; default?: boolean; reason?: string; warnings?: string[] }

const name = ref('')
const connId = ref('')
const method = ref('')
const methods = ref<Method[]>([])
const methodsLoading = ref(false)
const allDbs = ref(true)
const dbs = ref<string[]>([])
const dbChoices = ref<string[]>([])
const dbsLoading = ref(false)
const compression = ref('zstd')
const level = ref(5)
const encrypt = ref(false)
const pass1 = ref('')
const pass2 = ref('')
const targets = ref<string[]>([])
const spec = ref<Spec>({ kind: 'daily', time: '03:00', minute: 0, weekdays: [1], day: 1, cron: '0 3 * * *', timezone: '' })
const retention = ref<Retention>({ mode: 'count', count: 7, days: 30, hourly: 0, daily: 7, weekly: 4, monthly: 6, yearly: 1 })
const afterBackup = ref(false)
const verifySpec = ref<Spec>({ kind: 'manual', time: '04:00', weekdays: [0], day: 1, minute: 0, cron: '0 4 * * 0', timezone: '' })
const verifyConn = ref('')
const verifyConnOrig = ref('')
const notifyOk = ref<string[]>([])
const notifyFail = ref<string[]>([])
const enabled = ref(true)
const busy = ref(false)

const conn = computed(() => conns.byId[connId.value])
const isMySQL = computed(() => conn.value?.driver === 'mysql')
const choices = computed(() => conns.list.filter((c) => c.driver === 'mysql' || c.driver === 'sqlite'))
const mysqlConns = computed(() => conns.list.filter((c) => c.driver === 'mysql' && !c.read_only))
const canSetVerifyConn = computed(() => session.atLeast('admin'))

watch(open, async (o) => {
  if (!o) return
  const p = props.plan
  name.value = p?.name || ''
  connId.value = p?.connection_id || props.connectionId || ''
  method.value = p?.method || ''
  allDbs.value = !p || !p.databases.length
  dbs.value = [...(p?.databases || [])]
  compression.value = p?.compression || 'zstd'
  level.value = p?.compression_level || 5
  encrypt.value = !!p?.encrypt
  pass1.value = ''
  pass2.value = ''
  targets.value = p ? [...p.targets] : props.targets.length === 1 ? [props.targets[0].id] : []
  spec.value = p ? { minute: 0, weekdays: [1], day: 1, cron: '0 3 * * *', time: '03:00', timezone: '', ...p.schedule } : { kind: 'daily', time: '03:00', minute: 0, weekdays: [1], day: 1, cron: '0 3 * * *', timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || '' }
  retention.value = { count: 7, days: 30, hourly: 0, daily: 7, weekly: 4, monthly: 6, yearly: 1, ...(p?.retention || { mode: 'count' }) }
  if (!p) retention.value.mode = 'count'
  afterBackup.value = !!p?.verify?.after_backup
  verifySpec.value = { kind: 'manual', time: '04:00', weekdays: [0], day: 1, minute: 0, cron: '0 4 * * 0', timezone: '', ...(p?.verify?.schedule || {}) }
  notifyOk.value = [...(p?.notify_success || [])]
  notifyFail.value = [...(p?.notify_failure || [])]
  enabled.value = p ? p.enabled : true
  try {
    await conns.load()
    const s = await api.get(`/workspaces/${session.current?.id}/backups/settings`)
    verifyConn.value = verifyConnOrig.value = s.verify_connection_id || ''
  } catch (e) { toast.error(errorText(e)) }
  if (!connId.value && choices.value.length === 1) connId.value = choices.value[0].id
  loadConnInfo()
}, { immediate: true })

watch(connId, (n, o) => { if (o !== undefined && n !== o && open.value) loadConnInfo(true) })

async function loadConnInfo(changed = false) {
  methods.value = []
  dbChoices.value = []
  if (!connId.value) return
  if (changed) { method.value = ''; dbs.value = []; allDbs.value = true }
  methodsLoading.value = true
  try {
    const r = await api.get(`/workspaces/${session.current?.id}/backups/methods?connection=${connId.value}`)
    methods.value = r.methods || []
    if (!method.value) method.value = (methods.value.find((m) => m.default && m.available) || methods.value.find((m) => m.available))?.id || ''
  } catch (e) { toast.error(errorText(e)) } finally { methodsLoading.value = false }
  if (isMySQL.value) {
    dbsLoading.value = true
    try {
      const list = await conns.loadDatabases(connId.value)
      dbChoices.value = list.filter((d) => !d.system).map((d) => d.name)
    } catch (e) { toast.error(errorText(e)) } finally { dbsLoading.value = false }
  }
}

function toggleDb(d: string) { dbs.value = dbs.value.includes(d) ? dbs.value.filter((x) => x !== d) : [...dbs.value, d] }
function toggleTarget(id: string) { targets.value = targets.value.includes(id) ? targets.value.filter((x) => x !== id) : [...targets.value, id] }
const methodInfo = computed(() => methods.value.find((m) => m.id === method.value))

const passError = computed(() => {
  if (!encrypt.value) return ''
  const need = !props.plan?.passphrase_set || props.plan?.encrypt === false
  if (!pass1.value && !need) return ''
  if ([...pass1.value].length < 12) return t('backup.form.passphraseShort')
  if (pass1.value !== pass2.value) return t('backup.form.passphraseMismatch')
  return ''
})
const valid = computed(() => name.value.trim() && connId.value && method.value && methodInfo.value?.available !== false && targets.value.length
  && (allDbs.value || !isMySQL.value || dbs.value.length) && !passError.value)

async function save() {
  const ws = session.current?.id
  if (!ws) return
  busy.value = true
  try {
    if (isMySQL.value && canSetVerifyConn.value && verifyConn.value !== verifyConnOrig.value) {
      await api.put(`/workspaces/${ws}/backups/settings`, { verify_connection_id: verifyConn.value })
      verifyConnOrig.value = verifyConn.value
    }
    const vs = verifySpec.value.kind === 'manual' ? null : cleanSpec(verifySpec.value)
    const body: Record<string, any> = {
      connection_id: connId.value, name: name.value.trim(), enabled: enabled.value, method: method.value,
      databases: isMySQL.value && !allDbs.value ? dbs.value : [], compression: compression.value,
      compression_level: compression.value === 'none' ? 0 : Number(level.value), encrypt: encrypt.value,
      targets: targets.value, schedule: cleanSpec(spec.value), retention: cleanRetention(retention.value),
      verify: { after_backup: afterBackup.value, schedule: vs }, notify_success: notifyOk.value, notify_failure: notifyFail.value,
    }
    if (encrypt.value && pass1.value) body.passphrase = pass1.value
    const r: Plan = props.plan ? await api.put(`/workspaces/${ws}/backups/plans/${props.plan.id}`, body) : await api.post(`/workspaces/${ws}/backups/plans`, body)
    toast.success(t('common.saved'))
    emit('saved', r)
    open.value = false
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

function cleanRetention(r: Retention): Retention {
  const n = (v: any) => Math.max(0, Number(v) || 0)
  switch (r.mode) {
    case 'count': return { mode: 'count', count: n(r.count) }
    case 'days': return { mode: 'days', days: n(r.days) }
    case 'gfs': return { mode: 'gfs', hourly: n(r.hourly), daily: n(r.daily), weekly: n(r.weekly), monthly: n(r.monthly), yearly: n(r.yearly) }
  }
  return { mode: '' }
}

function onCompression(v: string) {
  compression.value = v
  level.value = v === 'gzip' ? 6 : 5
}
const gfsKeys = ['hourly', 'daily', 'weekly', 'monthly', 'yearly'] as const
</script>

<template>
  <Modal v-model:open="open" :title="plan ? $t('backup.editPlan') : $t('backup.newPlan')" :width="760">
    <div class="pe">
      <div class="lm-form">
        <TextField v-model="name" :label="$t('backup.form.name')" :placeholder="$t('backup.form.namePh')" />
        <div class="lm-field">
          <span class="lm-label">{{ $t('backup.form.connection') }}</span>
          <select v-model="connId" class="lm-select" :disabled="!!plan">
            <option value="" disabled>{{ $t('backup.form.pickConn') }}</option>
            <option v-for="c in choices" :key="c.id" :value="c.id">{{ c.name }}</option>
          </select>
        </div>
      </div>

      <template v-if="conn">
        <section>
          <h4>{{ $t('backup.form.method') }}</h4>
          <div v-if="methodsLoading" class="lm-muted small">{{ $t('common.loading') }}</div>
          <div class="methods">
            <button v-for="m in methods" :key="m.id" type="button" class="method" :class="{ on: method === m.id, off: !m.available }" :disabled="!m.available" @click="method = m.id">
              <span class="mh"><span class="radio" />{{ $t('backup.method.' + m.id) }}<span v-if="!m.available" class="lm-badge">{{ $t('backup.unavailable') }}</span></span>
              <span class="md">{{ m.available ? $t('backup.methodHelp.' + m.id) : m.reason }}</span>
              <span v-for="w in m.warnings || []" :key="w" class="mw"><Icon name="warning" :size="12" />{{ w }}</span>
            </button>
          </div>
        </section>

        <section>
          <h4>{{ $t('backup.form.scope') }}</h4>
          <div v-if="!isMySQL" class="lm-muted small">{{ $t('backup.form.sqliteScope') }}</div>
          <template v-else>
            <div class="seg2">
              <label><input v-model="allDbs" type="radio" :value="true" />{{ $t('backup.form.allDbs') }}</label>
              <label><input v-model="allDbs" type="radio" :value="false" />{{ $t('backup.form.someDbs') }}</label>
            </div>
            <div v-if="!allDbs" class="dbs">
              <div v-if="dbsLoading" class="lm-muted small">{{ $t('backup.form.loadingDbs') }}</div>
              <div v-else-if="!dbChoices.length" class="lm-muted small">{{ $t('backup.form.noDbs') }}</div>
              <label v-for="d in dbChoices" :key="d" class="chk" :class="{ on: dbs.includes(d) }"><input type="checkbox" :checked="dbs.includes(d)" @change="toggleDb(d)" /><Icon name="database" :size="13" />{{ d }}</label>
            </div>
          </template>
        </section>

        <section>
          <h4>{{ $t('backup.form.compression') }}</h4>
          <div class="lm-form">
            <div class="lm-field">
              <select :value="compression" class="lm-select" @change="onCompression(($event.target as HTMLSelectElement).value)">
                <option value="zstd">{{ $t('backup.form.zstd') }}</option>
                <option value="gzip">{{ $t('backup.form.gzip') }}</option>
                <option value="none">{{ $t('backup.form.none') }}</option>
              </select>
            </div>
            <div v-if="compression !== 'none'" class="lm-field">
              <div class="lvl"><span class="lm-label">{{ $t('backup.form.level') }}</span><input v-model.number="level" type="range" :min="1" :max="compression === 'gzip' ? 9 : 19" /><span class="lm-mono">{{ level }}</span></div>
            </div>
          </div>
          <label class="sw"><Switch v-model="encrypt" />{{ $t('backup.form.encrypt') }}<InfoTip :width="320" :text="$t('backup.form.passphraseHelp')" /></label>
          <div v-if="encrypt" class="lm-form mt">
            <TextField v-model="pass1" type="password" autocomplete="new-password" :label="$t('backup.form.passphrase')"
              :placeholder="plan?.passphrase_set && plan?.encrypt ? $t('backup.form.passphraseKept') : ''" />
            <TextField v-model="pass2" type="password" autocomplete="new-password" :label="$t('backup.form.passphraseRepeat')" />
            <div v-if="passError && (pass1 || pass2)" class="err full">{{ passError }}</div>
          </div>
        </section>

        <section>
          <h4>{{ $t('backup.form.targets') }}</h4>
          <div v-if="!props.targets.length" class="lm-muted small">{{ $t('backup.form.noTargets') }}</div>
          <div class="tgs">
            <label v-for="tg in props.targets" :key="tg.id" class="chk" :class="{ on: targets.includes(tg.id) }"><input type="checkbox" :checked="targets.includes(tg.id)" @change="toggleTarget(tg.id)" />{{ tg.name }}</label>
            <button v-if="session.atLeast('admin')" type="button" class="add" @click="emit('newTarget')"><Icon name="plus" :size="13" />{{ $t('backup.form.addTarget') }}</button>
          </div>
        </section>

        <section>
          <h4>{{ $t('backup.form.schedule') }}</h4>
          <ScheduleFields v-model="spec" />
        </section>

        <section>
          <h4>{{ $t('backup.form.retention') }}<InfoTip :width="320" :text="$t('backup.form.retHelp')" /></h4>
          <div class="lm-form">
            <div class="lm-field">
              <select v-model="retention.mode" class="lm-select">
                <option v-for="m in ['', 'count', 'days', 'gfs']" :key="m" :value="m">{{ $t('backup.form.retMode.' + m) }}</option>
              </select>
            </div>
            <div v-if="retention.mode === 'count'" class="lm-field"><input v-model.number="retention.count" type="number" min="1" class="lm-select" :aria-label="$t('backup.form.retCount')" /></div>
            <div v-if="retention.mode === 'days'" class="lm-field"><input v-model.number="retention.days" type="number" min="1" class="lm-select" :aria-label="$t('backup.form.retDays')" /></div>
          </div>
          <div v-if="retention.mode === 'gfs'" class="gfs">
            <label v-for="k in gfsKeys" :key="k"><span>{{ $t('backup.form.gfs.' + k) }}</span><input v-model.number="retention[k]" type="number" min="0" class="lm-select" /></label>
          </div>
        </section>

        <section>
          <h4>{{ $t('backup.form.verify') }}</h4>
          <label class="sw"><Switch v-model="afterBackup" />{{ $t('backup.form.afterBackup') }}</label>
          <div class="vs">
            <span class="lm-label">{{ $t('backup.form.verifySchedule') }}</span>
            <ScheduleFields v-model="verifySpec" :kinds="['manual', 'daily', 'weekly', 'monthly']" :manual-label="$t('backup.verifySched.none')" />
          </div>
          <div v-if="isMySQL" class="lm-field mt narrow">
            <span class="lm-label">{{ $t('backup.form.verifyConn') }}<InfoTip :width="320" :text="$t('backup.form.verifyConnHelp')" /></span>
            <select v-model="verifyConn" class="lm-select" :disabled="!canSetVerifyConn" :title="canSetVerifyConn ? '' : $t('backup.form.verifyConnAdminOnly')">
              <option value="">{{ $t('backup.form.verifyConnNone') }}</option>
              <option v-for="c in mysqlConns" :key="c.id" :value="c.id">{{ c.name }}</option>
            </select>
          </div>
          <p v-else class="lm-muted small mt">{{ $t('backup.form.sqliteVerify') }}</p>
        </section>

        <section>
          <h4>{{ $t('backup.form.notify') }}</h4>
          <div class="lm-form">
            <div class="lm-field"><span class="lm-label">{{ $t('backup.form.notifySuccess') }}</span><ChannelPicker v-model="notifyOk" /></div>
            <div class="lm-field"><span class="lm-label">{{ $t('backup.form.notifyFailure') }}</span><ChannelPicker v-model="notifyFail" /></div>
          </div>
        </section>
      </template>
    </div>
    <template #footer>
      <label class="sw en"><Switch v-model="enabled" />{{ $t('backup.form.enabled') }}</label>
      <span class="lm-spacer" />
      <Btn variant="ghost" @click="open = false">{{ $t('common.cancel') }}</Btn>
      <Btn variant="primary" :loading="busy" :disabled="!valid" @click="save">{{ $t('common.save') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.pe section { margin-top: 22px; }
.pe h4 { font-size: 13.5px; font-weight: 600; margin: 0 0 10px; display: flex; align-items: center; gap: 4px; }
.small { font-size: 12.5px; }
.mt { margin-top: 12px; }
.narrow { max-width: 360px; }
.methods { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 8px; }
.method { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; padding: 10px 12px; border: 1px solid var(--border-strong); border-radius: var(--radius-l); background: var(--bg); text-align: left; }
.method:hover:not(:disabled) { border-color: var(--accent); }
.method.on { border-color: var(--accent); background: var(--accent-soft); }
.method.off { opacity: .75; cursor: not-allowed; }
.mh { display: flex; align-items: center; gap: 8px; font-size: 13px; font-weight: 600; color: var(--text); }
.radio { width: 14px; height: 14px; border-radius: 50%; border: 1.5px solid var(--border-strong); flex: none; }
.method.on .radio { border: 4px solid var(--accent); }
.md { font-size: 12px; color: var(--text-2); line-height: 1.5; }
.mw { display: flex; gap: 4px; align-items: flex-start; font-size: 12px; color: var(--warning); line-height: 1.45; }
.mw :deep(svg) { margin-top: 2px; flex: none; }
.seg2 { display: flex; gap: 16px; font-size: 13px; margin-bottom: 8px; }
.seg2 label { display: inline-flex; align-items: center; gap: 6px; }
.dbs, .tgs { display: flex; flex-wrap: wrap; gap: 6px; }
.chk { display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 10px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); font-size: 12.5px; cursor: pointer; }
.chk input { margin: 0; }
.chk.on { border-color: var(--accent); background: var(--accent-soft); }
.add { display: inline-flex; align-items: center; gap: 4px; height: 30px; padding: 0 10px; border: 1px dashed var(--border-strong); border-radius: var(--radius-m); background: none; font-size: 12.5px; color: var(--text-2); }
.add:hover { color: var(--accent); border-color: var(--accent); }
.lvl { display: flex; align-items: center; gap: 10px; height: 34px; }
.lvl .lm-label { margin: 0; }
.lvl input { flex: 1; }
.sw { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; margin-top: 12px; }
.en { margin-top: 0; }
.err { color: var(--danger); font-size: 12.5px; }
.gfs { display: grid; grid-template-columns: repeat(5, 1fr); gap: 8px; margin-top: 10px; }
.gfs label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: var(--text-2); }
.vs { margin-top: 14px; }
@media (max-width: 760px) { .gfs { grid-template-columns: repeat(3, 1fr); } }
</style>
