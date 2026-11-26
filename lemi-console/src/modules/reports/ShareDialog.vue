<script setup lang="ts">
// 公开链接：访问密码（可选）、有效期；生成后可复制，可撤销。重新生成会让旧链接失效。
import { computed, ref, watch } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { copyText } from '@/lib/clipboard'
import { formatDate } from '@/lib/time'
import { errorText, i18n } from '@/i18n'

const props = defineProps<{ report: any }>()
const emit = defineEmits<{ changed: [] }>()
const open = defineModel<boolean>('open', { default: false })
const session = useSession()
const toast = useToast()
const t = i18n.global.t
const password = ref('')
const days = ref(30)
const busy = ref(false)
const link = ref('')
const allowed = computed(() => session.extra?.system?.allow_public_share !== false)

watch(open, (o) => { if (o) { link.value = ''; password.value = '' } })

async function create() {
  busy.value = true
  try {
    const r = await api.post(`/reports/${props.report.id}/share`, { password: password.value, expires_days: Number(days.value) })
    link.value = r.url?.startsWith('http') ? r.url : location.origin + r.path
    password.value = ''
    emit('changed')
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

async function revoke() {
  busy.value = true
  try {
    await api.del(`/reports/${props.report.id}/share`)
    link.value = ''
    emit('changed')
    toast.success(t('reports.revoked'))
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

async function copy() { if (await copyText(link.value)) toast.success(t('common.copied')) }
</script>

<template>
  <Modal v-model:open="open" :title="$t('reports.shareTitle')" :width="500">
    <p v-if="!allowed" class="lm-muted">{{ $t('reports.shareDisabled') }}</p>
    <template v-else>
      <div v-if="report.share" class="cur">
        {{ $t('reports.shareActive') }}
        <span v-if="report.share.has_password" class="lm-badge">{{ $t('reports.hasPassword') }}</span>
        <span class="lm-badge">{{ report.share.expires_at ? $t('reports.expiresAt', { t: formatDate(report.share.expires_at) }) : $t('reports.noExpiry') }}</span>
      </div>
      <div v-if="link" class="link"><span class="lm-mono">{{ link }}</span><IconBtn icon="copy" :size="14" :label="$t('common.copy')" @click="copy" /></div>
      <p v-if="link" class="lm-muted hint">{{ $t('reports.linkOnce') }}</p>
      <div class="lm-form">
        <TextField v-model="password" type="password" autocomplete="new-password" :label="$t('reports.password')" :placeholder="$t('reports.passwordPh')" />
        <div class="lm-field">
          <span class="lm-label">{{ $t('reports.validity') }}</span>
          <select v-model.number="days" class="lm-select">
            <option :value="1">{{ $t('agent.mcp.days', { n: 1 }) }}</option>
            <option :value="7">{{ $t('agent.mcp.days', { n: 7 }) }}</option>
            <option :value="30">{{ $t('agent.mcp.days', { n: 30 }) }}</option>
            <option :value="0">{{ $t('reports.noExpiry') }}</option>
          </select>
        </div>
      </div>
    </template>
    <template #footer>
      <Btn v-if="report.share && allowed" variant="ghost" :disabled="busy" @click="revoke">{{ $t('reports.revoke') }}</Btn>
      <span class="lm-spacer" />
      <Btn @click="open = false">{{ $t('common.close') }}</Btn>
      <Btn v-if="allowed" variant="primary" :loading="busy" @click="create">{{ report.share ? $t('reports.regenerate') : $t('reports.createLink') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.cur { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: 13px; margin-bottom: 12px; }
.link { display: flex; align-items: center; gap: 6px; background: var(--bg-soft); border: 1px solid var(--border); border-radius: var(--radius-m); padding: 8px 10px; word-break: break-all; font-size: 12.5px; }
.hint { font-size: 12px; margin: 6px 0 12px; }
</style>
