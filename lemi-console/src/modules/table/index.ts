import { defineModule } from '@/modules/registry'

export default defineModule({
  name: 'table',
  tabs: [{ type: 'table', icon: 'table', component: () => import('./TableView.vue') }],
})
