<script setup lang="ts">
// 数据库账号与权限（MySQL）：左侧账号列表，右侧授权语句与操作。所有操作先让服务端生成语句预览（口令打码），确认后执行。
import { computed, reactive, ref, watch } from 'vue'
import type { Tab } from '@/modules/registry'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import Modal from '@/components/ui/Modal.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import Switch from '@/components/ui/Switch.vue'
import TextField from '@/components/ui/TextField.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import SqlView from '@/components/code/SqlView.vue'
import PasswordField from './PasswordField.vue'
import { api } from '@/api/client'
import { useConns } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { hasCap } from '@/lib/dialect'
import { confirmDialog } from '@/lib/confirm'
import { copyText } from '@/lib/cm'
import { errorText, i18n } from '@/i18n'
import { PRIV_GROUPS, allowedAt, scopeOf, type Op } from './privs'

interface DBUser { user: string; host: string; plugin?: string; locked?: boolean }

const props = defineProps<{ tab: Tab; active: boolean }>()
const conns = useConns()
const tabs = useTabs()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t

const conn = computed(() => conns.byId[props.tab.params.connectionId])
const connMissing = computed(() => !conn.value && !conns.loading && conns.list.length > 0)
const supported = computed(() => hasCap(conn.value, 'users'))
const canManage = computed(() => conn.value?.access === 'manage')
const readOnlyConn = computed(() => !!conn.value?.read_only)
const isProd = computed(() => conn.value?.env === 'prod')
const cid = computed(() => String(props.tab.params.connectionId || ''))

// ---------- 账号列表 ----------
const users = ref<DBUser[]>([])
const listLoading = ref(false)
const listError = ref('')
const listLoaded = ref(false)
const q = ref('')
const selectedKey = ref<string>(String(props.tab.params.selected || ''))
const mobileDetail = ref(false)

const keyOf = (u: { user: string; host: string }) => `${u.user}@${u.host}`
const filtered = computed(() => {
  const s = q.value.trim().toLowerCase()
  return s ? users.value.filter((u) => keyOf(u).toLowerCase().includes(s)) : users.value
})
const selected = computed(() => users.value.find((u) => keyOf(u) === selectedKey.value) || null)

async function loadUsers() {
  if (!conn.value) return
  listLoading.value = true
  listError.value = ''
  try {
    const r = await api.get(`/connections/${cid.value}/users`)
    users.value = r.users || []
    listLoaded.value = true
  } catch (e) {
    listError.value = errorText(e)
  } finally {
    listLoading.value = false
  }
}

watch(conn, (c) => {
  if (c && supported.value && canManage.value && !listLoaded.value && !listLoading.value) loadUsers()
}, { immediate: true })

function select(u: DBUser) {
  selectedKey.value = keyOf(u)
  mobileDetail.value = true
}
watch(selectedKey, (k) => tabs.setParams(props.tab.id, { selected: k }))

// ---------- 授权语句 ----------
const grants = ref<string[]>([])
const grantsLoading = ref(false)
const grantsError = ref('')
let grantsSeq = 0

async function loadGrants() {
  const u = selected.value
  grants.value = []
  grantsError.value = ''
  if (!u) return
  const n = ++grantsSeq
  grantsLoading.value = true
  try {
    const qs = new URLSearchParams({ user: u.user, host: u.host })
    const r = await api.get(`/connections/${cid.value}/users?${qs}`)
    if (n === grantsSeq) grants.value = r.grants || []
  } catch (e) {
    if (n === grantsSeq) grantsError.value = errorText(e)
  } finally {
    if (n === grantsSeq) grantsLoading.value = false
  }
}
watch(() => selected.value && keyOf(selected.value), loadGrants, { immediate: true })
const grantsSql = computed(() => grants.value.map((g) => g.trim().replace(/;\s*$/, '') + ';').join('\n'))

