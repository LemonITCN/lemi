<script setup lang="ts">
// 侧栏：工作空间里的报告列表。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { formatRelative } from '@/lib/time'
import { errorText } from '@/i18n'
import { openReport } from './index'

const session = useSession()
const tabs = useTabs()
const toast = useToast()
const list = ref<any[]>([])
const loaded = ref(false)
const filter = ref('')
const creating = ref(false)
const newTitle = ref('')

async function load() {
  if (!session.current) return
  try {
    list.value = (await api.get(`/workspaces/${session.current.id}/reports`)).reports || []
  } catch (e) { toast.error(errorText(e)) } finally { loaded.value = true }
}
onMounted(load)
watch(() => session.current?.id, load)
const onChanged = () => load()
onMounted(() => window.addEventListener('lemi:report-changed', onChanged))
onBeforeUnmount(() => window.removeEventListener('lemi:report-changed', onChanged))

const filtered = computed(() => {
  const q = filter.value.trim().toLowerCase()
  return list.value.filter((r) => !q || r.title.toLowerCase().includes(q))
})
const activeId = computed(() => tabs.active?.type === 'report' ? tabs.active.params.reportId : '')

async function create() {
  try {
    const r = await api.post(`/workspaces/${session.current!.id}/reports`, { title: newTitle.value, body: '' })
    creating.value = false
    newTitle.value = ''
    await load()
    openReport(r.report.id, r.report.title)
  } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <div class="rp">
    <div class="lm-panel-head">
      <span>{{ $t('reports.title') }}</span>
      <span class="lm-spacer" />
      <IconBtn v-if="session.atLeast('member')" icon="plus" :size="15" :label="$t('reports.new')" @click="creating = true" />
      <IconBtn icon="refresh" :size="15" :label="$t('common.refresh')" @click="load" />
    </div>
    <div class="search"><Icon name="search" :size="14" /><input v-model="filter" :placeholder="$t('reports.filter')" /></div>
    <div class="items">
      <button v-for="r in filtered" :key="r.id" class="it" :class="{ on: r.id === activeId }" @click="openReport(r.id, r.title)">
        <Icon name="report" :size="15" />
        <span class="t lm-ellipsis">{{ r.title }}</span>
        <Icon v-if="r.shared" name="link" :size="12" class="sh" />
        <span class="d">{{ formatRelative(r.updated_at) }}</span>
      </button>
      <div v-if="loaded && !filtered.length" class="lm-empty">{{ $t('reports.empty') }}</div>
    </div>
    <Modal v-model:open="creating" :title="$t('reports.new')">
      <TextField v-model="newTitle" :label="$t('reports.titleLabel')" autofocus @keydown.enter="newTitle.trim() && create()" />
      <template #footer><Btn @click="creating = false">{{ $t('common.cancel') }}</Btn><Btn variant="primary" :disabled="!newTitle.trim()" @click="create">{{ $t('common.create') }}</Btn></template>
    </Modal>
  </div>
</template>

<style scoped>
.rp { flex: 1; display: flex; flex-direction: column; min-height: 0; }
.search { display: flex; align-items: center; gap: 6px; margin: 8px; padding: 0 8px; height: 30px; border: 1px solid var(--border); border-radius: var(--radius-m); color: var(--text-3); background: var(--bg); }
.search input { flex: 1; border: 0; outline: none; background: none; font-size: 13px; color: var(--text); }
.items { flex: 1; overflow: auto; padding: 0 6px 8px; }
.it { width: 100%; display: flex; align-items: center; gap: 8px; height: 32px; padding: 0 8px; border: 0; background: none; border-radius: var(--radius-m); text-align: left; font-size: 13px; color: var(--text-2); }
.it:hover { background: var(--bg-hover); color: var(--text); }
.it.on { background: var(--accent-soft); color: var(--accent); }
.t { flex: 1; min-width: 0; color: var(--text); }
.sh { color: var(--text-3); }
.d { font-size: 11.5px; color: var(--text-3); flex: none; }
</style>
