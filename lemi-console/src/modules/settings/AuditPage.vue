<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { formatDate } from '@/lib/time'

const props = defineProps<{ page: { id: string } }>()
const session = useSession()
const toast = useToast()
const entries = ref<any[]>([])
const q = reactive({ q: '', source: '', action: '' })
const loading = ref(false)
const done = ref(false)
const admin = computed(() => props.page.id === 'audit')

async function load(more = false) {
  loading.value = true
  try {
    const p = new URLSearchParams({ limit: '100' })
    if (q.q) p.set('q', q.q)
    if (q.source) p.set('source', q.source)
    if (q.action) p.set('action', q.action)
    if (more && entries.value.length) p.set('before', String(entries.value[entries.value.length - 1].id))
    const url = admin.value ? `/admin/audit?${p}` : `/workspaces/${session.current!.id}/audit?${p}`
    const r = (await api.get(url)).entries
    entries.value = more ? [...entries.value, ...r] : r
    done.value = r.length < 100
  } catch (e) { toast.error(errorText(e)) } finally { loading.value = false }
}
onMounted(() => load())
let timer: any
watch(q, () => { clearTimeout(timer); timer = setTimeout(() => load(), 300) })

function detail(e: any) {
  try {
    const d = JSON.parse(e.detail)
    return d.sql || (Object.keys(d).length ? JSON.stringify(d) : '')
  } catch { return '' }
}
</script>

<template>
  <div class="filters">
    <TextField v-model="q.q" :placeholder="$t('common.search')" size="sm" />
    <select v-model="q.source" class="lm-select sel">
      <option value="">{{ $t('settings.allSources') }}</option>
      <option v-for="s in ['ui', 'grid', 'agent', 'mcp', 'task', 'system', 'cli']" :key="s" :value="s">{{ $t('settings.sources.' + s) }}</option>
    </select>
    <select v-model="q.action" class="lm-select sel">
      <option value="">{{ $t('settings.allActions') }}</option>
      <option value="sql.">SQL</option><option value="auth.">{{ $t('settings.actAuth') }}</option><option value="connection.">{{ $t('settings.actConn') }}</option>
      <option value="backup.">{{ $t('settings.actBackup') }}</option><option value="user.">{{ $t('settings.actUser') }}</option><option value="workspace.">{{ $t('settings.actWs') }}</option>
    </select>
  </div>
  <div class="lm-card list">
    <div v-for="e in entries" :key="e.id" class="row" :class="{ bad: !e.ok }">
      <div class="top">
        <span class="lm-faint t">{{ formatDate(e.at, true) }}</span>
        <span class="lm-badge">{{ $t('settings.sources.' + e.source) }}</span>
        <b>{{ e.action }}</b>
        <span v-if="e.target" class="lm-ellipsis">{{ e.target }}</span>
        <span class="lm-spacer" />
        <span class="lm-faint">{{ e.username || '—' }}</span>
        <span class="lm-faint lm-mono">{{ e.ip }}</span>
      </div>
      <code v-if="detail(e)" class="d">{{ detail(e) }}</code>
      <div v-if="e.error" class="err">{{ e.error }}</div>
    </div>
    <div v-if="!entries.length && !loading" class="lm-empty">{{ $t('common.empty') }}</div>
  </div>
  <div v-if="!done" class="more"><Btn size="sm" :loading="loading" @click="load(true)">{{ $t('common.more') }}</Btn></div>
</template>

<style scoped>
.filters { display: flex; gap: 8px; margin-bottom: 12px; flex-wrap: wrap; }
.sel { width: auto; height: 30px; }
.list { font-size: 12.5px; }
.row { padding: 8px 12px; border-bottom: 1px solid var(--border); }
.row:last-child { border-bottom: 0; }
.row.bad { background: var(--danger-bg); }
.top { display: flex; align-items: center; gap: 8px; min-width: 0; flex-wrap: wrap; }
.t { font-variant-numeric: tabular-nums; }
.d { display: block; margin-top: 4px; font-family: var(--font-mono); font-size: 12px; color: var(--text-2); white-space: pre-wrap; word-break: break-all; max-height: 120px; overflow: auto; }
.err { color: var(--danger); margin-top: 4px; }
.more { margin-top: 12px; text-align: center; }
</style>
