<script setup lang="ts">
// 个人：MCP 接入（Claude Code / Cursor / Codex / Claude Desktop 配置片段）、个人访问令牌、已授权的应用。
import { computed, onMounted, reactive, ref, watch } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Modal from '@/components/ui/Modal.vue'
import TextField from '@/components/ui/TextField.vue'
import Segmented from '@/components/ui/Segmented.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import GrantLevels, { type Choice } from './GrantLevels.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { withReauth } from '@/lib/reauth'
import { copyText } from '@/lib/clipboard'
import { confirmDialog } from '@/lib/confirm'
import { formatDate, formatRelative } from '@/lib/time'
import { errorText, i18n } from '@/i18n'

const session = useSession()
const toast = useToast()
const t = i18n.global.t

const cfg = ref<{ url: string; clients: any[] } | null>(null)
const client = ref('claude-code')
const auth = ref<'oauth' | 'token'>('oauth')
const tokens = ref<any[]>([])
const grants = ref<any[]>([])

async function load() {
  try {
    const [c, tk, g] = await Promise.all([api.get('/me/mcp/config'), api.get('/me/mcp/tokens'), api.get('/me/mcp/grants')])
    cfg.value = c
    tokens.value = tk.tokens || []
    grants.value = g.grants || []
  } catch (e) { toast.error(errorText(e)) }
}
onMounted(load)

const snippet = computed(() => cfg.value?.clients.find((c) => c.id === client.value))
async function copy(s: string) { if (await copyText(s)) toast.success(t('common.copied')) }

function levelsText(levels: Record<string, string>) {
  const n = Object.values(levels || {}).filter((l) => l !== 'hidden').length
  return t('agent.mcp.connsN', { n })
}

// ---------- 新建令牌 ----------
const f = reactive({ open: false, name: '', workspace: '', levels: {} as Record<string, string>, expires: 90 })
const choices = ref<Choice[]>([])
const creating = ref(false)
const created = ref('')

async function loadChoices() {
  if (!f.workspace) return
  try {
    const r = await api.get(`/me/mcp/choices?workspace=${f.workspace}`)
    choices.value = r.connections || []
    f.levels = Object.fromEntries(choices.value.map((c) => [c.id, c.max_level === 'hidden' ? 'hidden' : 'read']))
  } catch (e) { toast.error(errorText(e)) }
}
watch(() => f.workspace, loadChoices)

function openCreate() {
  Object.assign(f, { open: true, name: '', workspace: session.current?.id || '', levels: {}, expires: 90 })
  loadChoices()
}

async function create() {
  creating.value = true
  try {
    const r = await withReauth(() => api.post('/me/mcp/tokens', { name: f.name, workspace_id: f.workspace, levels: f.levels, expires_days: Number(f.expires) }))
    f.open = false
    created.value = r.token
    await load()
  } catch (e) { toast.error(errorText(e)) } finally { creating.value = false }
}

