<script setup lang="ts">
// 存储目标：类型、占用（Lemi 记录的汇总，可实际统计）、测试连接、编辑、删除（被计划使用时不能删）。
import { ref } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Icon from '@/components/icons/Icon.vue'
import { api, ApiError } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { confirmDialog } from '@/lib/confirm'
import { errorText, i18n, isZh } from '@/i18n'
import { formatBytes, type Plan, type Target, type TargetKind } from './api'

const props = defineProps<{ targets: Target[]; kinds: TargetKind[]; plans: Plan[]; loaded: boolean }>()
const emit = defineEmits<{ edit: [t: Target | null]; changed: [] }>()
const session = useSession()
const toast = useToast()
const t = i18n.global.t
const testing = ref('')
const measuring = ref('')
const actual = ref<Record<string, { files: number; bytes: number }>>({})

const kname = (id: string) => {
  const k = props.kinds.find((x) => x.id === id)
  return k ? (isZh() ? k.name : k.name_en || k.name) : id
}
const icon = (id: string) => ({ local: 'folder', sftp: 'terminal', webdav: 'globe', ftp: 'upload', smb: 'server', baidu: 'cloud' } as Record<string, string>)[id] || 'cloud-up'
const usedBy = (id: string) => props.plans.filter((p) => p.targets.includes(id))
const canManage = (tg: Target) => session.atLeast('admin') && (tg.kind !== 'local' || session.isAdmin)

async function test(tg: Target) {
  testing.value = tg.id
  try {
    const r = await api.post(`/workspaces/${session.current?.id}/backups/targets/${tg.id}/test`)
    toast.success(t('backup.targets.testOk', { ms: r.latency_ms ?? 0 }))
  } catch (e) {
    if (e instanceof ApiError && e.code.startsWith('sftp_host_key')) emit('edit', tg)
    toast.error(errorText(e))
  } finally { testing.value = '' }
}
async function measure(tg: Target) {
  measuring.value = tg.id
  try {
    const r = await api.get(`/workspaces/${session.current?.id}/backups/targets/${tg.id}/usage?actual=1`)
    actual.value = { ...actual.value, [tg.id]: r.actual }
  } catch (e) { toast.error(errorText(e)) } finally { measuring.value = '' }
}
async function remove(tg: Target) {
  if (!(await confirmDialog({ title: t('common.delete'), message: t('backup.targets.deleteConfirm', { name: tg.name }), danger: true, okText: t('common.delete') }))) return
  try {
    await api.del(`/workspaces/${session.current?.id}/backups/targets/${tg.id}`)
    emit('changed')
  } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <div>
    <div v-if="loaded && !targets.length" class="lm-empty big">
      <Icon name="cloud-up" :size="28" />
      <div>{{ $t('backup.targets.empty') }}</div>
      <div class="lm-faint hint">{{ $t('backup.targets.emptyHint') }}</div>
      <Btn v-if="session.atLeast('admin')" size="sm" icon="plus" @click="emit('edit', null)">{{ $t('backup.targets.new') }}</Btn>
    </div>
    <div v-else class="cards">
      <div v-for="tg in targets" :key="tg.id" class="card">
        <div class="ch">
          <Icon :name="icon(tg.kind)" :size="18" class="ic" />
          <div class="cm">
            <div class="name lm-ellipsis">{{ tg.name }}</div>
            <div class="kind">{{ kname(tg.kind) }}</div>
          </div>
          <IconBtn v-if="canManage(tg)" icon="edit" :size="15" :label="$t('common.edit')" @click="emit('edit', tg)" />
          <IconBtn v-if="canManage(tg)" icon="trash" :size="15" :label="usedBy(tg.id).length ? $t('backup.targets.inUse') : $t('common.delete')" :disabled="usedBy(tg.id).length > 0" @click="remove(tg)" />
        </div>
        <div class="use">
          <span class="lm-faint">{{ $t('backup.targets.usage') }}</span>
          <b>{{ formatBytes(tg.usage?.bytes || 0) }}</b>
          <span class="lm-faint">{{ $t('backup.summary.files', { n: tg.usage?.files || 0 }) }}</span>
        </div>
        <div v-if="actual[tg.id]" class="act lm-faint">{{ $t('backup.targets.usageActualResult', { files: actual[tg.id].files, size: formatBytes(actual[tg.id].bytes) }) }}</div>
        <div v-if="usedBy(tg.id).length" class="plans"><Icon name="archive" :size="12" />{{ usedBy(tg.id).map((p) => p.name).join('、') }}</div>
        <div class="ca">
          <Btn v-if="session.atLeast('admin')" size="sm" variant="ghost" icon="plug" :loading="testing === tg.id" @click="test(tg)">{{ $t('backup.targets.test') }}</Btn>
          <Btn size="sm" variant="ghost" :loading="measuring === tg.id" @click="measure(tg)">{{ $t('backup.targets.usageActual') }}</Btn>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.big { display: flex; flex-direction: column; align-items: center; gap: 8px; margin-top: 8vh; }
.hint { max-width: 520px; line-height: 1.6; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
.card { border: 1px solid var(--border); border-radius: var(--radius-l); background: var(--bg); padding: 12px; display: flex; flex-direction: column; gap: 8px; }
.ch { display: flex; align-items: center; gap: 10px; }
.ic { color: var(--text-2); flex: none; }
.cm { flex: 1; min-width: 0; }
.name { font-size: 13.5px; font-weight: 600; }
.kind { font-size: 12px; color: var(--text-3); }
.use { display: flex; align-items: baseline; gap: 8px; font-size: 12.5px; }
.use b { font-size: 15px; font-weight: 600; }
.act { font-size: 12px; }
.plans { font-size: 12px; color: var(--text-2); display: flex; align-items: center; gap: 4px; }
.ca { display: flex; gap: 4px; margin-top: 2px; margin-left: -8px; }
</style>