// ---------- 操作对话框：表单 → 预览语句 → 执行 → （新建账号时）完成页 ----------
const dbs = ref<string[]>([])
const dbTables = ref<string[]>([])
const dlg = reactive({ open: false, op: '' as Op | '', step: 'form' as 'form' | 'preview' | 'done', statements: [] as string[], error: '', busy: false, fieldErr: '' })
const form = reactive({ user: '', host: '%', password: '', database: '*', table: '', privileges: [] as string[], with_grant: false })
const created = reactive({ account: '', password: '' })

const opTitle = computed(() => (dlg.op ? t('dbusers.op.' + dlg.op) : ''))
const needsForm = (op: Op) => ['create', 'password', 'grant', 'revoke', 'readonly'].includes(op)
const dangerOp = computed(() => dlg.op === 'drop' || dlg.op === 'revoke' || dlg.op === 'lock')

function openOp(op: Op) {
  const u = selected.value
  Object.assign(form, { user: '', host: '%', password: '', database: '*', table: '', privileges: [], with_grant: false })
  if (u && op !== 'create' && op !== 'readonly') Object.assign(form, { user: u.user, host: u.host })
  Object.assign(dlg, { open: true, op, step: needsForm(op) ? 'form' : 'preview', statements: [], error: '', busy: false, fieldErr: '' })
  if (op === 'grant' || op === 'revoke' || op === 'readonly') ensureDbs()
  if (!needsForm(op)) preview()
}

async function ensureDbs() {
  if (dbs.value.length) return
  try { dbs.value = (await conns.loadDatabases(cid.value)).map((d) => d.name) } catch { /* 列表取不到时只能选全部库 */ }
}

watch(() => form.database, async (db) => {
  form.table = ''
  dbTables.value = []
  if (!db || db === '*' || !dlg.open) return
  try { dbTables.value = ((await conns.loadObjects(cid.value, db)).tables || []).map((x) => x.name) } catch { /* 忽略 */ }
})

// 范围变化时去掉该范围不允许的权限
const scope = computed(() => scopeOf(form.database, form.table))
watch(scope, (s) => { form.privileges = form.privileges.filter((p) => allowedAt(p, s)) })

function togglePriv(p: string) {
  form.privileges = form.privileges.includes(p) ? form.privileges.filter((x) => x !== p) : [...form.privileges, p]
}
const allPriv = computed(() => form.privileges.includes('ALL PRIVILEGES'))

function params(): Record<string, any> {
  const base = { user: form.user.trim(), host: form.host.trim() || '%' }
  switch (dlg.op) {
    case 'create': case 'password': return { ...base, password: form.password }
    case 'readonly': return { ...base, password: form.password, database: form.database === '*' ? '' : form.database }
    case 'grant': return { ...base, privileges: allPriv.value ? ['ALL PRIVILEGES'] : form.privileges, database: form.database, table: form.table, with_grant: form.with_grant }
    case 'revoke': return { ...base, privileges: allPriv.value ? ['ALL PRIVILEGES'] : form.privileges, database: form.database, table: form.table }
    default: return base
  }
}

function check(): string {
  if (!form.user.trim()) return t('dbusers.err.user')
  if (dlg.op === 'create' || dlg.op === 'password') { if (form.password.length < 8) return t('dbusers.err.password', { n: 8 }) }
  if (dlg.op === 'readonly' && form.password.length < 12) return t('dbusers.err.password', { n: 12 })
  if ((dlg.op === 'grant' || dlg.op === 'revoke') && !form.privileges.length) return t('dbusers.err.privs')
  return ''
}

async function preview() {
  const op = dlg.op
  if (!op) return
  dlg.fieldErr = needsForm(op) ? check() : ''
  if (dlg.fieldErr) return
  dlg.busy = true
  dlg.error = ''
  try {
    const r = await api.post(`/connections/${cid.value}/users/sql`, { op, params: params(), execute: false })
    dlg.statements = r.statements || []
    dlg.step = 'preview'
  } catch (e) {
    if (needsForm(op)) dlg.fieldErr = errorText(e)
    else { dlg.error = errorText(e); dlg.step = 'preview' }
  } finally {
    dlg.busy = false
  }
}

