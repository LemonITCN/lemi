import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api, setCsrf } from '@/api/client'
import { setLocale } from '@/i18n'

export interface User {
  id: string
  username: string
  display_name: string
  email: string
  role: 'admin' | 'user'
  totp_enabled: boolean
  must_change_password: boolean
  locale: string
  recovery_left: number
}

export interface Workspace {
  id: string
  name: string
  description: string
  my_role: '' | 'viewer' | 'member' | 'admin' | 'owner'
  created_at: number
}

const roleRank: Record<string, number> = { viewer: 1, member: 2, admin: 3, owner: 4 }

export const useSession = defineStore('session', () => {
  const loaded = ref(false)
  const needsSetup = ref(false)
  const insecure = ref(false)
  const version = ref('')
  const user = ref<User | null>(null)
  const stage = ref<'' | 'mfa' | 'totp_setup' | 'done'>('')
  const workspaces = ref<Workspace[]>([])
  const currentId = ref<string>(localStorage.getItem('lemi.ws') || '')
  const extra = ref<Record<string, any>>({})

  const current = computed(() => workspaces.value.find((w) => w.id === currentId.value) || workspaces.value[0] || null)
  const role = computed(() => {
    const w = current.value
    if (!w) return ''
    if (w.my_role) return w.my_role
    return user.value?.role === 'admin' ? 'admin' : ''
  })
  const isAdmin = computed(() => user.value?.role === 'admin')

  function apply(d: any) {
    loaded.value = true
    needsSetup.value = !!d.needs_setup
    insecure.value = !!d.insecure
    version.value = d.version || ''
    user.value = d.user || null
    stage.value = d.stage || ''
    setCsrf(d.csrf)
    if (d.workspaces) workspaces.value = d.workspaces
    const { needs_setup, insecure: _i, version: _v, user: _u, stage: _s, csrf: _c, workspaces: _w, ...rest } = d
    extra.value = rest
    if (user.value?.locale) setLocale(user.value.locale)
    if (current.value && current.value.id !== currentId.value) selectWorkspace(current.value.id)
  }

  async function load() {
    apply(await api.get('/bootstrap'))
  }

  function selectWorkspace(id: string) {
    currentId.value = id
    try { localStorage.setItem('lemi.ws', id) } catch {}
  }

  async function logout() {
    try { await api.post('/auth/logout') } catch {}
    user.value = null
    stage.value = ''
    workspaces.value = []
    setCsrf('')
  }

  function atLeast(min: 'viewer' | 'member' | 'admin' | 'owner') {
    return (roleRank[role.value] || 0) >= roleRank[min]
  }

  return { loaded, needsSetup, insecure, version, user, stage, workspaces, currentId, current, role, isAdmin, extra, apply, load, selectWorkspace, logout, atLeast }
})
