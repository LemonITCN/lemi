<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import Modal from '@/components/ui/Modal.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'
import { confirmDialog } from '@/lib/confirm'

const session = useSession()
const toast = useToast()
const t = i18n.global.t
const members = ref<any[]>([])
const adding = ref(false)
const q = ref('')
const found = ref<any[]>([])
const addRole = ref('member')
const roles = ['viewer', 'member', 'admin', 'owner']

async function load() {
  if (!session.current) return
  try { members.value = (await api.get(`/workspaces/${session.current.id}/members`)).members } catch (e) { toast.error(errorText(e)) }
}
onMounted(load)
watch(() => session.current?.id, load)

let timer: any
watch(q, (v) => {
  clearTimeout(timer)
  timer = setTimeout(async () => {
    if (v.trim().length < 2) { found.value = []; return }
    try { found.value = (await api.get(`/workspaces/${session.current!.id}/user-search?q=${encodeURIComponent(v.trim())}`)).users } catch {}
  }, 250)
})

async function setRole(uid: string, role: string) {
  try {
    members.value = (await api.put(`/workspaces/${session.current!.id}/members/${uid}`, { role })).members
  } catch (e) { toast.error(errorText(e)); load() }
}

async function remove(m: any) {
  const self = m.user_id === session.user?.id
  if (!(await confirmDialog({ title: self ? t('settings.leaveTitle') : t('settings.removeMember', { name: m.display_name || m.username }), danger: true }))) return
  try {
    await api.del(`/workspaces/${session.current!.id}/members/${m.user_id}`)
    if (self) { await session.load(); return }
    load()
  } catch (e) { toast.error(errorText(e)) }
}

async function add(u: any) {
  await setRole(u.id, addRole.value)
  adding.value = false
  q.value = ''
}
</script>

<template>
  <div class="lm-section">
    <p class="desc">{{ $t('settings.membersHint') }}</p>
    <div class="lm-card">
      <table class="lm-table">
        <thead><tr><th>{{ $t('common.user') }}</th><th>{{ $t('auth.email') }}</th><th>{{ $t('common.role') }}</th><th /></tr></thead>
        <tbody>
          <tr v-for="m in members" :key="m.user_id">
            <td>{{ m.display_name || m.username }} <span class="lm-faint">@{{ m.username }}</span></td>
            <td class="lm-faint">{{ m.email }}</td>
            <td>
              <select v-if="session.atLeast('admin') && m.user_id !== session.user?.id" class="lm-select sm" :value="m.role" @change="setRole(m.user_id, ($event.target as HTMLSelectElement).value)">
                <option v-for="r in roles" :key="r" :value="r" :disabled="r === 'owner' && session.role !== 'owner' && !session.isAdmin">{{ $t('settings.roles.' + r) }}</option>
              </select>
              <span v-else class="lm-badge">{{ $t('settings.roles.' + m.role) }}</span>
            </td>
            <td class="r"><Btn v-if="session.atLeast('admin') || m.user_id === session.user?.id" size="sm" variant="ghost" @click="remove(m)">{{ m.user_id === session.user?.id ? $t('settings.leave') : $t('common.remove') }}</Btn></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-if="session.atLeast('admin')" class="actions"><Btn icon="plus" @click="adding = true">{{ $t('settings.addMember') }}</Btn></div>
  </div>
  <div class="lm-section">
    <h3>{{ $t('settings.rolesTitle') }}</h3>
    <ul class="roles">
      <li v-for="r in roles" :key="r"><b>{{ $t('settings.roles.' + r) }}</b>：{{ $t('settings.roleDesc.' + r) }}</li>
    </ul>
  </div>
  <Modal v-model:open="adding" :title="$t('settings.addMember')">
    <div class="add">
      <TextField v-model="q" :placeholder="$t('settings.searchUser')" autofocus />
      <select v-model="addRole" class="lm-select"><option v-for="r in roles.slice(0, 3)" :key="r" :value="r">{{ $t('settings.roles.' + r) }}</option></select>
      <div class="found">
        <button v-for="u in found" :key="u.id" class="u" @click="add(u)">{{ u.display_name || u.username }} <span class="lm-faint">@{{ u.username }}</span></button>
        <p v-if="q.length >= 2 && !found.length" class="lm-faint">{{ $t('settings.noUser') }}</p>
      </div>
    </div>
  </Modal>
</template>

<style scoped>
.actions { margin-top: 14px; }
.r { text-align: right; }
.sm { height: 28px; width: auto; }
.roles { margin: 0; padding-left: 18px; color: var(--text-2); font-size: 13px; line-height: 1.8; }
.add { display: flex; flex-direction: column; gap: 10px; }
.found { display: flex; flex-direction: column; gap: 2px; min-height: 60px; }
.u { text-align: left; border: 0; background: none; padding: 8px; border-radius: 6px; }
.u:hover { background: var(--bg-hover); }
</style>
