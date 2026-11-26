<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Modal from '@/components/ui/Modal.vue'
import RecoveryCodes from '@/components/RecoveryCodes.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'
import { withReauth } from '@/lib/reauth'
import { confirmDialog } from '@/lib/confirm'
import { formatRelative } from '@/lib/time'

const session = useSession()
const router = useRouter()
const toast = useToast()
const t = i18n.global.t
const cur = ref('')
const pw = ref('')
const pw2 = ref('')
const busy = ref(false)
const sessions = ref<any[]>([])
const codes = ref<string[]>([])

async function loadSessions() {
  try { sessions.value = (await api.get('/me/sessions')).sessions } catch (e) { toast.error(errorText(e)) }
}
onMounted(loadSessions)

async function changePassword() {
  if (pw.value !== pw2.value) return toast.error(t('auth.passwordMismatch'))
  busy.value = true
  try {
    await api.post('/me/password', { current: cur.value, new: pw.value })
    cur.value = pw.value = pw2.value = ''
    toast.success(t('settings.passwordChanged'))
    loadSessions()
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    busy.value = false
  }
}

async function regen() {
  try {
    codes.value = (await withReauth(() => api.post('/me/totp/recovery'))).recovery_codes
    await session.load()
  } catch (e) { toast.error(errorText(e)) }
}

async function rebind() {
  if (!(await confirmDialog({ title: t('settings.rebindTitle'), message: t('settings.rebindHint') }))) return
  try {
    await withReauth(() => api.post('/auth/totp/begin'))
    session.stage = 'totp_setup'
    router.push({ name: 'totp-setup' })
  } catch (e) { toast.error(errorText(e)) }
}

async function revoke(key: string) {
  try { await api.del('/me/sessions/' + key); loadSessions() } catch (e) { toast.error(errorText(e)) }
}
async function revokeOthers() {
  try { await api.post('/me/sessions/revoke-others'); loadSessions() } catch (e) { toast.error(errorText(e)) }
}
function device(ua: string) {
  const os = /Windows/.test(ua) ? 'Windows' : /iPhone|iPad/.test(ua) ? 'iOS' : /Mac OS/.test(ua) ? 'macOS' : /Android/.test(ua) ? 'Android' : /Linux/.test(ua) ? 'Linux' : ''
  const br = /Edg\//.test(ua) ? 'Edge' : /Chrome\//.test(ua) ? 'Chrome' : /Firefox\//.test(ua) ? 'Firefox' : /Safari\//.test(ua) ? 'Safari' : ''
  return [br, os].filter(Boolean).join(' · ') || ua.slice(0, 40)
}
</script>

<template>
  <div class="lm-section">
    <h3>{{ $t('settings.changePassword') }}</h3>
    <p class="desc">{{ $t('auth.passwordHint') }}</p>
    <div class="lm-form">
      <TextField v-model="cur" type="password" :label="$t('auth.passwordCurrent')" autocomplete="current-password" class="full" />
      <TextField v-model="pw" type="password" :label="$t('auth.passwordNew')" autocomplete="new-password" />
      <TextField v-model="pw2" type="password" :label="$t('auth.passwordConfirm')" autocomplete="new-password" />
    </div>
    <div class="actions"><Btn variant="primary" :loading="busy" :disabled="!cur || !pw" @click="changePassword">{{ $t('common.save') }}</Btn></div>
  </div>
  <div class="lm-section">
    <h3>{{ $t('auth.mfaTitle') }}</h3>
    <p class="desc">{{ session.user?.totp_enabled ? $t('settings.totpOn', { n: session.user?.recovery_left }) : $t('settings.totpOff') }}</p>
    <div class="row">
      <Btn @click="rebind">{{ session.user?.totp_enabled ? $t('settings.rebind') : $t('auth.totpBind') }}</Btn>
      <Btn v-if="session.user?.totp_enabled" @click="regen">{{ $t('settings.regenRecovery') }}</Btn>
    </div>
  </div>
  <div class="lm-section">
    <h3>{{ $t('settings.sessions') }}</h3>
    <p class="desc">{{ $t('settings.sessionsHint') }}</p>
    <div class="lm-card">
      <table class="lm-table">
        <tbody>
          <tr v-for="s in sessions" :key="s.key">
            <td>{{ device(s.ua) }} <span v-if="s.current" class="lm-badge accent">{{ $t('settings.thisDevice') }}</span></td>
            <td class="lm-mono">{{ s.ip }}</td>
            <td class="lm-faint">{{ formatRelative(s.last_seen_at) }}</td>
            <td class="r"><Btn v-if="!s.current" size="sm" variant="ghost" @click="revoke(s.key)">{{ $t('settings.signOut') }}</Btn></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="actions"><Btn size="sm" @click="revokeOthers">{{ $t('settings.signOutOthers') }}</Btn></div>
  </div>
  <Modal :open="codes.length > 0" :title="$t('auth.recoveryTitle')" @update:open="(v: boolean) => !v && (codes = [])">
    <p class="lm-muted">{{ $t('auth.recoveryHint') }}</p>
    <RecoveryCodes :codes="codes" />
  </Modal>
</template>

<style scoped>
.actions { margin-top: 14px; }
.row { display: flex; gap: 8px; flex-wrap: wrap; }
.r { text-align: right; }
</style>