const canExec = computed(() => canManage.value && !readOnlyConn.value)

async function execute() {
  const op = dlg.op
  const c = conn.value
  if (!op || !c || !canExec.value) return
  if (isProd.value && !(await confirmDialog({ title: t('dbusers.prodTitle'), message: t('dbusers.prodHint', { name: c.name }), danger: true, okText: t('dbusers.execute') }))) return
  dlg.busy = true
  dlg.error = ''
  const p = params()
  try {
    await api.post(`/connections/${cid.value}/users/sql`, { op, params: p, execute: true })
    toast.success(t('dbusers.done.' + op))
    const account = `${p.user}@${p.host}`
    if (op === 'drop') {
      selectedKey.value = ''
      mobileDetail.value = false
    }
    if (op === 'create' || op === 'readonly') {
      selectedKey.value = account
      Object.assign(created, { account: `'${p.user}'@'${p.host}'`, password: form.password })
      dlg.step = 'done'
    } else {
      dlg.open = false
    }
    await loadUsers()
    if (op !== 'drop') loadGrants()
  } catch (e) {
    dlg.error = errorText(e)
  } finally {
    dlg.busy = false
  }
}

watch(() => dlg.open, (o) => { if (!o) { form.password = ''; created.password = '' } })

const sql = computed(() => dlg.statements.map((s) => s.trim().replace(/;\s*$/, '') + ';').join('\n'))

async function copy(text: string) { if (await copyText(text)) toast.success(t('common.copied')) }

const detailMenu = computed<MenuItem[]>(() => {
  const u = selected.value
  if (!u) return []
  const items: MenuItem[] = []
  if (ui.isMobile) items.push({ label: t('dbusers.op.revoke'), icon: 'minus', run: () => openOp('revoke') })
  items.push({ label: t('dbusers.op.password'), icon: 'key', run: () => openOp('password') })
  items.push(u.locked ? { label: t('dbusers.op.unlock'), icon: 'unlock', run: () => openOp('unlock') } : { label: t('dbusers.op.lock'), icon: 'lock', run: () => openOp('lock') })
  items.push({ label: t('dbusers.copyGrants'), icon: 'copy', disabled: !grants.value.length, run: () => copy(grantsSql.value) })
  items.push({ separator: true }, { label: t('dbusers.op.drop'), icon: 'trash', danger: true, run: () => openOp('drop') })
  return items
})

const createMenu = computed<MenuItem[]>(() => [
  { label: t('dbusers.op.create'), icon: 'plus', run: () => openOp('create') },
  { label: t('dbusers.op.readonly'), icon: 'shield', run: () => openOp('readonly') },
])
</script>

