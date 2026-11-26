<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import Modal from '@/components/ui/Modal.vue'
import TextField from '@/components/ui/TextField.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'
import { withReauth } from '@/lib/reauth'
import { confirmDialog } from '@/lib/confirm'
import { formatRelative } from '@/lib/time'

const session = useSession()
const toast = useToast()
const t = i18n.global.t
const users = ref<any[]>([])
const creating = ref(false)
const f = reactive({ username: '', display_name: '', email: '', role: 'user', workspace_id: '', workspace_role: 'member' })
const secret = ref<{ title: string; value: string } | null>(null)

async function load() {
  try { users.value = (await api.get('/admin/users')).users } catch (e) { toast.error(errorText(e)) }
}
onMounted(load)

async function create() {
  try {
    const r = await api.post('/admin/users', f)
    creating.value = false
    Object.assign(f, { username: '', display_name: '', email: '', role: 'user' })
    if (r.temp_password) secret.value = { title: t('settings.tempPassword'), value: r.temp_password }
    load()
  } catch (e) { toast.error(errorText(e)) }
}

async function invite() {
  try {
    const r = await api.post('/admin/invites', { role: 'user', workspace_id: session.current?.id || '', workspace_role: 'member' })
    secret.value = { title: t('settings.inviteLink'), value: location.origin + r.path }
  } catch (e) { toast.error(errorText(e)) }
}

function menu(u: any): MenuItem[] {
  const self = u.id === session.user?.id
  return [
    { label: t('settings.resetPassword'), icon: 'key', disabled: self, run: async () => {
      try { const r = await withReauth(() => api.post(`/admin/users/${u.id}/reset-password`)); secret.value = { title: t('settings.tempPassword'), value: r.temp_password } } catch (e) { toast.error(errorText(e)) }
    } },
    { label: t('settings.resetTotp'), icon: 'shield', disabled: self, run: async () => {
      if (!(await confirmDialog({ title: t('settings.resetTotp'), message: t('settings.resetTotpHint') }))) return
      try { await withReauth(() => api.post(`/admin/users/${u.id}/reset-totp`)); toast.success(t('common.done')) } catch (e) { toast.error(errorText(e)) }
    } },
    { label: u.role === 'admin' ? t('settings.makeUser') : t('settings.makeAdmin'), icon: 'users', disabled: self, run: async () => {
      try { await withReauth(() => api.patch(`/admin/users/${u.id}`, { role: u.role === 'admin' ? 'user' : 'admin' })); load() } catch (e) { toast.error(errorText(e)) }
    } },
    { label: u.status === 'active' ? t('settings.disable') : t('settings.enable'), icon: 'lock', disabled: self, run: async () => {
      try { await api.patch(`/admin/users/${u.id}`, { status: u.status === 'active' ? 'disabled' : 'active' }); load() } catch (e) { toast.error(errorText(e)) }
    } },
    { separator: true },
    { label: t('common.delete'), icon: 'trash', danger: true, disabled: self, run: async () => {
      if (!(await confirmDialog({ title: t('settings.deleteUser', { name: u.username }), danger: true }))) return
      try { await withReauth(() => api.del(`/admin/users/${u.id}`)); load() } catch (e) { toast.error(errorText(e)) }
    } },
  ]
}

async function copy(v: string) { await navigator.clipboard.writeText(v); toast.success(t('common.copied')) }
</script>

<template>
  <div class="lm-section">
    <div class="actions">
      <Btn icon="plus" variant="primary" @click="creating = true">{{ $t('settings.newUser') }}</Btn>
      <Btn icon="link" @click="invite">{{ $t('settings.invite') }}</Btn>
    </div>
    <div class="lm-card">
      <table class="lm-table">
        <thead><tr><th>{{ $t('common.user') }}</th><th>{{ $t('common.role') }}</th><th>{{ $t('auth.mfaTitle') }}</th><th>{{ $t('settings.lastLogin') }}</th><th /></tr></thead>
        <tbody>
          <tr v-for="u in users" :key="u.id" :class="{ off: u.status !== 'active' }">
            <td>{{ u.display_name || u.username }} <span class="lm-faint">@{{ u.username }}</span></td>
            <td><span class="lm-badge" :class="{ accent: u.role === 'admin' }">{{ u.role === 'admin' ? $t('settings.instanceAdmin') : $t('settings.instanceUser') }}</span>
              <span v-if="u.status !== 'active'" class="lm-badge danger">{{ $t('common.disabled') }}</span></td>
            <td>{{ u.totp_enabled ? $t('common.enabled') : '—' }}</td>
            <td class="lm-faint">{{ u.last_login_at ? formatRelative(u.last_login_at) + ' · ' + u.last_login_ip : '—' }}</td>
            <td class="r"><Menu :items="menu(u)" align="end"><IconBtn icon="more" :size="16" /></Menu></td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
  <Modal v-model:open="creating" :title="$t('settings.newUser')" :width="480">
    <div class="lm-form">
      <TextField v-model="f.username" :label="$t('auth.username')" autofocus />
      <TextField v-model="f.display_name" :label="$t('auth.displayName')" />
      <TextField v-model="f.email" :label="$t('auth.email')" class="full" />
      <div class="lm-field">
        <span class="lm-label">{{ $t('settings.instanceRole') }}</span>
        <select v-model="f.role" class="lm-select"><option value="user">{{ $t('settings.instanceUser') }}</option><option value="admin">{{ $t('settings.instanceAdmin') }}</option></select>
      </div>
      <div class="lm-field">
        <span class="lm-label">{{ $t('settings.joinWorkspace') }}</span>
        <select v-model="f.workspace_id" class="lm-select"><option value="">{{ $t('common.none') }}</option><option v-for="w in session.workspaces" :key="w.id" :value="w.id">{{ w.name }}</option></select>
      </div>
      <p class="full lm-faint small">{{ $t('settings.tempPasswordHint') }}</p>
    </div>
    <template #footer><Btn @click="creating = false">{{ $t('common.cancel') }}</Btn><Btn variant="primary" :disabled="!f.username" @click="create">{{ $t('common.create') }}</Btn></template>
  </Modal>
  <Modal :open="!!secret" :title="secret?.title" @update:open="(v: boolean) => !v && (secret = null)">
    <p class="lm-muted small">{{ $t('settings.shownOnce') }}</p>
    <code class="secret">{{ secret?.value }}</code>
    <template #footer><Btn icon="copy" variant="primary" @click="copy(secret!.value)">{{ $t('common.copy') }}</Btn></template>
  </Modal>
</template>

<style scoped>
.actions { display: flex; gap: 8px; margin-bottom: 14px; }
.r { text-align: right; }
tr.off td { opacity: .6; }
.small { font-size: 12.5px; margin: 0; }
.secret { display: block; font-family: var(--font-mono); background: var(--bg-soft); padding: 10px; border-radius: 6px; word-break: break-all; user-select: all; margin-top: 8px; }
</style>
