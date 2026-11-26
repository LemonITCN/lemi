<script setup lang="ts">
// 设置 → 系统邮件服务器（实例管理员）：保存要二次验证；口令只写不读；可发测试邮件。
import { onMounted, ref } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Icon from '@/components/icons/Icon.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { withReauth } from '@/lib/reauth'
import { confirmDialog } from '@/lib/confirm'
import { errorText, i18n } from '@/i18n'

const toast = useToast()
const t = i18n.global.t
const loaded = ref(false)
const configured = ref(false)
const passSet = ref(false)
const f = ref({ host: '', port: '' as string | number, tls: 'ssl', username: '', password: '', from: '' })
const to = ref('')
const busy = ref(false)
const testing = ref(false)

function fill(v: any) {
  configured.value = !!v.configured
  passSet.value = (v.secrets_set || []).includes('password')
  f.value = { host: v.host || '', port: v.port || '', tls: v.tls || 'ssl', username: v.username || '', password: '', from: v.from || '' }
}
onMounted(async () => {
  try { fill(await api.get('/admin/smtp')) } catch (e) { toast.error(errorText(e)) } finally { loaded.value = true }
})

function body() {
  return { ...f.value, port: Number(f.value.port) || 0 }
}
async function save() {
  busy.value = true
  try {
    await withReauth(() => api.put('/admin/smtp', body()))
    fill(await api.get('/admin/smtp'))
    toast.success(t('common.saved'))
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
async function clearAll() {
  if (!(await confirmDialog({ title: t('notify.smtpClear'), message: t('notify.smtpClearConfirm'), danger: true, okText: t('notify.smtpClear') }))) return
  try {
    await withReauth(() => api.put('/admin/smtp', { host: '' }))
    fill(await api.get('/admin/smtp'))
  } catch (e) { toast.error(errorText(e)) }
}
async function test() {
  testing.value = true
  try {
    // 表单有改动时用表单内容测试（口令留空沿用已保存的）
    await api.post('/admin/smtp/test', { to: to.value.trim(), config: f.value.host ? body() : undefined })
    toast.success(t('notify.smtpSent'))
  } catch (e) { toast.error(errorText(e)) } finally { testing.value = false }
}
</script>

<template>
  <div v-if="loaded">
    <div class="lm-section">
      <p class="desc"><span class="lm-badge" :class="configured ? 'success' : ''">{{ configured ? $t('common.enabled') : $t('notify.smtpNotSet') }}</span> {{ $t('notify.smtpIntro') }}</p>
      <div class="lm-form">
        <TextField v-model="f.host" :label="$t('notify.smtpHost')" placeholder="smtp.example.com" />
        <TextField v-model="(f.port as any)" type="number" :label="$t('notify.smtpPort')" placeholder="465" />
        <div class="lm-field">
          <span class="lm-label">{{ $t('notify.smtpTls') }}</span>
          <select v-model="f.tls" class="lm-select">
            <option value="ssl">{{ $t('notify.tls.ssl') }}</option>
            <option value="starttls">{{ $t('notify.tls.starttls') }}</option>
            <option value="none">{{ $t('notify.tls.none') }}</option>
          </select>
        </div>
        <TextField v-model="f.from" :label="$t('notify.smtpFrom')" placeholder="Lemi <noreply@example.com>" />
        <TextField v-model="f.username" :label="$t('notify.smtpUser')" autocomplete="off" />
        <TextField v-model="f.password" type="password" :label="$t('notify.smtpPass')" autocomplete="new-password" :placeholder="passSet ? $t('notify.secretKept') : ''" />
      </div>
      <div class="actions">
        <Btn variant="primary" :loading="busy" :disabled="!f.host.trim()" @click="save">{{ $t('common.save') }}</Btn>
        <Btn v-if="configured" variant="ghost" @click="clearAll">{{ $t('notify.smtpClear') }}</Btn>
      </div>
    </div>
    <div class="lm-section">
      <h3>{{ $t('notify.smtpTest') }}</h3>
      <div class="test">
        <input v-model="to" class="lm-select" type="email" :placeholder="$t('notify.smtpTestToPh')" />
        <Btn icon="send" :loading="testing" :disabled="!configured && !f.host.trim()" @click="test">{{ $t('notify.smtpTest') }}</Btn>
      </div>
      <p class="hint"><Icon name="shield" :size="13" />{{ $t('notify.personal') }}</p>
    </div>
  </div>
</template>

<style scoped>
.lm-section > h3 { display: flex; align-items: center; gap: 8px; }
.desc { margin: 0 0 14px; color: var(--text-2); font-size: 13px; }
.actions { display: flex; gap: 8px; margin-top: 16px; }
.test { display: flex; gap: 8px; align-items: center; max-width: 560px; }
.test input { flex: 1; }
.hint { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--text-3); margin: 10px 0 0; }
@media (max-width: 760px) { .test { flex-wrap: wrap; } .test input { flex-basis: 100%; } }
</style>
