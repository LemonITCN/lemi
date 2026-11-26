<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import AuthFrame from './AuthFrame.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { errorText, i18n } from '@/i18n'
import { useSession } from '@/stores/session'

const router = useRouter()
const session = useSession()
const current = ref('')
const pw = ref('')
const pw2 = ref('')
const busy = ref(false)
const err = ref('')

async function submit() {
  err.value = ''
  if (pw.value !== pw2.value) {
    err.value = i18n.global.t('auth.passwordMismatch')
    return
  }
  busy.value = true
  try {
    await api.post('/me/password', { current: current.value, new: pw.value })
    await session.load()
    router.replace('/')
  } catch (e) {
    err.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthFrame :title="$t('auth.changePasswordTitle')" :subtitle="$t('auth.mustChange')">
    <form class="form" @submit.prevent="submit">
      <TextField v-model="current" type="password" :label="$t('auth.passwordCurrent')" autocomplete="current-password" autofocus />
      <TextField v-model="pw" type="password" :label="$t('auth.passwordNew')" autocomplete="new-password" />
      <TextField v-model="pw2" type="password" :label="$t('auth.passwordConfirm')" autocomplete="new-password" />
      <p class="hint">{{ $t('auth.passwordHint') }}</p>
      <p v-if="err" class="err">{{ err }}</p>
      <Btn type="submit" variant="primary" block :loading="busy" :disabled="!current || !pw">{{ $t('common.save') }}</Btn>
    </form>
  </AuthFrame>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 14px; }
.hint { margin: -6px 0 0; color: var(--text-3); font-size: 12px; }
.err { margin: 0; color: var(--danger); font-size: 13px; }
</style>
