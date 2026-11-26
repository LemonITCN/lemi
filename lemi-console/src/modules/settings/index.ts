import { defineModule } from '@/modules/registry'

export default defineModule({
  name: 'settings',
  routes: [{ path: 'settings/:section?', name: 'settings', component: () => import('./SettingsView.vue') }],
  settings: [
    { id: 'account', label: 'settings.account', icon: 'user', group: 'account', order: 10, component: () => import('./AccountPage.vue') },
    { id: 'security', label: 'settings.security', icon: 'shield', group: 'account', order: 20, component: () => import('./SecurityPage.vue') },
    { id: 'workspace', label: 'settings.workspace', icon: 'settings', group: 'workspace', order: 10, component: () => import('./WorkspacePage.vue'), minRole: 'viewer' },
    { id: 'members', label: 'settings.members', icon: 'users', group: 'workspace', order: 20, component: () => import('./MembersPage.vue'), minRole: 'viewer' },
    { id: 'ws-audit', label: 'settings.audit', icon: 'history', group: 'workspace', order: 90, component: () => import('./AuditPage.vue'), minRole: 'admin' },
    { id: 'users', label: 'settings.users', icon: 'users', group: 'admin', order: 10, component: () => import('./AdminUsersPage.vue') },
    { id: 'system', label: 'settings.system', icon: 'server', group: 'admin', order: 20, component: () => import('./AdminSystemPage.vue') },
    { id: 'audit', label: 'settings.auditAll', icon: 'history', group: 'admin', order: 80, component: () => import('./AuditPage.vue') },
    { id: 'about', label: 'settings.about', icon: 'info', group: 'admin', order: 99, component: () => import('./AboutPage.vue') },
  ],
  rail: [{ id: 'settings', icon: 'settings', label: 'common.settings', order: 1000, route: '/settings' }],
})
