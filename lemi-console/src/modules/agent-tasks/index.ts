// 定时智能体任务：计划、提示词、模型、连接档位（只能收紧）、通知；运行记录与完整对话。
import { defineModule } from '@/modules/registry'

export default defineModule({
  name: 'agent-tasks',
  routes: [{ path: 'agent-tasks/:id?', name: 'agent-tasks', component: () => import('./TasksView.vue') }],
  rail: [{ id: 'agent-tasks', icon: 'task', label: 'tasks.title', order: 30, route: '/agent-tasks' }],
})
