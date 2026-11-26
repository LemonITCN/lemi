<script setup lang="ts">
// 公开报告页（不需要登录）：只读 JSON 接口取报告与快照；有访问密码时先输入。
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import Logo from '@/components/brand/Logo.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import RichText from '@/components/rich/RichText.vue'
import { api, ApiError } from '@/api/client'
import { formatDate } from '@/lib/time'
import { errorText } from '@/i18n'

const route = useRoute()
const token = String(route.params.token || '')
const report = ref<any>(null)
const needPassword = ref(false)
const password = ref('')
const error = ref('')
const busy = ref(false)

async function load() {
  busy.value = true
  error.value = ''
  try {
    const headers: Record<string, string> = {}
    if (password.value) headers['X-Lemi-Report-Password'] = password.value
    report.value = (await api.get(`/api/public/reports/${encodeURIComponent(token)}`, { headers })).report
    needPassword.value = false
    document.title = report.value.title
  } catch (e) {
    if (e instanceof ApiError && (e.code === 'password_required' || e.code === 'bad_password')) {
      needPassword.value = true
      if (e.code === 'bad_password') error.value = errorText(e)
    } else {
      error.value = errorText(e)
    }
  } finally { busy.value = false }
}
onMounted(load)
</script>

<template>
  <div class="sr">
    <header><Logo :size="22" /><span class="brand">Lemi</span></header>
    <div v-if="needPassword" class="pw">
      <h2>{{ $t('reports.needPassword') }}</h2>
      <TextField v-model="password" type="password" autofocus :error="error" @keydown.enter="load" />
      <Btn variant="primary" :loading="busy" :disabled="!password" @click="load">{{ $t('reports.open') }}</Btn>
    </div>
    <div v-else-if="error" class="lm-empty err">{{ error }}</div>
    <div v-else-if="!report" class="lm-empty">{{ $t('common.loading') }}</div>
    <article v-else class="doc">
      <h1>{{ report.title }}</h1>
      <div class="meta">{{ $t('reports.updatedAt', { t: formatDate(report.updated_at, true) }) }}</div>
      <RichText :text="report.body" :snapshot="report.snapshot" public-view />
    </article>
  </div>
</template>

<style scoped>
.sr { min-height: 100%; background: var(--bg); overflow: auto; height: 100%; }
header { display: flex; align-items: center; gap: 8px; height: 48px; padding: 0 20px; border-bottom: 1px solid var(--border); }
.brand { font-weight: 650; }
.doc { max-width: 860px; margin: 0 auto; padding: 36px 32px 72px; }
.doc h1 { font-size: 28px; font-weight: 700; margin: 0 0 4px; line-height: 1.3; }
.meta { font-size: 12.5px; color: var(--text-3); margin-bottom: 28px; }
.doc :deep(.lm-md) { font-size: 15px; }
.pw { max-width: 360px; margin: 15vh auto 0; display: flex; flex-direction: column; gap: 12px; padding: 0 16px; }
.pw h2 { font-size: 17px; margin: 0; }
.err { color: var(--danger); }
@media (max-width: 760px) { .doc { padding: 20px 16px 48px; } }
</style>
