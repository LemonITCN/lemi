// 智能体操作主工作区的浏览器端工具（§16.5.1）。工具实现按需加载，首屏不带上。
import { defineModule } from '@/modules/registry'

const lazy = (name: 'workspace_open_table' | 'workspace_run_sql' | 'workspace_update_view' | 'workspace_read_view') =>
  async (args: any) => (await import('./tools'))[name](args)

export default defineModule({
  name: 'workspace-tools',
  clientTools: {
    workspace_open_table: lazy('workspace_open_table'),
    workspace_run_sql: lazy('workspace_run_sql'),
    workspace_update_view: lazy('workspace_update_view'),
    workspace_read_view: lazy('workspace_read_view'),
  },
})
