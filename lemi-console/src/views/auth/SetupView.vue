<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import AuthFrame from './AuthFrame.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { errorText, i18n } from '@/i18n'
import { useSession } from '@/stores/session'

const session = useSession()
const router = useRouter()
const f = ref({ token: '', username: 'admin', password: '', password2: '', display_name: '', email: '', workspace_name: '' })
const busy = ref(false)
const err = ref('')

async function submit() {
  err.value = ''
  if (f.value.password !== f.value.password2) {
    err.value = i18n.global.t('auth.passwordMismatch')
    return
  }
  busy.value = true
  try {
    const { password2: _p, ...body } = f.value
    session.apply(await api.post('/setup', body))
    router.replace({ name: 'totp-setup' })
  } catch (e) {
    err.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthFrame :title="$t('auth.setupTitle')" :subtitle="$t('auth.setupHint')" :width="440">
    <form class="form" @submit.prevent="submit">
      <TextField v-model="f.token" :label="$t('auth.setupToken')" mono autofocus autocomplete="off" />
      <TextField v-model="f.username" :label="$t('auth.username')" autocomplete="username" />
      <TextField v-model="f.password" type="password" :label="$t('auth.password')" autocomplete="new-password" />
      <TextField v-model="f.password2" type="password" :label="$t('auth.passwordConfirm')" autocomplete="new-password" />
      <p class="hint">{{ $t('auth.passwordHint') }}</p>
      <TextField v-model="f.email" :label="$t('auth.email')" autocomplete="email" />
      <TextField v-model="f.workspace_name" :label="$t('auth.workspaceName')" placeholder="默认空间" />
      <p v-if="err" class="err">{{ err }}</p>
      <Btn type="submit" variant="primary" block :loading="busy" :disabled="!f.token || !f.username || !f.password">{{ $t('auth.createAdmin') }}</Btn>
    </form>
  </AuthFrame>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 14px; }
.hint { margin: -6px 0 0; color: var(--text-3); font-size: 12px; }
.err { margin: 0; color: var(--danger); font-size: 13px; }
</style>
