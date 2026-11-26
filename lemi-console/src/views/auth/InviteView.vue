<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AuthFrame from './AuthFrame.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { errorText, i18n } from '@/i18n'

const route = useRoute()
const router = useRouter()
const token = String(route.params.token || '')
const valid = ref<boolean | null>(null)
const workspace = ref('')
const f = ref({ username: '', password: '', password2: '', display_name: '', email: '' })
const busy = ref(false)
const err = ref('')

onMounted(async () => {
  try {
    const r = await api.post('/auth/invite/check', { token })
    valid.value = true
    workspace.value = r.workspace || ''
  } catch {
    valid.value = false
  }
})

async function submit() {
  err.value = ''
  if (f.value.password !== f.value.password2) {
    err.value = i18n.global.t('auth.passwordMismatch')
    return
  }
  busy.value = true
  try {
    const { password2: _p, ...body } = f.value
    await api.post('/auth/invite/accept', { token, ...body })
    router.replace({ name: 'login' })
  } catch (e) {
    err.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthFrame :title="$t('auth.inviteTitle')" :subtitle="valid === false ? $t('auth.inviteInvalid') : workspace ? `${$t('auth.inviteHint')} · ${workspace}` : $t('auth.inviteHint')">
    <form v-if="valid" class="form" @submit.prevent="submit">
      <TextField v-model="f.username" :label="$t('auth.username')" autocomplete="username" autofocus />
      <TextField v-model="f.display_name" :label="$t('auth.displayName')" />
      <TextField v-model="f.email" :label="$t('auth.email')" autocomplete="email" />
      <TextField v-model="f.password" type="password" :label="$t('auth.password')" autocomplete="new-password" />
      <TextField v-model="f.password2" type="password" :label="$t('auth.passwordConfirm')" autocomplete="new-password" />
      <p class="hint">{{ $t('auth.passwordHint') }}</p>
      <p v-if="err" class="err">{{ err }}</p>
      <Btn type="submit" variant="primary" block :loading="busy" :disabled="!f.username || !f.password">{{ $t('auth.register') }}</Btn>
    </form>
  </AuthFrame>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 14px; }
.hint { margin: -6px 0 0; color: var(--text-3); font-size: 12px; }
.err { margin: 0; color: var(--danger); font-size: 13px; }
</style>
