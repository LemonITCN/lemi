<script setup lang="ts">
import { ref, watch } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import { reauthState, finishReauth } from '@/lib/reauth'
import { api } from '@/api/client'
import { errorText } from '@/i18n'
import { useSession } from '@/stores/session'

const session = useSession()
const password = ref('')
const code = ref('')
const useCode = ref(false)
const busy = ref(false)
const err = ref('')

watch(() => reauthState.open, (v) => {
  if (v) { password.value = ''; code.value = ''; err.value = ''; useCode.value = !!session.user?.totp_enabled }
})

async function submit() {
  busy.value = true
  err.value = ''
  try {
    await api.post('/me/reauth', useCode.value ? { code: code.value } : { password: password.value })
    finishReauth(true)
  } catch (e) {
    err.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :open="reauthState.open" :title="$t('auth.reauthTitle')" :width="380" @update:open="(v: boolean) => !v && finishReauth(false)">
    <form class="form" @submit.prevent="submit">
      <p class="hint">{{ $t('auth.reauthHint') }}</p>
      <TextField v-if="useCode" v-model="code" :label="$t('auth.code')" autocomplete="one-time-code" mono autofocus />
      <TextField v-else v-model="password" type="password" :label="$t('auth.password')" autocomplete="current-password" autofocus />
      <p v-if="err" class="err">{{ err }}</p>
      <div class="row">
        <button v-if="session.user?.totp_enabled" type="button" class="link" @click="useCode = !useCode">{{ useCode ? $t('auth.usePassword') : $t('auth.useCode') }}</button>
        <span class="lm-spacer" />
        <Btn @click="finishReauth(false)">{{ $t('common.cancel') }}</Btn>
        <Btn type="submit" variant="primary" :loading="busy">{{ $t('auth.verify') }}</Btn>
      </div>
    </form>
  </Modal>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 14px; }
.hint { margin: 0; color: var(--text-2); font-size: 13px; }
.err { margin: 0; color: var(--danger); font-size: 13px; }
.row { display: flex; align-items: center; gap: 8px; }
.link { border: 0; background: none; color: var(--accent); padding: 0; font-size: 13px; }
</style>
