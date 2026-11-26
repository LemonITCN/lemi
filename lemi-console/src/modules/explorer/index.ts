import { defineModule } from '@/modules/registry'

export default defineModule({
  name: 'explorer',
  rail: [{ id: 'explorer', icon: 'database', label: 'explorer.title', order: 10, panel: () => import('./ExplorerPanel.vue'), mobile: true }],
})
