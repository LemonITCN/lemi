<script setup lang="ts">
// 还原记录：还原到哪个连接、库名映射、方式、结果；运行中显示进度，可取消。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import Icon from '@/components/icons/Icon.vue'
import ProgressBar from './ProgressBar.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { formatDate } from '@/lib/time'
import { errorText, i18n } from '@/i18n'
import { formatDuration, live, onProgress, statusClass } from './api'

const emit = defineEmits<{ openRun: [id: string] }>()
const session = useSession()
const toast = useToast()
const t = i18n.global.t
const list = ref<any[]>([])
const cursor = ref('')
const loading = ref(false)
const loaded = ref(false)

async function load(reset = true) {
  const ws = session.current?.id
  if (!ws) return
  loading.value = true
  try {
    const q = new URLSearchParams({ limit: '50' })
    if (!reset && cursor.value) q.set('cursor', cursor.value)
    const r = await api.get(`/workspaces/${ws}/backups/restores?${q}`)
    list.value = reset ? r.restores || [] : [...list.value, ...(r.restores || [])]
    cursor.value = r.next_cursor || ''
  } catch (e) { toast.error(errorText(e)) } finally { loading.value = false; loaded.value = true }
}
onMounted(() => load())
watch(() => session.current?.id, () => load())
const off = onProgress((p) => { if (p.kind === 'restore' && p.done) setTimeout(() => load(), 300) })
onBeforeUnmount(off)

function mapText(m: Record<string, string> | null) {
  if (!m) return ''
  return Object.entries(m).map(([a, b]) => (a === b ? a : `${a} → ${b}`)).join(', ')
}
const methodLabel = (m: string) => ({ mysql: t('backup.restore.methodMysql'), lemi: t('backup.restore.methodLemi'), sqlite_file: t('backup.restore.methodFile') } as Record<string, string>)[m] || m
async function cancel(id: string) {
  try { await api.post(`/workspaces/${session.current?.id}/backups/tasks/${id}/cancel`) } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <div>
    <div v-if="loaded && !list.length" class="lm-card lm-empty">{{ $t('backup.restores.empty') }}</div>
    <div v-else class="lm-card">
      <div v-for="r in list" :key="r.id" class="row">
        <span class="lm-badge st" :class="statusClass[r.status]">{{ $t('backup.status.' + r.status) }}</span>
        <div class="main">
          <div class="t1 lm-ellipsis"><Icon name="arrow-right" :size="12" /> {{ r.connection_name }}<span v-if="mapText(r.database_map)" class="lm-faint lm-mono"> · {{ mapText(r.database_map) }}</span></div>
          <div class="t2">{{ formatDate(r.started_at, true) }} · {{ methodLabel(r.method) }}<template v-if="r.finished_at"> · {{ formatDuration(r.finished_at - r.started_at) }}</template></div>
          <ProgressBar v-if="r.status === 'running' && live[r.id]" :p="live[r.id]" compact class="prog" />
          <div v-if="r.error" class="err">{{ r.error }}</div>
          <div v-if="r.result?.previous_file" class="t2">{{ $t('backup.restore.previous', { f: r.result.previous_file }) }}</div>
        </div>
        <Btn v-if="r.status === 'running' && session.atLeast('member')" size="sm" icon="stop" @click="cancel(r.id)">{{ $t('backup.cancel') }}</Btn>
        <Btn size="sm" variant="ghost" @click="emit('openRun', r.run_id)">{{ $t('backup.restores.from') }}</Btn>
      </div>
      <div v-if="cursor" class="more"><Btn size="sm" variant="ghost" :loading="loading" @click="load(false)">{{ $t('backup.runs.loadMore') }}</Btn></div>
    </div>
  </div>
</template>

<style scoped>
.row { display: flex; align-items: flex-start; gap: 12px; padding: 10px 12px; border-bottom: 1px solid var(--border); }
.row:last-child { border-bottom: 0; }
.st { margin-top: 1px; flex: none; }
.main { flex: 1; min-width: 0; }
.t1 { font-size: 13px; font-weight: 500; display: flex; align-items: center; gap: 4px; }
.t2 { font-size: 12px; color: var(--text-3); margin-top: 2px; }
.err { font-size: 12px; color: var(--danger); margin-top: 2px; word-break: break-word; }
.prog { margin-top: 6px; max-width: 420px; }
.more { padding: 8px; text-align: center; }
@media (max-width: 760px) { .row { flex-wrap: wrap; } .main { flex-basis: calc(100% - 80px); } }
</style>