<template>
  <div class="dbusers">
    <div class="lm-toolbar">
      <Icon name="users" :size="16" class="ticon" />
      <span class="title lm-ellipsis">{{ conn?.name || '' }}</span>
      <span v-if="isProd" class="lm-badge danger">{{ $t('explorer.env.prod') }}</span>
      <span v-if="!ui.isMobile" class="lm-faint kind">{{ $t('dbusers.title') }}</span>
      <span class="lm-spacer" />
      <template v-if="supported && canManage">
        <template v-if="!ui.isMobile">
          <Btn size="sm" variant="ghost" icon="shield" @click="openOp('readonly')">{{ $t('dbusers.op.readonly') }}</Btn>
          <Btn size="sm" icon="plus" @click="openOp('create')">{{ $t('dbusers.op.create') }}</Btn>
        </template>
        <Menu v-else :items="createMenu" align="end"><IconBtn icon="plus" :size="17" :label="$t('dbusers.op.create')" /></Menu>
        <IconBtn icon="refresh" :size="16" :label="$t('common.refresh')" :disabled="listLoading" @click="loadUsers(); loadGrants()" />
      </template>
    </div>

    <div class="body">
      <div v-if="connMissing" class="state"><Icon name="plug" :size="28" :stroke="1.3" /><p>{{ $t('dbusers.connMissing') }}</p></div>
      <div v-else-if="!conn" class="state"><span class="spin" /><p>{{ $t('common.loading') }}</p></div>
      <div v-else-if="!supported" class="state"><Icon name="users" :size="28" :stroke="1.3" /><p>{{ $t('dbusers.unsupported') }}</p></div>
      <div v-else-if="!canManage" class="state"><Icon name="lock" :size="28" :stroke="1.3" /><p>{{ $t('dbusers.needManage') }}</p></div>
      <div v-else class="split" :class="{ mobile: ui.isMobile, detail: mobileDetail }">
        <!-- 账号列表 -->
        <aside v-show="!ui.isMobile || !mobileDetail" class="list-pane">
          <div class="search"><Icon name="search" :size="14" /><input v-model="q" :placeholder="$t('dbusers.search')" spellcheck="false" /></div>
          <div class="list">
            <p v-if="listError" class="list-err">{{ listError }}</p>
            <p v-else-if="!listLoaded" class="lm-empty">{{ $t('common.loading') }}</p>
            <p v-else-if="!filtered.length" class="lm-empty">{{ users.length ? $t('dbusers.noMatch') : $t('dbusers.noUsers') }}</p>
            <button v-for="u in filtered" :key="keyOf(u)" type="button" class="user-row" :class="{ on: keyOf(u) === selectedKey }" @click="select(u)">
              <span class="name lm-ellipsis"><b>{{ u.user || $t('dbusers.anonymous') }}</b><span class="lm-faint">@{{ u.host }}</span></span>
              <Icon v-if="u.locked" name="lock" :size="13" class="locked" />
              <Icon v-if="ui.isMobile" name="chevron-right" :size="15" class="lm-faint" />
            </button>
          </div>
          <div v-if="listLoaded" class="list-foot lm-faint">{{ $t('dbusers.count', { n: users.length }) }}</div>
        </aside>

        <!-- 账号详情 -->
        <section v-show="!ui.isMobile || mobileDetail" class="detail-pane">
          <div v-if="!selected" class="state"><Icon name="user" :size="28" :stroke="1.3" /><p>{{ $t('dbusers.pick') }}</p></div>
          <template v-else>
            <div class="d-head">
              <IconBtn v-if="ui.isMobile" icon="chevron-left" :size="18" :label="$t('common.back')" @click="mobileDetail = false" />
              <div class="who">
                <div class="acct lm-mono lm-ellipsis" :title="keyOf(selected)">'{{ selected.user }}'@'{{ selected.host }}'</div>
                <div class="badges">
                  <span v-if="selected.locked" class="lm-badge warning">{{ $t('dbusers.locked') }}</span>
                  <span v-if="selected.plugin" class="lm-badge">{{ selected.plugin }}</span>
                </div>
              </div>
              <div class="d-acts">
                <Btn size="sm" icon="plus" @click="openOp('grant')">{{ $t('dbusers.op.grant') }}</Btn>
                <Btn v-if="!ui.isMobile" size="sm" variant="ghost" icon="minus" @click="openOp('revoke')">{{ $t('dbusers.op.revoke') }}</Btn>
                <Menu :items="detailMenu" align="end"><IconBtn icon="more" :size="16" :label="$t('common.more')" /></Menu>
              </div>
            </div>
            <div class="grants">
              <div class="g-head"><span>{{ $t('dbusers.grants') }}</span><IconBtn icon="refresh" :size="14" :label="$t('common.refresh')" :disabled="grantsLoading" @click="loadGrants" /></div>
              <p v-if="grantsError" class="list-err">{{ grantsError }}</p>
              <p v-else-if="grantsLoading && !grants.length" class="lm-empty">{{ $t('common.loading') }}</p>
              <p v-else-if="!grants.length" class="lm-empty">{{ $t('dbusers.noGrants') }}</p>
              <SqlView v-else :code="grantsSql" dialect="mysql" :line-numbers="false" wrap :class="{ dim: grantsLoading }" />
            </div>
          </template>
        </section>
      </div>
    </div>

    <Modal v-model:open="dlg.open" :title="opTitle" :width="dlg.op === 'grant' || dlg.op === 'revoke' ? 620 : 480">
      <!-- 表单 -->
      <div v-if="dlg.step === 'form'" class="form">
        <div class="lm-form">
          <TextField v-model="form.user" :label="$t('dbusers.user')" mono :disabled="dlg.op !== 'create' && dlg.op !== 'readonly'" autofocus />
          <TextField v-model="form.host" :label="$t('dbusers.host')" mono :disabled="dlg.op !== 'create' && dlg.op !== 'readonly'" placeholder="%">
            <template #label><InfoTip :text="$t('dbusers.hostTip')" /></template>
          </TextField>
          <PasswordField v-if="dlg.op === 'create' || dlg.op === 'password' || dlg.op === 'readonly'" v-model="form.password" class="full"
            :label="dlg.op === 'password' ? $t('dbusers.newPassword') : $t('dbusers.password')" :min="dlg.op === 'readonly' ? 12 : 8" />
          <template v-if="dlg.op === 'grant' || dlg.op === 'revoke' || dlg.op === 'readonly'">
            <label class="lm-field">
              <span class="lm-label">{{ $t('dbusers.database') }}</span>
              <select v-model="form.database" class="lm-select">
                <option value="*">{{ $t('dbusers.allDatabases') }}</option>
                <option v-for="d in dbs" :key="d" :value="d">{{ d }}</option>
              </select>
            </label>
            <label v-if="dlg.op !== 'readonly'" class="lm-field">
              <span class="lm-label">{{ $t('dbusers.table') }}</span>
              <select v-model="form.table" class="lm-select" :disabled="form.database === '*'">
                <option value="">{{ $t('dbusers.allTables') }}</option>
                <option v-for="x in dbTables" :key="x" :value="x">{{ x }}</option>
              </select>
            </label>
          </template>
        </div>
        <p v-if="dlg.op === 'readonly'" class="note"><Icon name="info" :size="14" />{{ $t('dbusers.readonlyNote') }}</p>

        <div v-if="dlg.op === 'grant' || dlg.op === 'revoke'" class="privs">
          <label class="priv all"><input type="checkbox" class="ck" :checked="allPriv" @change="togglePriv('ALL PRIVILEGES')" /><span>ALL PRIVILEGES</span></label>
          <div v-for="g in PRIV_GROUPS" :key="g.key" class="pgroup">
            <div class="pg-title">{{ $t('dbusers.group.' + g.key) }}</div>
            <div class="pg-items">
              <label v-for="p in g.privs" :key="p" class="priv" :class="{ off: !allowedAt(p, scope) || allPriv }" :title="allowedAt(p, scope) ? '' : $t('dbusers.notAtScope')">
                <input type="checkbox" class="ck" :checked="form.privileges.includes(p)" :disabled="!allowedAt(p, scope) || allPriv" @change="togglePriv(p)" />
                <span>{{ p }}</span>
              </label>
            </div>
          </div>
          <label v-if="dlg.op === 'grant'" class="sw"><Switch v-model="form.with_grant" /><span>WITH GRANT OPTION</span><InfoTip :text="$t('dbusers.withGrantTip')" /></label>
        </div>
        <p v-if="dlg.fieldErr" class="err" role="alert">{{ dlg.fieldErr }}</p>
      </div>

      <!-- 预览 -->
      <div v-else-if="dlg.step === 'preview'" class="preview">
        <p v-if="dlg.busy && !dlg.statements.length && !dlg.error" class="lm-empty">{{ $t('common.loading') }}</p>
        <p v-if="dlg.op === 'drop' && dlg.statements.length" class="warn-line"><Icon name="warning" :size="14" />{{ $t('dbusers.dropWarn') }}</p>
        <SqlView v-if="dlg.statements.length" :code="sql" dialect="mysql" :line-numbers="false" wrap max-height="40vh" />
        <p v-if="!canExec" class="note"><Icon name="info" :size="14" />{{ readOnlyConn ? $t('dbusers.readOnlyConn') : $t('dbusers.needManage') }}</p>
        <p v-if="dlg.error" class="err" role="alert">{{ dlg.error }}</p>
      </div>

      <!-- 完成（新建账号） -->
      <div v-else class="done">
        <p class="done-line"><Icon name="success" :size="16" class="ok" />{{ $t('dbusers.createdHint') }}</p>
        <div class="cred">
          <span class="lm-label">{{ $t('dbusers.account') }}</span>
          <div class="cred-row"><code class="lm-mono">{{ created.account }}</code><IconBtn icon="copy" :size="15" :label="$t('common.copy')" @click="copy(created.account)" /></div>
          <span class="lm-label">{{ $t('dbusers.password') }}</span>
          <div class="cred-row"><code class="lm-mono">{{ created.password }}</code><IconBtn icon="copy" :size="15" :label="$t('common.copy')" @click="copy(created.password)" /></div>
        </div>
      </div>

      <template #footer>
        <template v-if="dlg.step === 'form'">
          <Btn @click="dlg.open = false">{{ $t('common.cancel') }}</Btn>
          <Btn variant="primary" :loading="dlg.busy" @click="preview">{{ $t('dbusers.previewSql') }}</Btn>
        </template>
        <template v-else-if="dlg.step === 'preview'">
          <Btn v-if="needsForm(dlg.op as Op)" variant="ghost" icon="chevron-left" @click="dlg.step = 'form'; dlg.error = ''">{{ $t('common.back') }}</Btn>
          <span class="lm-spacer" />
          <Btn @click="dlg.open = false">{{ $t('common.cancel') }}</Btn>
          <Btn v-if="canExec && dlg.statements.length" :variant="dangerOp || isProd ? 'danger' : 'primary'" icon="play" :loading="dlg.busy" @click="execute">{{ $t('dbusers.execute') }}</Btn>
        </template>
        <Btn v-else variant="primary" @click="dlg.open = false">{{ $t('common.done') }}</Btn>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
