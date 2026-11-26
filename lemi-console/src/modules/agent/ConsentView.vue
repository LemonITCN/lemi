<script setup lang="ts">
// OAuth 授权确认页：外部智能体（Claude Code、Cursor……）请求访问 Lemi。用户选工作空间、每个连接的档位与有效期。
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import AuthFrame from '@/views/auth/AuthFrame.vue'
import Btn from '@/components/ui/Btn.vue'
import Icon from '@/components/icons/Icon.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import GrantLevels, { type Choice } from './settings/GrantLevels.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { errorText } from '@/i18n'

const route = useRoute()
const session = useSession()
const KEYS = ['response_type', 'client_id', 'redirect_uri', 'state', 'scope', 'code_challenge', 'code_challenge_method', 'resource']
const params = Object.fromEntries(KEYS.map((k) => [k, String(route.query[k] || '')]))

const info = ref<any>(null)
const error = ref('')
const busy = ref(false)
const f = reactive({ workspace: '', levels: {} as Record<string, string>, expires: 90 })
const choices = computed<Choice[]>(() => info.value?.workspaces?.find((w: any) => w.id === f.workspace)?.connections || [])
const granted = computed(() => Object.values(f.levels).filter((l) => l !== 'hidden').length)

onMounted(async () => {
  try {
    info.value = await api.post('/api/v1/oauth/authorize-info', params)
    if (!info.value.ok) error.value = info.value.error_description || info.value.error
    const ws = info.value.workspaces || []
    f.workspace = (ws.find((w: any) => w.id === session.current?.id) || ws[0])?.id || ''
  } catch (e) { error.value = errorText(e) }
})

watch(() => f.workspace, () => {
  f.levels = Object.fromEntries(choices.value.map((c) => [c.id, c.max_level === 'hidden' ? 'hidden' : 'read']))
})

async function decide(approve: boolean) {
  busy.value = true
  try {
    const r = await api.post('/api/v1/oauth/consent', { ...params, approve, workspace_id: f.workspace, levels: f.levels, expires_days: Number(f.expires) })
    if (r.redirect) window.location.href = r.redirect
  } catch (e) { error.value = errorText(e); busy.value = false }
}
</script>

<template>
  <AuthFrame :title="$t('agent.consent.title')" :width="520">
    <div v-if="!info && !error" class="lm-empty">{{ $t('common.loading') }}</div>
    <template v-else-if="info?.ok">
      <div class="app">
        <Icon name="plug" :size="20" />
        <div><div class="an">{{ info.client_name }}</div><div class="ah">{{ $t('agent.consent.redirect', { host: info.redirect_host }) }}</div></div>
      </div>
      <p class="sub">{{ $t('agent.consent.desc', { user: session.user?.username }) }}</p>
      <div class="lm-field">
        <span class="lm-label">{{ $t('agent.mcp.workspace') }}</span>
        <select v-model="f.workspace" class="lm-select"><option v-for="w in info.workspaces" :key="w.id" :value="w.id">{{ w.name }}</option></select>
      </div>
      <div class="lm-field mt">
        <span class="lm-label">{{ $t('agent.mcp.levels') }}<InfoTip :text="$t('agent.mcp.levelsHint')" /></span>
        <GrantLevels v-model="f.levels" :choices="choices" />
      </div>
      <div class="lm-field mt">
        <span class="lm-label">{{ $t('agent.consent.validity') }}</span>
        <select v-model.number="f.expires" class="lm-select">
          <option :value="1">{{ $t('agent.mcp.days', { n: 1 }) }}</option>
          <option :value="30">{{ $t('agent.mcp.days', { n: 30 }) }}</option>
          <option :value="90">{{ $t('agent.mcp.days', { n: 90 }) }}</option>
          <option :value="365">{{ $t('agent.mcp.days', { n: 365 }) }}</option>
          <option :value="0">{{ $t('agent.mcp.never') }}</option>
        </select>
      </div>
      <div class="btns">
        <Btn :disabled="busy" @click="decide(false)">{{ $t('agent.consent.deny') }}</Btn>
        <Btn variant="primary" :loading="busy" :disabled="!granted" @click="decide(true)">{{ $t('agent.consent.allow') }}</Btn>
      </div>
    </template>
    <div v-if="error" class="err"><Icon name="error" :size="15" />{{ error }}</div>
  </AuthFrame>
</template>

<style scoped>
.app { display: flex; align-items: center; gap: 12px; padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-m); margin-bottom: 12px; color: var(--accent); }
.an { font-weight: 600; color: var(--text); }
.ah { font-size: 12px; color: var(--text-3); }
.sub { color: var(--text-2); font-size: 13px; line-height: 1.6; margin: 0 0 14px; }
.mt { margin-top: 14px; }
.btns { display: flex; justify-content: flex-end; gap: 8px; margin-top: 20px; }
.err { display: flex; align-items: center; gap: 6px; color: var(--danger); font-size: 13px; margin-top: 12px; }
</style>