async function revoke(kind: 'tokens' | 'grants', g: any) {
  const ok = await confirmDialog({ title: kind === 'tokens' ? t('agent.mcp.revokeToken') : t('agent.mcp.revokeGrant'), message: t('agent.mcp.revokeConfirm', { name: g.name }), danger: true, okText: t('agent.mcp.revoke') })
  if (!ok) return
  try { await api.del(`/me/mcp/${kind}/${g.id}`); await load() } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <div class="lm-section">
    <h3>{{ $t('agent.mcp.connect') }} <InfoTip :text="$t('agent.mcp.connectHint')" /></h3>
    <p class="desc">{{ $t('agent.mcp.connectDesc') }}</p>
    <div v-if="cfg" class="lm-card cfg">
      <div class="url"><span class="lm-mono">{{ cfg.url }}</span><IconBtn icon="copy" :size="14" :label="$t('common.copy')" @click="copy(cfg.url)" /></div>
      <div class="tabs">
        <Segmented v-model="client" :options="cfg.clients.map((c) => ({ value: c.id, label: c.name }))" />
        <span class="lm-spacer" />
        <Segmented v-model="auth" :options="[{ value: 'oauth', label: $t('agent.mcp.byOAuth') }, { value: 'token', label: $t('agent.mcp.byToken') }]" />
      </div>
      <div v-if="snippet" class="snip">
        <div v-if="snippet.file" class="file">{{ snippet.file }}</div>
        <pre class="lm-mono">{{ snippet[auth] }}</pre>
        <IconBtn class="cp" icon="copy" :size="14" :label="$t('common.copy')" @click="copy(snippet[auth])" />
      </div>
    </div>
  </div>

  <div class="lm-section">
    <h3>{{ $t('agent.mcp.tokens') }}</h3>
    <p class="desc">{{ $t('agent.mcp.tokensDesc') }}</p>
    <div class="lm-card">
      <table v-if="tokens.length" class="lm-table">
        <thead><tr><th>{{ $t('common.name') }}</th><th>{{ $t('agent.mcp.workspace') }}</th><th>{{ $t('agent.mcp.scope') }}</th><th>{{ $t('agent.mcp.expires') }}</th><th>{{ $t('agent.mcp.lastUsed') }}</th><th /></tr></thead>
        <tbody>
          <tr v-for="g in tokens" :key="g.id">
            <td>{{ g.name }}<div class="hint lm-mono">{{ g.token_hint }}</div></td>
            <td>{{ g.workspace_name }}</td>
            <td>{{ levelsText(g.levels) }}</td>
            <td :class="{ bad: g.expired }">{{ g.expires_at ? formatDate(g.expires_at) : $t('agent.mcp.never') }}</td>
            <td>{{ g.last_used_at ? formatRelative(g.last_used_at) : '—' }}</td>
            <td class="act"><Btn size="sm" variant="ghost" @click="revoke('tokens', g)">{{ $t('agent.mcp.revoke') }}</Btn></td>
          </tr>
        </tbody>
      </table>
      <div v-else class="lm-empty">{{ $t('agent.mcp.noTokens') }}</div>
    </div>
    <Btn class="mt" icon="plus" @click="openCreate">{{ $t('agent.mcp.newToken') }}</Btn>
  </div>

  <div class="lm-section">
    <h3>{{ $t('agent.mcp.grants') }}</h3>
    <div class="lm-card">
      <table v-if="grants.length" class="lm-table">
        <thead><tr><th>{{ $t('agent.mcp.app') }}</th><th>{{ $t('agent.mcp.workspace') }}</th><th>{{ $t('agent.mcp.scope') }}</th><th>{{ $t('agent.mcp.authorizedAt') }}</th><th>{{ $t('agent.mcp.lastUsed') }}</th><th /></tr></thead>
        <tbody>
          <tr v-for="g in grants" :key="g.id">
            <td>{{ g.name }}</td>
            <td>{{ g.workspace_name }}</td>
            <td>{{ levelsText(g.levels) }}</td>
            <td>{{ formatDate(g.created_at) }}</td>
            <td>{{ g.last_used_at ? formatRelative(g.last_used_at) : '—' }}</td>
            <td class="act"><Btn size="sm" variant="ghost" @click="revoke('grants', g)">{{ $t('agent.mcp.revoke') }}</Btn></td>
          </tr>
        </tbody>
      </table>
      <div v-else class="lm-empty">{{ $t('agent.mcp.noGrants') }}</div>
    </div>
  </div>

  <Modal v-model:open="f.open" :title="$t('agent.mcp.newToken')" :width="540">
    <div class="lm-form">
      <TextField v-model="f.name" :label="$t('common.name')" :placeholder="$t('agent.mcp.namePh')" autofocus />
      <div class="lm-field">
        <span class="lm-label">{{ $t('agent.mcp.expires') }}</span>
        <select v-model.number="f.expires" class="lm-select">
          <option :value="7">{{ $t('agent.mcp.days', { n: 7 }) }}</option>
          <option :value="30">{{ $t('agent.mcp.days', { n: 30 }) }}</option>
          <option :value="90">{{ $t('agent.mcp.days', { n: 90 }) }}</option>
          <option :value="365">{{ $t('agent.mcp.days', { n: 365 }) }}</option>
          <option :value="0">{{ $t('agent.mcp.never') }}</option>
        </select>
      </div>
      <div class="lm-field full">
        <span class="lm-label">{{ $t('agent.mcp.workspace') }}</span>
        <select v-model="f.workspace" class="lm-select"><option v-for="w in session.workspaces" :key="w.id" :value="w.id">{{ w.name }}</option></select>
      </div>
      <div class="lm-field full">
        <span class="lm-label">{{ $t('agent.mcp.levels') }}<InfoTip :text="$t('agent.mcp.levelsHint')" /></span>
        <GrantLevels v-model="f.levels" :choices="choices" />
      </div>
    </div>
    <template #footer>
      <Btn @click="f.open = false">{{ $t('common.cancel') }}</Btn>
      <Btn variant="primary" :loading="creating" :disabled="!f.name.trim() || !f.workspace" @click="create">{{ $t('common.create') }}</Btn>
    </template>
  </Modal>

  <Modal :open="!!created" :title="$t('agent.mcp.tokenCreated')" :width="520" @update:open="(v: boolean) => { if (!v) created = '' }">
    <p class="lm-muted">{{ $t('agent.mcp.tokenOnce') }}</p>
    <div class="tok"><span class="lm-mono">{{ created }}</span><IconBtn icon="copy" :size="14" :label="$t('common.copy')" @click="copy(created)" /></div>
    <template #footer><Btn variant="primary" @click="created = ''">{{ $t('common.done') }}</Btn></template>
  </Modal>
</template>

<style scoped>
.cfg { padding: 12px; display: flex; flex-direction: column; gap: 10px; }
.url { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.tabs { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.snip { position: relative; background: var(--bg-soft); border: 1px solid var(--border); border-radius: var(--radius-m); padding: 8px 36px 8px 10px; }
.snip pre { margin: 0; white-space: pre-wrap; word-break: break-all; font-size: 12px; }
.snip .cp { position: absolute; top: 6px; right: 6px; }
.file { font-size: 11.5px; color: var(--text-3); margin-bottom: 4px; }
.hint { font-size: 11px; color: var(--text-3); }
.bad { color: var(--danger); }
.act { text-align: right; width: 1%; }
.mt { margin-top: 10px; }
.tok { display: flex; align-items: center; gap: 6px; background: var(--bg-soft); border: 1px solid var(--border); border-radius: var(--radius-m); padding: 8px 10px; word-break: break-all; font-size: 12.5px; }
</style>