.dbusers { display: flex; flex-direction: column; flex: 1; min-height: 0; height: 100%; min-width: 0; }
.ticon { color: var(--text-2); margin: 0 2px 0 4px; }
.title { font-weight: 500; font-size: 13px; min-width: 0; }
.kind { font-size: 12px; white-space: nowrap; }
.body { flex: 1; min-height: 0; position: relative; }
.split { position: absolute; inset: 0; display: flex; min-width: 0; }
.list-pane { width: 280px; flex: none; border-right: 1px solid var(--border); display: flex; flex-direction: column; min-height: 0; }
.split.mobile .list-pane { width: 100%; border-right: 0; }
.search { display: flex; align-items: center; gap: 6px; height: 30px; margin: 8px; padding: 0 8px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); color: var(--text-3); flex: none; }
.search:focus-within { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.search input { flex: 1; min-width: 0; border: 0; outline: none; background: transparent; font-size: 13px; color: var(--text); }
.list { flex: 1; min-height: 0; overflow: auto; padding: 0 6px 6px; }
.list-err { margin: 8px; font-size: 12.5px; color: var(--danger); word-break: break-word; }
.user-row { display: flex; align-items: center; gap: 6px; width: 100%; height: 32px; padding: 0 8px; border: 0; border-radius: var(--radius-m); background: transparent; text-align: left; color: var(--text); font-size: 13px; }
.split.mobile .user-row { height: 44px; }
.user-row:hover { background: var(--bg-hover); }
.user-row.on { background: var(--bg-active); }
.user-row .name { flex: 1; min-width: 0; }
.user-row b { font-weight: 500; }
.locked { color: var(--warning); flex: none; }
.list-foot { flex: none; font-size: 12px; padding: 6px 14px; border-top: 1px solid var(--border); }
.detail-pane { flex: 1; min-width: 0; display: flex; flex-direction: column; min-height: 0; overflow: auto; position: relative; }
.d-head { display: flex; align-items: center; gap: 10px; padding: 12px 16px; border-bottom: 1px solid var(--border); flex-wrap: wrap; }
.who { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.acct { font-size: 14px; font-weight: 500; }
.badges { display: flex; gap: 6px; flex-wrap: wrap; }
.badges:empty { display: none; }
.d-acts { display: flex; align-items: center; gap: 6px; }
.grants { padding: 12px 16px 24px; display: flex; flex-direction: column; gap: 8px; min-width: 0; }
.g-head { display: flex; align-items: center; justify-content: space-between; font-size: 12.5px; font-weight: 500; color: var(--text-2); }
.dim { opacity: .55; }
.state { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px; color: var(--text-3); padding: 24px; text-align: center; }
.state p { margin: 0; font-size: 13px; max-width: 420px; }
.spin { width: 18px; height: 18px; border-radius: 50%; border: 2px solid var(--border-strong); border-right-color: transparent; animation: spin .7s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

.form { display: flex; flex-direction: column; gap: 14px; }
.note { display: flex; align-items: flex-start; gap: 6px; margin: 0; font-size: 12.5px; color: var(--text-2); line-height: 1.5; }
.note .lm-icon { margin-top: 2px; }
.err { margin: 0; padding: 8px 10px; border-radius: var(--radius-m); background: var(--danger-bg); color: var(--danger); font-size: 13px; word-break: break-word; white-space: pre-wrap; }
.privs { display: flex; flex-direction: column; gap: 10px; border-top: 1px solid var(--border); padding-top: 12px; }
.pgroup { display: flex; flex-direction: column; gap: 4px; }
.pg-title { font-size: 12px; color: var(--text-3); font-weight: 500; }
.pg-items { display: grid; grid-template-columns: repeat(auto-fill, minmax(170px, 1fr)); gap: 2px 12px; }
.priv { display: flex; align-items: center; gap: 8px; font-size: 12.5px; font-family: var(--font-mono); min-height: 26px; cursor: pointer; }
.priv.off { color: var(--text-3); cursor: default; }
.priv.all { font-weight: 600; }
.ck { width: 15px; height: 15px; accent-color: var(--accent); margin: 0; flex: none; }
.sw { display: inline-flex; align-items: center; gap: 8px; font-size: 12.5px; font-family: var(--font-mono); margin-top: 4px; }
.preview { display: flex; flex-direction: column; gap: 10px; }
.warn-line { display: flex; align-items: center; gap: 6px; margin: 0; font-size: 13px; color: var(--danger); }
.done { display: flex; flex-direction: column; gap: 12px; }
.done-line { display: flex; align-items: center; gap: 8px; margin: 0; font-size: 13px; color: var(--text-2); }
.ok { color: var(--success); }
.cred { display: flex; flex-direction: column; gap: 4px; }
.cred .lm-label { margin: 6px 0 0; }
.cred-row { display: flex; align-items: center; gap: 6px; background: var(--bg-soft); border-radius: var(--radius-m); padding: 4px 4px 4px 10px; }
.cred-row code { flex: 1; min-width: 0; word-break: break-all; font-size: 13px; }
@media (max-width: 760px) { .d-head { padding: 10px 10px; } .grants { padding: 10px 10px 24px; } }
</style>
