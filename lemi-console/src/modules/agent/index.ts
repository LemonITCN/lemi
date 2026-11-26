// 智能体：右侧对话面板、模型供应商与 MCP 设置、OAuth 授权确认页。
import { defineModule } from '@/modules/registry'

export default defineModule({
  name: 'agent',
  rightPanel: { icon: 'assistant', label: 'agent.title', component: () => import('./AgentPanel.vue') },
  topRoutes: [{ path: '/connect/authorize', name: 'oauth-consent', component: () => import('./ConsentView.vue') }],
  settings: [
    { id: 'ai-me', label: 'agent.settings.me', icon: 'assistant', group: 'account', order: 40, component: () => import('./settings/PersonalPage.vue') },
    { id: 'mcp', label: 'agent.settings.mcp', icon: 'plug', group: 'account', order: 50, component: () => import('./settings/McpPage.vue') },
    { id: 'ai-workspace', label: 'agent.settings.workspace', icon: 'assistant', group: 'workspace', order: 40, component: () => import('./settings/WorkspacePage.vue'), minRole: 'viewer' },
    { id: 'ai-providers', label: 'agent.settings.providers', icon: 'sparkle', group: 'admin', order: 30, component: () => import('./settings/ProvidersPage.vue') },
  ],
})
