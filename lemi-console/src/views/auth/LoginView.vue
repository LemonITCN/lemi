<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AuthFrame from './AuthFrame.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { errorText } from '@/i18n'
import { useSession } from '@/stores/session'

const session = useSession()
const router = useRouter()
const route = useRoute()
const username = ref('')
const password = ref('')
const code = ref('')
const busy = ref(false)
const err = ref('')

function next() {
  const n = route.query.next
  router.replace(typeof n === 'string' && n.startsWith('/') && !n.startsWith('//') ? n : '/')
}

async function login() {
  busy.value = true
  err.value = ''
  try {
    session.apply(await api.post('/auth/login', { username: username.value, password: password.value }))
    password.value = ''
    if (session.stage === 'done') next()
    else if (session.stage === 'totp_setup') router.replace({ name: 'totp-setup' })
  } catch (e) {
    err.value = errorText(e)
  } finally {
    busy.value = false
  }
}

async function verify() {
  busy.value = true
  err.value = ''
  try {
    session.apply(await api.post('/auth/mfa', { code: code.value.trim() }))
    next()
  } catch (e) {
    err.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthFrame v-if="session.stage !== 'mfa'" :title="$t('auth.login')">
    <form class="form" @submit.prevent="login">
      <TextField v-model="username" :label="$t('auth.username')" autocomplete="username" autofocus />
      <TextField v-model="password" type="password" :label="$t('auth.password')" autocomplete="current-password" />
      <p v-if="err" class="err">{{ err }}</p>
      <Btn type="submit" variant="primary" block :loading="busy" :disabled="!username || !password">{{ $t('auth.login') }}</Btn>
    </form>
  </AuthFrame>
  <AuthFrame v-else :title="$t('auth.mfaTitle')" :subtitle="$t('auth.mfaHint')">
    <form class="form" @submit.prevent="verify">
      <TextField v-model="code" :label="$t('auth.code')" autocomplete="one-time-code" mono autofocus />
      <p v-if="err" class="err">{{ err }}</p>
      <Btn type="submit" variant="primary" block :loading="busy" :disabled="code.trim().length < 6">{{ $t('auth.verify') }}</Btn>
      <Btn variant="ghost" block @click="session.logout()">{{ $t('common.back') }}</Btn>
    </form>
  </AuthFrame>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 14px; }
.err { margin: 0; color: var(--danger); font-size: 13px; }
</style>
