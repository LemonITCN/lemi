<script setup lang="ts">
import { onMounted, ref } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import Switch from '@/components/ui/Switch.vue'
import TextField from '@/components/ui/TextField.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { useSession } from '@/stores/session'
import { errorText, i18n } from '@/i18n'
import { withReauth } from '@/lib/reauth'

const toast = useToast()
const session = useSession()
const t = i18n.global.t
const s = ref<any>(null)
const allow = ref('')
const deny = ref('')
const busy = ref(false)

onMounted(async () => {
  try {
    s.value = await api.get('/admin/settings')
    allow.value = (s.value.conn_allow || []).join('\n')
    deny.value = (s.value.conn_deny || []).join('\n')
  } catch (e) { toast.error(errorText(e)) }
})

async function save() {
  busy.value = true
  try {
    const body = { ...s.value, conn_allow: allow.value.split(/\s+/).filter(Boolean), conn_deny: deny.value.split(/\s+/).filter(Boolean) }
    for (const k of ['session_idle_hours', 'session_max_days', 'audit_retention_days', 'query_timeout_sec', 'agent_timeout_sec', 'max_result_rows', 'agent_max_steps', 'agent_preview_rows']) body[k] = Number(body[k])
    s.value = await withReauth(() => api.put('/admin/settings', body))
    await session.load()
    toast.success(t('common.saved'))
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
const toggles = ['require_member_totp', 'users_can_create_workspace', 'allow_public_share', 'allow_ai_full_access', 'allow_private_egress']
</script>

<template>
  <template v-if="s">
    <div class="lm-section">
      <h3>{{ $t('settings.securityPolicy') }}</h3>
      <div class="toggles">
        <label v-for="k in toggles" :key="k" class="tg"><Switch v-model="s[k]" /><span>{{ $t('settings.sys.' + k) }}</span><InfoTip :text="$t('settings.sysHelp.' + k)" /></label>
      </div>
      <div class="lm-form mt">
        <TextField v-model="s.session_idle_hours" type="number" :label="$t('settings.sys.session_idle_hours')" />
        <TextField v-model="s.session_max_days" type="number" :label="$t('settings.sys.session_max_days')" />
        <TextField v-model="s.audit_retention_days" type="number" :label="$t('settings.sys.audit_retention_days')" />
      </div>
    </div>
    <div class="lm-section">
      <h3>{{ $t('settings.networkPolicy') }}</h3>
      <p class="desc">{{ $t('settings.networkPolicyHint') }}</p>
      <div class="lm-form">
        <div class="lm-field"><span class="lm-label">{{ $t('settings.connAllow') }}</span><textarea v-model="allow" class="lm-textarea lm-mono" rows="4" placeholder="10.0.0.0/8&#10;192.168.0.0/16" /></div>
        <div class="lm-field"><span class="lm-label">{{ $t('settings.connDeny') }}</span><textarea v-model="deny" class="lm-textarea lm-mono" rows="4" placeholder="127.0.0.0/8" /></div>
      </div>
    </div>
    <div class="lm-section">
      <h3>{{ $t('settings.queryLimits') }}</h3>
      <div class="lm-form">
        <TextField v-model="s.query_timeout_sec" type="number" :label="$t('settings.sys.query_timeout_sec')" />
        <TextField v-model="s.max_result_rows" type="number" :label="$t('settings.sys.max_result_rows')" />
        <TextField v-model="s.agent_timeout_sec" type="number" :label="$t('settings.sys.agent_timeout_sec')" />
        <TextField v-model="s.agent_max_steps" type="number" :label="$t('settings.sys.agent_max_steps')" />
        <TextField v-model="s.agent_preview_rows" type="number" :label="$t('settings.sys.agent_preview_rows')" />
      </div>
    </div>
    <Btn variant="primary" :loading="busy" @click="save">{{ $t('common.save') }}</Btn>
  </template>
</template>

<style scoped>
.toggles { display: flex; flex-direction: column; gap: 10px; }
.tg { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.mt { margin-top: 16px; }
</style>
