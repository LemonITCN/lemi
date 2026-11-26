<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Modal from '@/components/ui/Modal.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'
import { withReauth } from '@/lib/reauth'

const session = useSession()
const toast = useToast()
const t = i18n.global.t
const f = reactive({ name: '', description: '' })
watch(() => session.current, (w) => { f.name = w?.name || ''; f.description = w?.description || '' }, { immediate: true })
const busy = ref(false)
const creating = ref(false)
const newName = ref('')
const deleting = ref(false)
const confirmName = ref('')

async function save() {
  busy.value = true
  try {
    await api.patch(`/workspaces/${session.current!.id}`, f)
    await session.load()
    toast.success(t('common.saved'))
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

async function create() {
  try {
    const w = await api.post('/workspaces', { name: newName.value })
    await session.load()
    session.selectWorkspace(w.id)
    creating.value = false
    newName.value = ''
  } catch (e) { toast.error(errorText(e)) }
}

async function remove() {
  try {
    await withReauth(() => api.del(`/workspaces/${session.current!.id}`, { confirm_name: confirmName.value }))
    deleting.value = false
    await session.load()
    if (session.workspaces[0]) session.selectWorkspace(session.workspaces[0].id)
  } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <div v-if="session.current" class="lm-section">
    <h3>{{ $t('settings.wsBasic') }}</h3>
    <div class="lm-form">
      <TextField v-model="f.name" :label="$t('common.name')" :disabled="!session.atLeast('admin')" />
      <TextField v-model="f.description" :label="$t('common.description')" :disabled="!session.atLeast('admin')" />
    </div>
    <div class="actions">
      <Btn v-if="session.atLeast('admin')" variant="primary" :loading="busy" @click="save">{{ $t('common.save') }}</Btn>
      <span class="lm-badge">{{ $t('settings.myRole') }}：{{ $t('settings.roles.' + (session.role || 'none')) }}</span>
    </div>
  </div>
  <div class="lm-section">
    <h3>{{ $t('settings.wsNew') }}</h3>
    <p class="desc">{{ $t('settings.wsNewHint') }}</p>
    <Btn icon="plus" @click="creating = true">{{ $t('settings.wsNew') }}</Btn>
  </div>
  <div v-if="session.current && (session.role === 'owner' || session.isAdmin)" class="lm-section danger">
    <h3>{{ $t('settings.wsDelete') }}</h3>
    <p class="desc">{{ $t('settings.wsDeleteHint') }}</p>
    <Btn variant="danger" @click="deleting = true">{{ $t('settings.wsDelete') }}</Btn>
  </div>
  <Modal v-model:open="creating" :title="$t('settings.wsNew')">
    <TextField v-model="newName" :label="$t('common.name')" autofocus />
    <template #footer><Btn @click="creating = false">{{ $t('common.cancel') }}</Btn><Btn variant="primary" :disabled="!newName.trim()" @click="create">{{ $t('common.create') }}</Btn></template>
  </Modal>
  <Modal v-model:open="deleting" :title="$t('settings.wsDelete')">
    <p class="lm-muted">{{ $t('settings.wsDeleteConfirm', { name: session.current?.name }) }}</p>
    <TextField v-model="confirmName" autofocus />
    <template #footer><Btn @click="deleting = false">{{ $t('common.cancel') }}</Btn><Btn variant="danger" :disabled="confirmName !== session.current?.name" @click="remove">{{ $t('common.delete') }}</Btn></template>
  </Modal>
</template>

<style scoped>
.actions { margin-top: 14px; display: flex; align-items: center; gap: 12px; }
.danger h3 { color: var(--danger); }
</style>
