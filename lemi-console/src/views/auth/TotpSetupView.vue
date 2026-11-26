<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import QRCode from 'qrcode'
import AuthFrame from './AuthFrame.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import RecoveryCodes from '@/components/RecoveryCodes.vue'
import { api } from '@/api/client'
import { errorText } from '@/i18n'
import { useSession } from '@/stores/session'

const session = useSession()
const router = useRouter()
const qr = ref('')
const secret = ref('')
const showSecret = ref(false)
const code = ref('')
const busy = ref(false)
const err = ref('')
const codes = ref<string[]>([])

onMounted(async () => {
  try {
    const r = await api.post('/auth/totp/begin')
    secret.value = r.secret
    qr.value = await QRCode.toDataURL(r.otpauth_uri, { margin: 1, width: 200 })
  } catch (e) {
    err.value = errorText(e)
  }
})

async function confirm() {
  busy.value = true
  err.value = ''
  try {
    const r = await api.post('/auth/totp/confirm', { code: code.value.trim() })
    codes.value = r.recovery_codes || []
    const { recovery_codes: _r, ...rest } = r
    session.apply(rest)
  } catch (e) {
    err.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthFrame v-if="!codes.length" :title="$t('auth.totpTitle')" :subtitle="$t('auth.totpRequired')" :width="420">
    <div class="qr"><img v-if="qr" :src="qr" alt="QR" width="200" height="200" /></div>
    <button class="link" type="button" @click="showSecret = !showSecret">{{ $t('auth.totpManual') }}</button>
    <code v-if="showSecret" class="secret">{{ secret }}</code>
    <form class="form" @submit.prevent="confirm">
      <TextField v-model="code" :label="$t('auth.code')" autocomplete="one-time-code" mono autofocus />
      <p v-if="err" class="err">{{ err }}</p>
      <Btn type="submit" variant="primary" block :loading="busy" :disabled="code.trim().length !== 6">{{ $t('auth.totpBind') }}</Btn>
      <Btn variant="ghost" block @click="session.logout().then(() => router.replace({ name: 'login' }))">{{ $t('auth.logout') }}</Btn>
    </form>
  </AuthFrame>
  <AuthFrame v-else :title="$t('auth.recoveryTitle')" :subtitle="$t('auth.recoveryHint')" :width="420">
    <RecoveryCodes :codes="codes" />
    <Btn variant="primary" block class="mt" @click="router.replace('/')">{{ $t('auth.recoverySaved') }}</Btn>
  </AuthFrame>
</template>

<style scoped>
.qr { display: flex; justify-content: center; margin: 4px 0 8px; min-height: 200px; }
.qr img { border-radius: 8px; background: #fff; padding: 6px; }
.link { border: 0; background: none; color: var(--accent); padding: 0; font-size: 13px; display: block; margin: 0 auto 8px; }
.secret { display: block; text-align: center; font-family: var(--font-mono); font-size: 13px; background: var(--bg-soft); border-radius: 6px; padding: 8px; margin-bottom: 12px; word-break: break-all; user-select: all; }
.form { display: flex; flex-direction: column; gap: 12px; margin-top: 8px; }
.err { margin: 0; color: var(--danger); font-size: 13px; }
.mt { margin-top: 16px; }
</style>
