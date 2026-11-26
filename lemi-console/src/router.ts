import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { useSession } from '@/stores/session'
import { modules } from '@/modules/registry'
import { onAuthError } from '@/api/client'

const shellChildren: RouteRecordRaw[] = [
  { path: '', name: 'workbench', component: () => import('@/workbench/Workbench.vue') },
  ...modules.flatMap((m) => m.routes || []),
]

const routes: RouteRecordRaw[] = [
  { path: '/login', name: 'login', component: () => import('@/views/auth/LoginView.vue'), meta: { public: true } },
  { path: '/setup', name: 'setup', component: () => import('@/views/auth/SetupView.vue'), meta: { public: true } },
  { path: '/totp-setup', name: 'totp-setup', component: () => import('@/views/auth/TotpSetupView.vue'), meta: { public: true } },
  { path: '/invite/:token', name: 'invite', component: () => import('@/views/auth/InviteView.vue'), meta: { public: true } },
  { path: '/change-password', name: 'change-password', component: () => import('@/views/auth/ChangePasswordView.vue'), meta: { public: true } },
  ...modules.flatMap((m) => m.topRoutes || []),
  { path: '/', component: () => import('@/views/AppShell.vue'), children: shellChildren },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export const router = createRouter({ history: createWebHistory(), routes })

router.beforeEach(async (to) => {
  const s = useSession()
  if (!s.loaded) {
    try { await s.load() } catch { /* 网络错误时留在当前页 */ }
  }
  const pub = to.meta.public === true
  if (s.needsSetup) return to.name === 'setup' ? true : { name: 'setup' }
  if (to.name === 'setup') return { path: '/' }
  if (to.meta.anyAuth) return true
  if (!s.user || s.stage === 'mfa' || s.stage === '') {
    if (pub && to.name !== 'totp-setup' && to.name !== 'change-password') return true
    return { name: 'login', query: to.fullPath !== '/' ? { next: to.fullPath } : undefined }
  }
  if (s.stage === 'totp_setup') return to.name === 'totp-setup' ? true : { name: 'totp-setup' }
  if (s.user.must_change_password) return to.name === 'change-password' ? true : { name: 'change-password' }
  if (to.name === 'login' || to.name === 'totp-setup' || to.name === 'change-password') return { path: '/' }
  return true
})

onAuthError((e) => {
  const s = useSession()
  if (e.code === 'session_invalid' || e.code === 'unauthorized') {
    s.user = null
    s.stage = ''
    router.replace({ name: 'login' })
  } else if (e.code === 'totp_setup_required') {
    s.stage = 'totp_setup'
    router.replace({ name: 'totp-setup' })
  } else if (e.code === 'must_change_password') {
    router.replace({ name: 'change-password' })
  }
})
